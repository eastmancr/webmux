package main

import (
	"os"
	"path/filepath"
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
