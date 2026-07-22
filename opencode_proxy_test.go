package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAnalyzeOpenCodeIndexCompatibility(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantError   bool
		wantWarning bool
	}{
		{
			name:        "typical app shell",
			content:     `<!doctype html><html><head><script type="module" src="/assets/index.js"></script><link rel="stylesheet" href="/assets/index.css"></head><body><div id="root"></div></body></html>`,
			wantError:   false,
			wantWarning: false,
		},
		{
			name:        "renderable but unusual app shell",
			content:     `<html><body><script type="module">window.__OPENCODE__ = true</script><div id="root"></div></body></html>`,
			wantError:   false,
			wantWarning: true,
		},
		{
			name:        "empty response",
			content:     ``,
			wantError:   true,
			wantWarning: false,
		},
		{
			name:        "json response",
			content:     `{"ok":true}`,
			wantError:   true,
			wantWarning: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := analyzeOpenCodeIndexCompatibility(tt.content)
			if (got.Error != "") != tt.wantError {
				t.Fatalf("error = %q, wantError %v", got.Error, tt.wantError)
			}
			if (got.Warning != "") != tt.wantWarning {
				t.Fatalf("warning = %q, wantWarning %v", got.Warning, tt.wantWarning)
			}
		})
	}
}

func TestAnalyzeOpenCodeStorageSchema(t *testing.T) {
	tests := []struct {
		name        string
		items       map[string]string
		wantWarning bool
		wantText    string
		wantDetail  string
	}{
		{
			name: "known storage keys",
			items: map[string]string{
				"opencode.global.dat:server":               `{"projects":{"webmux":[]},"lastProject":{"webmux":"/tmp/project"}}`,
				"opencode.global.dat:layout":               `{"sessionTabs":{},"sessionView":{},"handoff":{}}`,
				"opencode.global.dat:layout.page":          `{"lastProjectSession":{},"workspaceOrder":{}}`,
				"opencode.global.dat:model":                `anthropic/claude-sonnet-4`,
				"opencode.global.dat:notification":         `{}`,
				"opencode.global.dat:prompt-history":       `[]`,
				"opencode.global.dat:prompt-history-shell": `[]`,
				"opencode.global.dat:route.context":        `{"server":"webmux"}`,
				"opencode.global.dat:go-upsell":            `{}`,
				"opencode.global.dat:home.servers":         `{"collapsed":{}}`,
				"opencode.global.dat:language":             `{"locale":"en"}`,
				"opencode.global.dat:open.app":             `{"app":"finder"}`,
				"opencode.global.dat:review-panel-v2":      `{}`,
			},
			wantWarning: false,
		},
		{
			name: "one unknown global storage key",
			items: map[string]string{
				"opencode.global.dat:server":         `{"projects":{},"lastProject":{}}`,
				"opencode.global.dat:future.context": `{"server":"local"}`,
			},
			wantWarning: true,
			wantText:    "1 unrecognized global key",
			wantDetail:  "opencode.global.dat:future.context",
		},
		{
			name: "unknown global storage keys",
			items: map[string]string{
				"opencode.global.dat:server":          `{"projects":{},"lastProject":{}}`,
				"opencode.global.dat:future.context":  `{"server":"local"}`,
				"opencode.global.dat:workspace.index": `[]`,
			},
			wantWarning: true,
			wantText:    "2 unrecognized global keys",
			wantDetail:  "opencode.global.dat:future.context",
		},
		{
			name: "malformed known storage key",
			items: map[string]string{
				"opencode.global.dat:layout": `{`,
			},
			wantWarning: true,
			wantText:    "1 malformed known key",
			wantDetail:  "opencode.global.dat:layout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := analyzeOpenCodeStorageSchema(tt.items)
			if got.Error != "" {
				t.Fatalf("unexpected error: %q", got.Error)
			}
			if (got.Warning != "") != tt.wantWarning {
				t.Fatalf("warning = %q, wantWarning %v", got.Warning, tt.wantWarning)
			}
			if tt.wantText != "" && !strings.Contains(got.Warning, tt.wantText) {
				t.Fatalf("warning = %q, want text %q", got.Warning, tt.wantText)
			}
			if tt.wantDetail != "" && !strings.Contains(strings.Join(got.Details, ","), tt.wantDetail) {
				t.Fatalf("details = %q, want detail %q", got.Details, tt.wantDetail)
			}
			if strings.Contains(got.Warning, "key(s)") {
				t.Fatalf("warning uses key(s): %q", got.Warning)
			}
		})
	}
}

