package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func isolateWebmuxTest(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "webmux-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	t.Setenv("HOME", root)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	return root
}

func TestAllocatePanePortReusesFreedPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	occupiedPort := listener.Addr().(*net.TCPAddr).Port
	if occupiedPort >= 65535 {
		listener.Close()
		t.Skip("ephemeral port has no following port")
	}
	manager := &PaneManager{startPort: int32(occupiedPort - 1)}
	got, err := manager.allocatePanePort()
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	if got == occupiedPort {
		listener.Close()
		t.Fatalf("allocated occupied port %d", got)
	}
	listener.Close()

	got, err = manager.allocatePanePort()
	if err != nil {
		t.Fatal(err)
	}
	if got != occupiedPort {
		t.Fatalf("allocated port %d after release, want reused port %d", got, occupiedPort)
	}
}

func TestTerminalPaneRestoresOnlyWhileTmuxSurvives(t *testing.T) {
	if _, err := execLookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	isolateWebmuxTest(t)
	instance := "restore-terminal-test"
	manager := NewPaneManager(24000, "/bin/sh", t.TempDir(), instance)
	server := NewServer(manager, t.TempDir())
	pane, err := manager.CreatePane("terminal", "survivor")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.terminal.Shutdown(time.Second) })
	server.saveInstanceState()

	manager.mu.Lock()
	manager.panes = make(map[string]*Pane)
	manager.mu.Unlock()
	time.Sleep(20 * time.Millisecond)

	restoredManager := NewPaneManager(24000, "/bin/sh", t.TempDir(), instance)
	restoredServer := NewServer(restoredManager, t.TempDir())
	if restored, ok := restoredManager.GetPane(pane.ID); !ok || restored.Name != "survivor" {
		restoredServer.forceCloseAllBackends(time.Second)
		t.Fatalf("terminal pane was not adopted: pane=%+v ok=%v", restored, ok)
	}
	restoredServer.forceCloseAllBackends(time.Second)

	coldManager := NewPaneManager(24000, "/bin/sh", t.TempDir(), instance)
	coldServer := NewServer(coldManager, t.TempDir())
	if got := coldManager.ListPanes(); len(got) != 0 {
		t.Fatalf("cold startup recreated terminal panes: %+v", got)
	}
	coldServer.forceCloseAllBackends(time.Second)
}

func execLookPath(name string) (string, error) {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			return path, nil
		}
	}
	return "", os.ErrNotExist
}

func TestOpenCodeBackendPersistsWithoutPaneAndIsAdopted(t *testing.T) {
	root := isolateWebmuxTest(t)
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	fakeOpenCode := filepath.Join(binDir, "opencode")
	script := "#!/bin/sh\nexec \"$WEBMUX_TEST_BINARY\" -test.run=TestOpenCodeHelperProcess -- opencode \"$@\"\n"
	if err := os.WriteFile(fakeOpenCode, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEBMUX_TEST_BINARY", testBinary)
	t.Setenv("WEBMUX_OPENCODE_HELPER", "1")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	basePort := availablePort(t) - 1
	manager := NewPaneManager(basePort, "/bin/sh", root, "restore-opencode-test")
	server := NewServer(manager, filepath.Join(root, "uploads"))
	pane, err := manager.CreatePane("opencode", "shared")
	if err != nil {
		t.Fatal(err)
	}
	backend, ok := manager.opencode.PersistedBackend("opencode")
	if !ok {
		t.Fatal("started OpenCode backend was not persistable")
	}
	t.Cleanup(func() {
		if openCodeProcessMatches(backend) {
			_ = syscall.Kill(-backend.ProcessGroup, syscall.SIGKILL)
		}
	})
	if err := manager.ClosePane(pane.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		state, err := LoadInstanceState(manager.instanceID)
		return err == nil && state != nil && len(state.Panes) == 0 && len(state.Backends) == 1
	})
	if !manager.opencode.IsRunning("opencode") {
		t.Fatal("closing the last OpenCode pane stopped its instance backend")
	}

	time.Sleep(50 * time.Millisecond)
	server.saveInstanceState()
	state, _ := manager.opencode.getState("opencode")
	manager.opencode.removeState("opencode", state)
	restoredManager := NewPaneManager(basePort, "/bin/sh", root, "restore-opencode-test")
	restoredServer := NewServer(restoredManager, filepath.Join(root, "uploads-2"))
	restoredPort, ok := restoredManager.opencode.RunningBackend("opencode")
	if !ok || restoredPort != backend.Port {
		restoredServer.forceCloseAllBackends(time.Second)
		t.Fatalf("OpenCode backend was not adopted: port=%d ok=%v want=%d", restoredPort, ok, backend.Port)
	}

	restoredServer.forceCloseAllBackends(2 * time.Second)
	waitFor(t, 2*time.Second, func() bool { return syscall.Kill(backend.PID, 0) != nil })
}

func TestOpenCodeHelperProcess(t *testing.T) {
	if os.Getenv("WEBMUX_OPENCODE_HELPER") != "1" {
		return
	}
	port := 0
	for i, arg := range os.Args {
		if arg == "--port" && i+1 < len(os.Args) {
			port, _ = strconv.Atoi(os.Args[i+1])
		}
	}
	if port == 0 {
		os.Exit(2)
	}
	server := &http.Server{
		Addr: fmt.Sprintf("127.0.0.1:%d", port),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<!doctype html><html><body>test opencode</body></html>"))
		}),
	}
	if err := server.ListenAndServe(); err != nil {
		os.Exit(3)
	}
}

func availablePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	if port <= 1024 {
		t.Fatalf("unexpected ephemeral port %d", port)
	}
	return port
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}

