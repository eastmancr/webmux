package main

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

type shortWriter struct {
	bytes.Buffer
	max int
}

func (w *shortWriter) Write(data []byte) (int, error) {
	if len(data) > w.max {
		data = data[:w.max]
	}
	return w.Buffer.Write(data)
}

func TestValidTerminalSize(t *testing.T) {
	tests := []struct {
		name       string
		cols, rows int
		want       bool
	}{
		{name: "normal", cols: 120, rows: 40, want: true},
		{name: "minimum", cols: 2, rows: 1, want: true},
		{name: "maximum", cols: maxTerminalCols, rows: maxTerminalRows, want: true},
		{name: "too narrow", cols: 1, rows: 24, want: false},
		{name: "zero rows", cols: 80, rows: 0, want: false},
		{name: "too wide", cols: maxTerminalCols + 1, rows: 24, want: false},
		{name: "too tall", cols: 80, rows: maxTerminalRows + 1, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validTerminalSize(tt.cols, tt.rows); got != tt.want {
				t.Fatalf("validTerminalSize(%d, %d) = %v, want %v", tt.cols, tt.rows, got, tt.want)
			}
		})
	}
}

func TestValidTerminalPixels(t *testing.T) {
	tests := []struct {
		width, height int
		want          bool
	}{
		{width: 1280, height: 720, want: true},
		{width: 0, height: 0, want: true},
		{width: 0, height: 720, want: false},
		{width: 1280, height: 0, want: false},
		{width: -1, height: 720, want: false},
		{width: maxTerminalPixels + 1, height: 720, want: false},
	}

	for _, tt := range tests {
		if got := validTerminalPixels(tt.width, tt.height); got != tt.want {
			t.Errorf("validTerminalPixels(%d, %d) = %v, want %v", tt.width, tt.height, got, tt.want)
		}
	}
}

func TestTerminalClientEnvironment(t *testing.T) {
	env := terminalClientEnvironment([]string{
		"PATH=/usr/bin",
		"TERM=linux",
		"COLORTERM=old",
		"TERM_PROGRAM=other",
	})

	for _, expected := range []string{
		"PATH=/usr/bin",
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"TERM_PROGRAM=webmux",
	} {
		if !slices.Contains(env, expected) {
			t.Errorf("terminal environment missing %q: %v", expected, env)
		}
	}
	for _, stale := range []string{"TERM=linux", "COLORTERM=old", "TERM_PROGRAM=other"} {
		if slices.Contains(env, stale) {
			t.Errorf("terminal environment retained stale value %q: %v", stale, env)
		}
	}
}

func TestTerminalOriginAllowed(t *testing.T) {
	tests := []struct {
		name        string
		host        string
		origin      string
		forwarded   http.Header
		wantAllowed bool
	}{
		{name: "same origin", host: "localhost:7788", origin: "http://localhost:7788", wantAllowed: true},
		{name: "different host", host: "localhost:7788", origin: "http://attacker.example", wantAllowed: false},
		{name: "different scheme", host: "webmux.example", origin: "http://webmux.example", forwarded: http.Header{"X-Forwarded-Proto": {"https"}}, wantAllowed: false},
		{name: "forwarded origin", host: "127.0.0.1:7788", origin: "https://webmux.example", forwarded: http.Header{"X-Forwarded-Proto": {"https"}, "X-Forwarded-Host": {"webmux.example"}}, wantAllowed: true},
		{name: "forwarded WebSocket origin", host: "webmux.example", origin: "https://webmux.example", forwarded: http.Header{"X-Forwarded-Proto": {"wss"}}, wantAllowed: true},
		{name: "first forwarded value", host: "127.0.0.1:7788", origin: "https://webmux.example", forwarded: http.Header{"X-Forwarded-Proto": {"https, http"}, "X-Forwarded-Host": {"webmux.example, proxy.internal"}}, wantAllowed: true},
		{name: "same origin fetch metadata through rewriting proxy", host: "proxy.internal:7777", origin: "https://webmux.example", forwarded: http.Header{"Sec-Fetch-Site": {"same-origin"}}, wantAllowed: true},
		{name: "cross site fetch metadata", host: "proxy.internal:7777", origin: "https://attacker.example", forwarded: http.Header{"Sec-Fetch-Site": {"cross-site"}}, wantAllowed: false},
		{name: "default port", host: "webmux.example:443", origin: "https://webmux.example", forwarded: http.Header{"X-Forwarded-Proto": {"https"}}, wantAllowed: true},
		{name: "missing origin", host: "localhost:7788", wantAllowed: true},
		{name: "null origin", host: "localhost:7788", origin: "null", wantAllowed: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+tt.host+"/api/panes/pane-1/terminal", nil)
			req.Host = tt.host
			req.Header.Set("Origin", tt.origin)
			for key, values := range tt.forwarded {
				req.Header[key] = values
			}
			if got := terminalOriginAllowed(req); got != tt.wantAllowed {
				t.Fatalf("terminalOriginAllowed() = %v, want %v", got, tt.wantAllowed)
			}
		})
	}
}

