package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMountPathHandler(t *testing.T) {
	tests := []struct {
		name         string
		target       string
		wantPath     string
		wantLocation string
	}{
		{name: "root", target: "/", wantPath: "/"},
		{name: "explicit index", target: "/index.html", wantPath: "/index.html"},
		{name: "root static asset", target: "/style.css", wantPath: "/style.css"},
		{name: "mounted static asset", target: "/webmux/style.css", wantPath: "/style.css"},
		{name: "mounted favicon", target: "/webmux/favicon.ico?v=1", wantPath: "/favicon.ico"},
		{name: "mounted explicit index", target: "/webmux/index.html", wantPath: "/index.html"},
		{name: "extensionless static asset", target: "/wm", wantPath: "/wm"},
		{name: "extensionless mount", target: "/webmux", wantLocation: "/webmux/"},
		{name: "dotted mount", target: "/machine.http", wantLocation: "/machine.http/"},
		{name: "arbitrary dotted mount", target: "/machine.example", wantLocation: "/machine.example/"},
		{name: "nested dotted mount", target: "/gateway/machine.example?view=1", wantLocation: "/gateway/machine.example/?view=1"},
		{name: "canonical mount", target: "/webmux/", wantPath: "/"},
		{name: "prefixed api", target: "/webmux/api/info", wantPath: "/api/info"},
		{name: "prefixed pane", target: "/webmux/p/pane-1/", wantPath: "/p/pane-1/"},
		{name: "prefixed vendor asset", target: "/webmux/vendor/xterm/xterm.js", wantPath: "/vendor/xterm/xterm.js"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.WriteHeader(http.StatusNoContent)
			})
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, tt.target, nil)

			mountPathHandler(next).ServeHTTP(recorder, request)

			if tt.wantLocation != "" {
				if recorder.Code != http.StatusMovedPermanently {
					t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMovedPermanently)
				}
				if got := recorder.Header().Get("Location"); got != tt.wantLocation {
					t.Fatalf("Location = %q, want %q", got, tt.wantLocation)
				}
				if gotPath != "" {
					t.Fatalf("next handler called with path %q during redirect", gotPath)
				}
				return
			}

			if recorder.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
			}
			if gotPath != tt.wantPath {
				t.Fatalf("path = %q, want %q", gotPath, tt.wantPath)
			}
		})
	}
}
