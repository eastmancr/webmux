package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestPaneTypesIncludesOpenCodeWarning(t *testing.T) {
	isolateWebmuxTest(t)
	manager := NewPaneManager(10000, "/bin/sh", "", "8080")
	defer os.Remove(manager.terminal.tmuxConfigPath)
	defer os.RemoveAll(manager.terminal.wmBinDir)
	manager.opencode.setWarningReason("OpenCode schema changed")

	found := false
	for _, paneType := range manager.PaneTypes() {
		if paneType.Type != "opencode" {
			continue
		}
		found = true
		if paneType.WarningReason != "OpenCode schema changed" {
			t.Fatalf("WarningReason = %q, want warning", paneType.WarningReason)
		}
	}
	if !found {
		t.Fatal("opencode pane type not found")
	}
}

func TestCachedOpenCodeVersionRefreshesAfterTTL(t *testing.T) {
	isolateWebmuxTest(t)
	dir := t.TempDir()
	command := filepath.Join(dir, "opencode")
	writeVersionCommand := func(version string) {
		t.Helper()
		content := "#!/bin/sh\nprintf '%s\\n' '" + version + "'\n"
		if err := os.WriteFile(command, []byte(content), 0755); err != nil {
			t.Fatalf("write opencode command: %v", err)
		}
	}
	writeVersionCommand("version-one")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	manager := NewPaneManager(10000, "/bin/sh", "", "8080")
	defer os.Remove(manager.terminal.tmuxConfigPath)
	defer os.RemoveAll(manager.terminal.wmBinDir)

	if got := manager.cachedOpenCodeVersion(); got != "version-one" {
		t.Fatalf("initial version = %q, want version-one", got)
	}
	writeVersionCommand("version-two")
	if got := manager.cachedOpenCodeVersion(); got != "version-one" {
		t.Fatalf("cached version = %q, want version-one", got)
	}

	manager.versionMu.Lock()
	manager.opencodeVersionCheckedAt = time.Now().Add(-paneTypeVersionTTL)
	manager.versionMu.Unlock()
	if got := manager.cachedOpenCodeVersion(); got != "version-two" {
		t.Fatalf("refreshed version = %q, want version-two", got)
	}
}

func TestCanonicalPaneViewsUseVisualOrderAndDerivedFields(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	panes := []*Pane{
		{ID: "pane-5", Type: "terminal", Name: "", CreatedAt: base.Add(4 * time.Minute)},
		{ID: "pane-3", Type: "terminal", Name: "", CreatedAt: base.Add(2 * time.Minute)},
		{ID: "pane-1", Type: "terminal", Name: "custom", CreatedAt: base},
		{ID: "pane-4", Type: "terminal", Name: "", CreatedAt: base.Add(3 * time.Minute)},
		{ID: "pane-2", Type: "terminal", Name: "", CreatedAt: base.Add(time.Minute)},
	}
	state := &UIState{
		Groups: []UIGroup{
			{ID: "first", PaneIDs: []string{"pane-2"}},
			{ID: "second", PaneIDs: []string{"pane-3", "pane-1"}, CellMapping: []int{1}},
		},
		GroupOrder:    []string{"second", "first"},
		ActiveGroupID: "second", FocusedPaneID: "pane-3",
	}

	views := buildPaneViews(panes, validateUIStateForPanes(state, panes))
	gotIDs := make([]string, len(views))
	for i := range views {
		gotIDs[i] = views[i].ID
	}
	if want := []string{"pane-1", "pane-3", "pane-2", "pane-4", "pane-5"}; !slices.Equal(gotIDs, want) {
		t.Fatalf("pane order = %v, want %v", gotIDs, want)
	}
	if views[0].Position != 1 || views[0].DisplayName != "custom" {
		t.Fatalf("named pane view = %+v", views[0])
	}
	if views[1].Position != 2 || views[1].DisplayName != "2" || !views[1].Focused {
		t.Fatalf("focused unnamed pane view = %+v", views[1])
	}
	if views[2].DisplayName != "3" {
		t.Fatalf("named panes did not consume a position: %+v", views[2])
	}
	if state.Groups[1].CellMapping[0] != 1 || state.GroupOrder[0] != "second" {
		t.Fatal("building pane views mutated UI state")
	}
}

