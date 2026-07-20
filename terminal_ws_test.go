package main

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
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

func TestTerminalAttachArgsAdvertiseSixel(t *testing.T) {
	got := terminalAttachArgs("/tmp/tmux.sock", "/tmp/tmux.conf", "mux-7701")
	want := []string{
		"-S", "/tmp/tmux.sock",
		"-f", "/tmp/tmux.conf",
		"-T", "sixel",
		"attach-session", "-t", "mux-7701",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("terminalAttachArgs() = %v, want %v", got, want)
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
	runtime := &TerminalRuntime{manager: &PaneManager{serverPort: "7788"}}
	args := runtime.paneEnvArgs()
	if !slices.Contains(args, "WEBMUX_IMAGE_PROTOCOL=sixel") {
		t.Fatalf("pane environment missing SIXEL indicator: %v", args)
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
	server := &Server{}
	pane := &Pane{ID: "pane-7701", Name: "one < two", Type: "terminal"}
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
		"webmux-popouts",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("popout page missing %q", expected)
		}
	}
}
