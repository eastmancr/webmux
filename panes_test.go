package main

import "testing"

func TestPaneTypesIncludesOpenCodeWarning(t *testing.T) {
	manager := NewPaneManager(10000, "/bin/sh", "", "8080")
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
