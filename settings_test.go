package main

import (
	"encoding/json"
	"testing"
)

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

func TestPaneAttentionDefaults(t *testing.T) {
	settings := DefaultSettings()
	if !settings.Panes.AttentionIndicators || !settings.Panes.ShowAttentionInTitle {
		t.Fatal("pane attention indicators and title indicator should default on")
	}
	if !settings.Panes.Terminal.IndicateAttention || !settings.Panes.OpenCode.IndicateAttention {
		t.Fatal("terminal and OpenCode attention indicators should default on")
	}
}

func TestPaneAttentionExplicitFalseSurvivesSettingsMerge(t *testing.T) {
	settings := *DefaultSettings()
	data := []byte(`{"panes":{"attentionIndicators":false,"showAttentionInTitle":false,"terminal":{"indicateAttention":false},"opencode":{"indicateAttention":false}}}`)
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	mergeWithDefaults(&settings)

	if settings.Panes.AttentionIndicators || settings.Panes.ShowAttentionInTitle {
		t.Fatal("explicitly disabled global attention settings were reset")
	}
	if settings.Panes.Terminal.IndicateAttention || settings.Panes.OpenCode.IndicateAttention {
		t.Fatal("explicitly disabled pane type attention settings were reset")
	}
}

func TestPaneAttentionDefaultsSurviveLegacySettingsJSON(t *testing.T) {
	settings := *DefaultSettings()
	if err := json.Unmarshal([]byte(`{"ui":{"accent":"#ffffff"}}`), &settings); err != nil {
		t.Fatal(err)
	}

	if !settings.Panes.AttentionIndicators || !settings.Panes.ShowAttentionInTitle {
		t.Fatal("legacy settings should receive enabled pane attention defaults")
	}
	if !settings.Panes.Terminal.IndicateAttention || !settings.Panes.OpenCode.IndicateAttention {
		t.Fatal("legacy settings should receive enabled pane type attention defaults")
	}
}