func TestModifyOpenCodeIndexResponseInjectsAndWarns(t *testing.T) {
	runtime := &OpenCodeRuntime{states: make(map[string]*OpenCodePaneState)}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Body:       io.NopCloser(strings.NewReader(`<html><body><script type="module">window.__OPENCODE__ = true</script><div id="root"></div></body></html>`)),
	}
	storage := PaneStorageState{Items: map[string]string{
		"opencode.global.dat:server":         `{"projects":{},"lastProject":{}}`,
		"opencode.global.dat:future.context": `{"server":"local"}`,
	}}

	if err := runtime.modifyOpenCodeIndexResponse(nil, "pane-1", "opencode", storage, DiagnosticsSettings{}, resp); err != nil {
		t.Fatalf("modifyOpenCodeIndexResponse returned error: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read modified body: %v", err)
	}
	content := string(body)
	for _, want := range []string{"pane-1", "__webmuxOpenCodePathname", "opencode.global.dat:server", "webmux-clipboard-write"} {
		if !strings.Contains(content, want) {
			t.Fatalf("modified content missing %q", want)
		}
	}
	bridgeIndex := strings.Index(content, "var trackedSockets = []")
	storageIndex := strings.Index(content, "var OriginalWebSocketForStorage")
	if bridgeIndex == -1 || storageIndex == -1 || bridgeIndex > storageIndex {
		t.Fatal("popout bridge must wrap WebSocket before storage captures it")
	}
	if !strings.Contains(content, "window.location.origin);") {
		t.Fatal("clipboard postMessage must target the Webmux origin")
	}
	warning := runtime.WarningReason()
	if warning == "" {
		t.Fatal("expected warning to be recorded")
	}
	if !strings.Contains(warning, "unrecognized global key") || !strings.Contains(warning, "no head element") {
		t.Fatalf("warning = %q, want storage and html warnings", warning)
	}
}

func TestModifyOpenCodeIndexResponseCanonicalizesDefaultServerOrigin(t *testing.T) {
	runtime := &OpenCodeRuntime{states: make(map[string]*OpenCodePaneState)}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Body:       io.NopCloser(strings.NewReader(`<!doctype html><html><head><script type="module" src="/assets/index.js"></script></head><body></body></html>`)),
	}
	storage := PaneStorageState{Items: map[string]string{
		"opencode.settings.dat:defaultServerUrl": "https://old.example.com",
	}}

	if err := runtime.modifyOpenCodeIndexResponse(nil, "pane-1", "opencode", storage, DiagnosticsSettings{}, resp); err != nil {
		t.Fatalf("modifyOpenCodeIndexResponse returned error: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read modified body: %v", err)
	}
	content := string(body)
	for _, want := range []string{
		"opencode.settings.dat:defaultServerUrl",
		"key === opencodeDefaultServerStorageKey) return window.location.origin",
		"key === opencodeDefaultServerStorageKey && typeof value === 'string'",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("modified content missing default-server migration %q", want)
		}
	}
}

func TestModifyOpenCodeIndexResponseCanonicalizesHomeServerSelection(t *testing.T) {
	runtime := &OpenCodeRuntime{states: make(map[string]*OpenCodePaneState)}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Body:       io.NopCloser(strings.NewReader(`<!doctype html><html><head><script type="module" src="/assets/index.js"></script></head><body></body></html>`)),
	}
	storage := PaneStorageState{Items: map[string]string{
		"opencode.global.dat:layout": `{"home":{"selection":{"server":"https://old.example.com"}}}`,
	}}

	if err := runtime.modifyOpenCodeIndexResponse(nil, "pane-1", "opencode", storage, DiagnosticsSettings{}, resp); err != nil {
		t.Fatalf("modifyOpenCodeIndexResponse returned error: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read modified body: %v", err)
	}
	content := string(body)
	for _, want := range []string{
		"parsed.home.selection.server = canonicalizeOpenCodeServerReference",
		"parsed.home.selection.server = materializeOpenCodeServerReference",
		"window.location.origin",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("modified content missing home-server migration %q", want)
		}
	}
}

