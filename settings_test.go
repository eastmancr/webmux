package main

import "testing"

func TestKeybarAnchorDefaultsAndValidation(t *testing.T) {
	settings := &Settings{}
	mergeWithDefaults(settings)
	if settings.Keybar.Anchor != "bottom" {
		t.Fatalf("default keybar anchor = %q, want bottom", settings.Keybar.Anchor)
	}

	settings.Keybar.Anchor = "pane"
	mergeWithDefaults(settings)
	if settings.Keybar.Anchor != "pane" {
		t.Fatalf("valid keybar anchor = %q, want pane", settings.Keybar.Anchor)
	}

	settings.Keybar.Anchor = "invalid"
	mergeWithDefaults(settings)
	if settings.Keybar.Anchor != "bottom" {
		t.Fatalf("invalid keybar anchor = %q, want bottom", settings.Keybar.Anchor)
	}
}