func TestTerminalWinsize(t *testing.T) {
	size, ok := terminalWinsize([]byte(`{"type":"resize","cols":123,"rows":47,"pixelWidth":984,"pixelHeight":752}`))
	if !ok {
		t.Fatal("terminalWinsize rejected valid resize")
	}
	if size.Cols != 123 || size.Rows != 47 || size.X != 984 || size.Y != 752 {
		t.Fatalf("terminalWinsize() = %+v", size)
	}
	for _, invalid := range []string{
		`{"type":"input","cols":123,"rows":47}`,
		`{"type":"resize","cols":1,"rows":47}`,
		`{"type":"resize","cols":123,"rows":47,"pixelWidth":984}`,
		`not json`,
	} {
		if _, ok := terminalWinsize([]byte(invalid)); ok {
			t.Fatalf("terminalWinsize accepted %q", invalid)
		}
	}
}

func TestTerminalAttachArgsAdvertiseSixel(t *testing.T) {
	got := terminalAttachArgs("/tmp/tmux.sock", "/tmp/tmux.conf", "mux-7701", true)
	want := []string{
		"-S", "/tmp/tmux.sock",
		"-f", "/tmp/tmux.conf",
		"-T", "sixel",
		"attach-session", "-t", "mux-7701",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("terminalAttachArgs() = %v, want %v", got, want)
	}
	withoutSixel := terminalAttachArgs("/tmp/tmux.sock", "", "mux-7701", false)
	if slices.Contains(withoutSixel, "sixel") || slices.Contains(withoutSixel, "-T") {
		t.Fatalf("terminalAttachArgs advertised unsupported SIXEL: %v", withoutSixel)
	}
}