func TestBackendLifetimeIsIndependentOfScope(t *testing.T) {
	manager := &PaneManager{}
	if got := manager.backendScope("opencode"); got != PaneBackendShared {
		t.Fatalf("OpenCode scope = %q", got)
	}
	if got := manager.backendLifetime("opencode"); got != PaneBackendLifetimeInstance {
		t.Fatalf("OpenCode lifetime = %q", got)
	}
	if strings.TrimSpace(manager.backendLifetime("terminal")) != PaneBackendLifetimePane {
		t.Fatal("terminal backend should remain pane-scoped")
	}
}

func TestInvalidInstanceStateIsNotOverwritten(t *testing.T) {
	isolateWebmuxTest(t)
	instance := "invalid-state-test"
	path := instanceStateFilePath(instanceIDForPort(instance))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("{not valid json")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	manager := NewPaneManager(26000, "/bin/sh", t.TempDir(), instance)
	server := NewServer(manager, t.TempDir())
	if server.stateLoadErr == nil {
		t.Fatal("invalid state did not disable persistence")
	}
	server.saveInstanceState()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("invalid state was overwritten: %q", got)
	}
}

func TestOpenCodeShutdownRejectsChangedProcessIdentity(t *testing.T) {
	state := &OpenCodePaneState{
		pid: os.Getpid(),
		identity: PersistedBackend{
			PID: os.Getpid(), Port: 1, ProcessGroup: os.Getpid(),
			StartTime: 1, BootID: "wrong", Token: "wrong",
		},
		done: make(chan struct{}),
	}
	runtime := &OpenCodeRuntime{states: map[string]*OpenCodePaneState{"opencode": state}}
	if runtime.stopState("opencode", state, 10*time.Millisecond) {
		t.Fatal("shutdown accepted a changed process identity")
	}
	if _, ok := runtime.getState("opencode"); !ok {
		t.Fatal("failed shutdown discarded recovery state")
	}
}

func TestUIStateValidatesAndUpdatesFocusedPane(t *testing.T) {
	manager := &PaneManager{panes: map[string]*Pane{
		"pane-1": {ID: "pane-1"},
		"pane-2": {ID: "pane-2"},
		"pane-3": {ID: "pane-3"},
	}}
	server := &Server{manager: manager}
	state := &UIState{
		Groups: []UIGroup{
			{ID: "group-1", PaneIDs: []string{"pane-1", "pane-2"}},
			{ID: "group-2", PaneIDs: []string{"pane-3"}},
		},
		GroupOrder:       []string{"group-1", "group-2"},
		ActiveGroupID:    "group-1",
		FocusedPaneID:    "pane-3",
		AttentionPaneIDs: []string{"pane-1", "missing-pane", "pane-1"},
	}
	validated := server.validateUIState(state)
	if validated.FocusedPaneID != "pane-1" {
		t.Fatalf("focused pane = %q, want first pane in active group", validated.FocusedPaneID)
	}
	if !slices.Equal(validated.AttentionPaneIDs, []string{"pane-1"}) {
		t.Fatalf("attention panes = %v, want [pane-1]", validated.AttentionPaneIDs)
	}

	validated.FocusedPaneID = "pane-1"
	server.uiState = validated
	server.removePaneFromUIState("pane-1")
	if server.uiState.FocusedPaneID != "pane-2" {
		t.Fatalf("focused pane after removal = %q, want pane-2", server.uiState.FocusedPaneID)
	}
	if len(server.uiState.AttentionPaneIDs) != 0 {
		t.Fatalf("attention panes after removal = %v, want none", server.uiState.AttentionPaneIDs)
	}
}

func TestScratchPersistsPerInstance(t *testing.T) {
	isolateWebmuxTest(t)
	instance := instanceIDForPort("scratch-test")
	if err := SaveScratch(instance, "persistent notes\nsecond line"); err != nil {
		t.Fatal(err)
	}
	got, err := LoadScratch(instance)
	if err != nil {
		t.Fatal(err)
	}
	if got != "persistent notes\nsecond line" {
		t.Fatalf("loaded scratch = %q", got)
	}
	other, err := LoadScratch(instanceIDForPort("other-scratch-test"))
	if err != nil {
		t.Fatal(err)
	}
	if other != "" {
		t.Fatalf("scratch leaked across instances: %q", other)
	}
	info, err := os.Stat(scratchFilePath(instance))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("scratch permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestUIStateRejectsStaleRevision(t *testing.T) {
	isolateWebmuxTest(t)
	manager := &PaneManager{
		panes:      map[string]*Pane{"pane-1": {ID: "pane-1"}},
		opencode:   &OpenCodeRuntime{states: make(map[string]*OpenCodePaneState)},
		instanceID: "revision-test",
	}
	server := &Server{
		manager: manager,
		uiState: &UIState{
			Revision: 4, Groups: []UIGroup{{ID: "group-1", PaneIDs: []string{"pane-1"}}},
			GroupOrder: []string{"group-1"}, ActiveGroupID: "group-1", FocusedPaneID: "pane-1",
		},
	}

	state := cloneUIState(server.uiState)
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/ui-state", bytes.NewReader(body))
	response := httptest.NewRecorder()
	server.handleUIState(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("current revision status = %d", response.Code)
	}
	if server.uiState.Revision != 5 {
		t.Fatalf("saved revision = %d, want 5", server.uiState.Revision)
	}

	staleRequest := httptest.NewRequest(http.MethodPost, "/api/ui-state", bytes.NewReader(body))
	staleResponse := httptest.NewRecorder()
	server.handleUIState(staleResponse, staleRequest)
	if staleResponse.Code != http.StatusConflict {
		t.Fatalf("stale revision status = %d, want 409", staleResponse.Code)
	}
	if server.uiState.Revision != 5 {
		t.Fatalf("stale write changed revision to %d", server.uiState.Revision)
	}
}