func TestModifyOpenCodeIndexResponseMaterializesWindowTabs(t *testing.T) {
	runtime := &OpenCodeRuntime{states: make(map[string]*OpenCodePaneState)}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Body:       io.NopCloser(strings.NewReader(`<!doctype html><html><head><script type="module" src="/assets/index.js"></script></head><body></body></html>`)),
	}
	storage := PaneStorageState{Items: map[string]string{
		openCodeWindowTabsKey: `[{"type":"session","server":"webmux","sessionId":"ses_1"}]`,
	}}

	if err := runtime.modifyOpenCodeIndexResponse(nil, "pane-1", "opencode", storage, DiagnosticsSettings{}, resp); err != nil {
		t.Fatalf("modifyOpenCodeIndexResponse returned error: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read modified body: %v", err)
	}
	content := string(body)
	for _, want := range []string{
		"opencode.window.browser.dat:tabs",
		"translateOpenCodeTabsStorageValue(value, window.location.origin)",
		"translateOpenCodeTabInfoStorageValue(value, canonicalServerID)",
		"encodeOpenCodeServerID(serverID)",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("modified content missing window-tab translation %q", want)
		}
	}
}

func TestModifyOpenCodeIndexResponseUsesRegularStorageFlushes(t *testing.T) {
	runtime := &OpenCodeRuntime{states: make(map[string]*OpenCodePaneState)}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Body:       io.NopCloser(strings.NewReader(`<!doctype html><html><head><script type="module" src="/assets/index.js"></script></head><body></body></html>`)),
	}

	if err := runtime.modifyOpenCodeIndexResponse(nil, "pane-1", "opencode", PaneStorageState{}, DiagnosticsSettings{}, resp); err != nil {
		t.Fatalf("modifyOpenCodeIndexResponse returned error: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read modified body: %v", err)
	}
	content := string(body)
	for _, want := range []string{
		"keepalive: keepalive === true",
		"storageSyncKeepaliveLimit = 60 * 1024",
		"window.__webmuxFlushOpenCodeStorage = flushAllStorageUpdates",
		"flushStorageUpdates(pendingStorageUpdateSize() <= storageSyncKeepaliveLimit)",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("modified content missing storage flush behavior %q", want)
		}
	}
}

func TestModifyOpenCodeIndexResponseTracksActivePath(t *testing.T) {
	runtime := &OpenCodeRuntime{states: make(map[string]*OpenCodePaneState)}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Body:       io.NopCloser(strings.NewReader(`<!doctype html><html><head><script type="module" src="/assets/index.js"></script></head><body></body></html>`)),
	}
	storage := PaneStorageState{Items: map[string]string{
		"webmux.internal.opencode.activePath": "/server/d2VibXV4/session/ses_1",
	}}

	if err := runtime.modifyOpenCodeIndexResponse(nil, "pane-1", "opencode", storage, DiagnosticsSettings{}, resp); err != nil {
		t.Fatalf("modifyOpenCodeIndexResponse returned error: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read modified body: %v", err)
	}
	content := string(body)
	for _, want := range []string{
		"webmux.internal.opencode.activePath",
		"delete serverStorage[activePathStorageKey]",
		"requestedOpenCodePath = translateOpenCodeRoute(activeOpenCodePath, window.location.origin)",
		"persistActiveOpenCodePath(arguments[2])",
		"window.addEventListener('popstate'",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("modified content missing active-path behavior %q", want)
		}
	}
}

func TestModifyOpenCodeIndexResponseMaterializesRouteContext(t *testing.T) {
	runtime := &OpenCodeRuntime{states: make(map[string]*OpenCodePaneState)}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Body:       io.NopCloser(strings.NewReader(`<!doctype html><html><head><script type="module" src="/assets/index.js"></script></head><body></body></html>`)),
	}

	if err := runtime.modifyOpenCodeIndexResponse(nil, "pane-1", "opencode", PaneStorageState{}, DiagnosticsSettings{}, resp); err != nil {
		t.Fatalf("modifyOpenCodeIndexResponse returned error: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read modified body: %v", err)
	}
	content := string(body)
	for _, want := range []string{
		"translateOpenCodeRouteContextStorageValue(value, window.location.origin)",
		"translateOpenCodeRouteContextStorageValue(value, canonicalServerID)",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("modified content missing route-context behavior %q", want)
		}
	}
}

