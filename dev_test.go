//go:build dev

package main

import (
	"path/filepath"
	"testing"
)

func TestDevStaticDirEnvironmentOverride(t *testing.T) {
	previous := devMode.staticDir
	devMode.staticDir = ""
	t.Cleanup(func() { devMode.staticDir = previous })

	dir := filepath.Join(t.TempDir(), "assets")
	t.Setenv("WEBMUX_STATIC_DIR", dir)
	if got := devStaticDir(); got != dir {
		t.Fatalf("devStaticDir() = %q, want %q", got, dir)
	}
}