func TestWriteTerminalInputHandlesShortWrites(t *testing.T) {
	writer := &shortWriter{max: 3}
	input := []byte("\x1b[<0;12;7M")
	if err := writeTerminalInput(writer, input); err != nil {
		t.Fatalf("writeTerminalInput() error = %v", err)
	}
	if !bytes.Equal(writer.Bytes(), input) {
		t.Fatalf("written input = %q, want %q", writer.Bytes(), input)
	}

	if err := writeTerminalInput(zeroWriter{}, input); err != io.ErrShortWrite {
		t.Fatalf("zero-byte write error = %v, want %v", err, io.ErrShortWrite)
	}
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

func TestPaneEnvironmentAdvertisesSixel(t *testing.T) {
	runtime := &TerminalRuntime{manager: &PaneManager{serverPort: "7788"}, sixelSupported: true}
	args := runtime.paneEnvArgs()
	if !slices.Contains(args, "WEBMUX_IMAGE_PROTOCOL=sixel") {
		t.Fatalf("pane environment missing SIXEL indicator: %v", args)
	}
	runtime.sixelSupported = false
	if args := runtime.paneEnvArgs(); slices.Contains(args, "WEBMUX_IMAGE_PROTOCOL=sixel") {
		t.Fatalf("pane environment advertised unsupported SIXEL: %v", args)
	}
}

func TestDestroyedTmuxSessionDetachesClient(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	root := t.TempDir()
	socket := filepath.Join(root, "tmux.sock")
	config := filepath.Join(root, "tmux.conf")
	contents, err := staticFiles.ReadFile("static/tmux.conf")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, contents, 0600); err != nil {
		t.Fatal(err)
	}
	runTmux := func(args ...string) error {
		return exec.Command("tmux", append([]string{"-S", socket, "-f", config}, args...)...).Run()
	}
	if err := runTmux("new-session", "-d", "-s", "pane-a", "sleep", "30"); err != nil {
		t.Fatal(err)
	}
	defer exec.Command("tmux", "-S", socket, "kill-server").Run()
	if err := runTmux("new-session", "-d", "-s", "pane-b", "sleep", "30"); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("tmux", "-S", socket, "-f", config, "attach-session", "-t", "pane-a")
	cmd.Env = terminalClientEnvironment(os.Environ())
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer ptmx.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	deadline := time.Now().Add(2 * time.Second)
	attached := false
	for time.Now().Before(deadline) {
		out, _ := exec.Command("tmux", "-S", socket, "list-clients", "-t", "pane-a", "-F", "#{session_name}").Output()
		if strings.TrimSpace(string(out)) == "pane-a" {
			attached = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !attached {
		_ = cmd.Process.Kill()
		t.Fatal("tmux client did not attach to pane-a")
	}
	if err := exec.Command("tmux", "-S", socket, "kill-session", "-t", "pane-a").Run(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("tmux client remained attached after its pane session was destroyed")
	}
}

func TestTmuxConfigForwardsPaneBells(t *testing.T) {
	contents, err := staticFiles.ReadFile("static/tmux.conf")
	if err != nil {
		t.Fatal(err)
	}
	config := string(contents)
	if !strings.Contains(config, "set -g bell-action current") {
		t.Fatal("tmux config must forward current pane bells for attention indicators")
	}
	if strings.Contains(config, "set -g bell-action none") {
		t.Fatal("tmux config still discards pane bells")
	}

	if _, err := exec.LookPath("tmux"); err != nil {
		return
	}
	socket := filepath.Join(t.TempDir(), "tmux.sock")
	if err := exec.Command("tmux", "-S", socket, "new-session", "-d", "sleep", "30").Run(); err != nil {
		t.Fatal(err)
	}
	defer exec.Command("tmux", "-S", socket, "kill-server").Run()
	if err := exec.Command("tmux", "-S", socket, "set-option", "-g", "bell-action", "none").Run(); err != nil {
		t.Fatal(err)
	}
	if err := configureTmuxAttention(socket); err != nil {
		t.Fatal(err)
	}
	value, err := exec.Command("tmux", "-S", socket, "show-option", "-gv", "bell-action").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(value)) != "current" {
		t.Fatalf("bell-action = %q, want current", strings.TrimSpace(string(value)))
	}
}

func TestOSC52ScannerHandlesFragmentedOutput(t *testing.T) {
	server := &Server{}
	scanner := newOSC52Scanner(server)
	payload := base64.StdEncoding.EncodeToString([]byte("fragmented clipboard"))
	sequence := []byte("prefix\x1b]52;c;" + payload + "\x1b\\suffix")

	for _, chunk := range [][]byte{
		sequence[:8],
		sequence[8:17],
		sequence[17 : len(sequence)-3],
		sequence[len(sequence)-3:],
	} {
		scanner.ObserveBackendToClient(chunk)
	}

	server.clipboardMu.RLock()
	clipboard := server.clipboard
	version := server.clipboardVersion
	server.clipboardMu.RUnlock()
	if clipboard != "fragmented clipboard" {
		t.Fatalf("clipboard = %q, want %q", clipboard, "fragmented clipboard")
	}
	if version != 1 {
		t.Fatalf("clipboard version = %d, want 1", version)
	}
}

func TestTerminalPopoutPage(t *testing.T) {
	pane := &Pane{ID: "pane-7701", Name: "one < two", Type: "terminal"}
	server := &Server{
		manager: &PaneManager{panes: map[string]*Pane{pane.ID: pane}},
		uiState: &UIState{Groups: []UIGroup{{ID: "group-1", PaneIDs: []string{pane.ID}}}, GroupOrder: []string{"group-1"}},
	}
	req := httptest.NewRequest("GET", "/p/pane-7701/", nil)
	recorder := httptest.NewRecorder()

	server.serveTerminalPopout(recorder, req, pane)

	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"Terminal one &lt; two",
		"../../vendor/xterm/xterm.js",
		"../../vendor/xterm/addon-webgl.js",
		"../../vendor/xterm/addon-image.js",
		"../../terminal-popout.js",
		"id=\"keybar\"",
		"webmux-popouts",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("popout page missing %q", expected)
		}
	}
}

func TestTerminalPopoutPageUsesCanonicalPositionForUnnamedPane(t *testing.T) {
	first := &Pane{ID: "pane-1", Name: "named", Type: "terminal"}
	second := &Pane{ID: "pane-2", Type: "terminal"}
	server := &Server{
		manager: &PaneManager{panes: map[string]*Pane{first.ID: first, second.ID: second}},
		uiState: &UIState{
			Groups:     []UIGroup{{ID: "group-1", PaneIDs: []string{second.ID, first.ID}, CellMapping: []int{1, 0}}},
			GroupOrder: []string{"group-1"},
		},
	}
	recorder := httptest.NewRecorder()
	server.serveTerminalPopout(recorder, httptest.NewRequest(http.MethodGet, "/p/pane-2/", nil), second)
	if !strings.Contains(recorder.Body.String(), "<title>Terminal 2</title>") {
		t.Fatalf("popout title did not use canonical position: %s", recorder.Body.String())
	}
}