func TestPanesAPIUsesAdditivePaneView(t *testing.T) {
	created := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	pane := &Pane{
		ID: "pane-1", Type: "terminal", BackendID: "pane-1",
		BackendScope: PaneBackendDedicated, BackendLifetime: PaneBackendLifetimePane,
		Port: 1234, CreatedAt: created, CurrentActivity: "running",
	}
	server := &Server{
		manager: &PaneManager{panes: map[string]*Pane{pane.ID: pane}},
		uiState: &UIState{
			Groups:     []UIGroup{{ID: "group-1", PaneIDs: []string{pane.ID}}},
			GroupOrder: []string{"group-1"}, ActiveGroupID: "group-1", FocusedPaneID: pane.ID,
		},
	}
	recorder := httptest.NewRecorder()
	server.handlePanes(recorder, httptest.NewRequest(http.MethodGet, "/api/panes", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var views []PaneView
	if err := json.Unmarshal(recorder.Body.Bytes(), &views); err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].ID != pane.ID || views[0].BackendID != pane.BackendID ||
		views[0].CurrentActivity != pane.CurrentActivity || views[0].CreatedAt != created ||
		views[0].Position != 1 || views[0].DisplayName != "1" || !views[0].Focused {
		t.Fatalf("pane view = %+v", views)
	}
}

func TestCreatePaneAPITrimsCustomAndEmptyNames(t *testing.T) {
	if _, err := execLookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	isolateWebmuxTest(t)
	manager := NewPaneManager(28000, "/bin/sh", t.TempDir(), "pane-title-create-test")
	server := NewServer(manager, t.TempDir())
	t.Cleanup(func() { server.forceCloseAllBackends(time.Second) })

	for i, test := range []struct {
		name        string
		wantName    string
		wantDisplay string
	}{
		{name: "  custom title\n", wantName: "custom title", wantDisplay: "custom title"},
		{name: " \t ", wantName: "", wantDisplay: "2"},
	} {
		body, err := json.Marshal(map[string]string{"type": "terminal", "name": test.name})
		if err != nil {
			t.Fatal(err)
		}
		recorder := httptest.NewRecorder()
		server.handlePanes(recorder, httptest.NewRequest(http.MethodPost, "/api/panes", bytes.NewReader(body)))
		if recorder.Code != http.StatusCreated {
			t.Fatalf("create %d status = %d: %s", i, recorder.Code, recorder.Body.String())
		}
		var view PaneView
		if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if view.Name != test.wantName || view.DisplayName != test.wantDisplay || view.Position != i+1 {
			t.Fatalf("create %d view = %+v", i, view)
		}
	}
}

func TestRenamePaneTrimsCustomNameAndAllowsUnnamed(t *testing.T) {
	manager := &PaneManager{panes: map[string]*Pane{"pane-1": {ID: "pane-1", Name: "old"}}}
	if err := manager.RenamePane("pane-1", "  custom title\n"); err != nil {
		t.Fatal(err)
	}
	if pane, _ := manager.GetPane("pane-1"); pane.Name != "custom title" {
		t.Fatalf("trimmed name = %q", pane.Name)
	}
	if err := manager.RenamePane("pane-1", " \t "); err != nil {
		t.Fatal(err)
	}
	if pane, _ := manager.GetPane("pane-1"); pane.Name != "" {
		t.Fatalf("empty name = %q", pane.Name)
	}
}

func TestPaneEventsRemainRawPanes(t *testing.T) {
	data, err := json.Marshal(paneEvent{Type: "created", Pane: &Pane{ID: "pane-1", Name: ""}})
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	pane := event["pane"].(map[string]any)
	if _, ok := pane["position"]; ok {
		t.Fatalf("pane event included API view fields: %s", data)
	}
	if _, ok := pane["displayName"]; ok {
		t.Fatalf("pane event included API view fields: %s", data)
	}
}
