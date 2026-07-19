package main

import (
	"encoding/base64"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

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
		"../../terminal-popout.js",
		"webmux-popouts",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("popout page missing %q", expected)
		}
	}
}