func TestRewriteOpenCodeJSInitialLocation(t *testing.T) {
	input := `const t=()=>{const r=window.location.pathname.replace(/^\/+/,"/")+window.location.search;return r}`
	output := rewriteOpenCodeJSInitialLocation(input)
	if output == input {
		t.Fatal("expected JS initial location rewrite")
	}
	if !strings.Contains(output, "__webmuxOpenCodePathname") {
		t.Fatalf("rewritten JS missing Webmux pathname override: %s", output)
	}
	if strings.Contains(output, "const r=window.location.pathname") {
		t.Fatalf("rewritten JS still reads raw initial pathname: %s", output)
	}
}

func TestModifyOpenCodeAssetResponseRewritesInitialLocation(t *testing.T) {
	runtime := &OpenCodeRuntime{states: make(map[string]*OpenCodePaneState)}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/javascript"}},
		Body:       io.NopCloser(strings.NewReader(`const t=()=>window.location.pathname.replace(/^\/+/,"/")+window.location.search`)),
	}

	if err := runtime.modifyOpenCodeAssetResponse("pane-1", resp); err != nil {
		t.Fatalf("modifyOpenCodeAssetResponse returned error: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read modified body: %v", err)
	}
	content := string(body)
	if !strings.Contains(content, "__webmuxOpenCodePathname") {
		t.Fatalf("modified JS missing pathname override: %s", content)
	}
}

func TestModifyOpenCodeIndexResponseRejectsIncompatibleHTML(t *testing.T) {
	runtime := &OpenCodeRuntime{states: make(map[string]*OpenCodePaneState)}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
	}

	err := runtime.modifyOpenCodeIndexResponse(nil, "pane-1", "opencode", PaneStorageState{}, DiagnosticsSettings{}, resp)
	if err == nil {
		t.Fatal("expected incompatible HTML error")
	}
	if !strings.Contains(err.Error(), "not an HTML document") {
		t.Fatalf("error = %q, want incompatible HTML message", err.Error())
	}
}

func TestModifyOpenCodeIndexResponseClearsStaleWarning(t *testing.T) {
	runtime := &OpenCodeRuntime{states: make(map[string]*OpenCodePaneState)}
	runtime.setWarningReason("old warning")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Body:       io.NopCloser(strings.NewReader(`<!doctype html><html><head><script type="module" src="/assets/index.js"></script></head><body><div id="root"></div></body></html>`)),
	}

	if err := runtime.modifyOpenCodeIndexResponse(nil, "pane-1", "opencode", PaneStorageState{}, DiagnosticsSettings{}, resp); err != nil {
		t.Fatalf("modifyOpenCodeIndexResponse returned error: %v", err)
	}
	if warning := runtime.WarningReason(); warning != "" {
		t.Fatalf("WarningReason = %q, want cleared warning", warning)
	}
}

func TestUpdateWarningReasonReportsOnlyChanges(t *testing.T) {
	runtime := &OpenCodeRuntime{states: make(map[string]*OpenCodePaneState)}
	if !runtime.updateWarningReason("schema warning") {
		t.Fatal("first warning update should report a change")
	}
	if runtime.updateWarningReason("schema warning") {
		t.Fatal("same warning update should not report a change")
	}
	if !runtime.updateWarningReason("") {
		t.Fatal("clearing warning should report a change")
	}
}

func TestUpdateWarningReportsDetailChanges(t *testing.T) {
	runtime := &OpenCodeRuntime{states: make(map[string]*OpenCodePaneState)}
	if !runtime.updateWarning("schema warning", "key=a") {
		t.Fatal("first detailed warning update should report a change")
	}
	if runtime.updateWarning("schema warning", "key=a") {
		t.Fatal("same detailed warning update should not report a change")
	}
	if !runtime.updateWarning("schema warning", "key=b") {
		t.Fatal("same warning with changed detail should report a change")
	}
}
