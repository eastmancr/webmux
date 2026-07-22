package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestCanonicalizeOpenCodeStorageItems(t *testing.T) {
	items := map[string]string{
		"opencode.settings.dat:defaultServerUrl":                        "https://vpn.example.com",
		"opencode.global.dat:https://vpn.example.com\x00model":          "alias-model",
		"opencode.global.dat:model":                                     "canonical-model",
		"opencode.global.dat:https://vpn.example.com\x00prompt-history": `[]`,
		"opencode.global.dat:route.context":                             `{"server":"https://vpn.example.com"}`,
		"opencode.global.dat:layout":                                    `{"sessionTabs":{"https://vpn.example.com\u0000project/session":{"active":"context"}},"sessionView":{},"handoff":{},"home":{"selection":{"server":"https://vpn.example.com"}}}`,
		"opencode.global.dat:server":                                    `{"list":[{"type":"http","http":{"url":"https://vpn.example.com"}}],"projects":{"webmux":[{"worktree":"/canonical","expanded":true}],"local":[{"worktree":"/local","expanded":true}],"https://vpn.example.com":[{"worktree":"/vpn","expanded":true},{"worktree":"/canonical","expanded":false}]},"lastProject":{"https://vpn.example.com":"/vpn","webmux":"/canonical"},"recentlyClosed":{"local":["/local"],"https://vpn.example.com":["/vpn","/local"]}}`,
		openCodeActivePathStorageKey:                                    `/server/aHR0cHM6Ly92cG4uZXhhbXBsZS5jb20/session/ses_1`,
	}

	got := canonicalizeOpenCodeStorageItems(items)
	if got[openCodeDefaultServerKey] != openCodeCanonicalServerID {
		t.Fatalf("default server = %q, want %q", got[openCodeDefaultServerKey], openCodeCanonicalServerID)
	}
	if got["opencode.global.dat:model"] != "canonical-model" {
		t.Fatalf("canonical model did not win alias collision: %q", got["opencode.global.dat:model"])
	}
	if _, ok := got["opencode.global.dat:https://vpn.example.com\x00model"]; ok {
		t.Fatal("origin-scoped model key was retained")
	}
	if _, ok := got["opencode.global.dat:prompt-history"]; !ok {
		t.Fatal("origin-scoped prompt history was not canonicalized")
	}

	server := decodeStorageObject(t, got[openCodeServerStorageKey])
	if list, ok := server["list"].([]any); !ok || len(list) != 0 {
		t.Fatalf("server list = %#v, want empty managed-backend list", server["list"])
	}
	projects := objectMap(server["projects"])
	projectList, ok := projects[openCodeCanonicalServerID].([]any)
	if !ok || len(projectList) != 3 {
		t.Fatalf("canonical projects = %#v, want three merged projects", projects[openCodeCanonicalServerID])
	}
	if got := objectMap(server["lastProject"])[openCodeCanonicalServerID]; got != "/canonical" {
		t.Fatalf("last project = %#v, want canonical value", got)
	}
	closed, ok := objectMap(server["recentlyClosed"])[openCodeCanonicalServerID].([]any)
	if !ok || len(closed) != 2 || closed[0] != "/local" || closed[1] != "/vpn" {
		t.Fatalf("recently closed = %#v, want merged unique values", closed)
	}

	layout := decodeStorageObject(t, got[openCodeLayoutStorageKey])
	selection := objectMap(objectMap(layout["home"])["selection"])
	if selection["server"] != openCodeCanonicalServerID {
		t.Fatalf("home server = %#v, want canonical identity", selection["server"])
	}
	tabs := objectMap(layout["sessionTabs"])
	if _, ok := tabs["webmux\x00project/session"]; !ok {
		t.Fatalf("session tabs were not canonicalized: %#v", tabs)
	}
	route := decodeStorageObject(t, got[openCodeRouteContextStorageKey])
	if route["server"] != openCodeCanonicalServerID {
		t.Fatalf("route server = %#v, want canonical identity", route["server"])
	}
	if got[openCodeActivePathStorageKey] != "/server/d2VibXV4/session/ses_1" {
		t.Fatalf("active path = %q, want canonical server route", got[openCodeActivePathStorageKey])
	}
}

func TestCanonicalizePaneStorageLeavesOtherNamespacesUntouched(t *testing.T) {
	items := map[string]string{"opencode.settings.dat:defaultServerUrl": "https://example.com"}
	got := canonicalizePaneStorageItems("other", items)
	if got[openCodeDefaultServerKey] != "https://example.com" {
		t.Fatalf("non-OpenCode value was changed: %q", got[openCodeDefaultServerKey])
	}
	got["new"] = "value"
	if _, ok := items["new"]; ok {
		t.Fatal("non-OpenCode canonicalization returned the input map")
	}
}

func TestCanonicalizeOpenCodeWindowTabs(t *testing.T) {
	items := map[string]string{
		openCodeWindowTabsKey:       `[{"type":"session","server":"https://vpn.example.com","sessionId":"ses_1"},{"type":"draft","server":"https://vpn.example.com","draftID":"draft_1"}]`,
		openCodeWindowTabsClosedKey: `[{"tab":{"type":"session","server":"https://vpn.example.com","sessionId":"ses_2"},"index":1}]`,
		openCodeWindowTabsInfoKey:   `{"https://vpn.example.com\n/server/aHR0cHM6Ly92cG4uZXhhbXBsZS5jb20/session/ses_1":{"title":"Alias"},"webmux\n/server/d2VibXV4/session/ses_1":{"title":"Canonical"},"draft:draft_1":{"title":"Draft"}}`,
		openCodeWindowTabsRecentKey: `{"key":"https://vpn.example.com\n/server/aHR0cHM6Ly92cG4uZXhhbXBsZS5jb20/session/ses_1"}`,
		openCodeHomeServersKey:      `{"collapsed":{"https://vpn.example.com":true}}`,
	}

	got := canonicalizeOpenCodeStorageItems(items)
	for key, value := range got {
		if strings.Contains(value, "vpn.example.com") {
			t.Fatalf("canonical %s retained browser origin: %s", key, value)
		}
	}

	var tabs []map[string]any
	if err := json.Unmarshal([]byte(got[openCodeWindowTabsKey]), &tabs); err != nil {
		t.Fatalf("failed to decode tabs: %v", err)
	}
	if len(tabs) != 2 || tabs[0]["server"] != openCodeCanonicalServerID || tabs[1]["server"] != openCodeCanonicalServerID {
		t.Fatalf("canonical tabs = %#v", tabs)
	}
	info := decodeStorageObject(t, got[openCodeWindowTabsInfoKey])
	canonicalTabID := "webmux\n/server/d2VibXV4/session/ses_1"
	if title := objectMap(info[canonicalTabID])["title"]; title != "Canonical" {
		t.Fatalf("canonical tab info did not win alias collision: %#v", title)
	}
	homeServers := decodeStorageObject(t, got[openCodeHomeServersKey])
	if collapsed := objectMap(homeServers["collapsed"]); collapsed[openCodeCanonicalServerID] != true || len(collapsed) != 1 {
		t.Fatalf("canonical collapsed servers = %#v", collapsed)
	}

	materialized := map[string]string{
		openCodeWindowTabsKey:       translateOpenCodeTabsStorageValue(got[openCodeWindowTabsKey], "https://lan.example.com"),
		openCodeWindowTabsClosedKey: translateOpenCodeClosedTabsStorageValue(got[openCodeWindowTabsClosedKey], "https://lan.example.com"),
		openCodeWindowTabsInfoKey:   translateOpenCodeTabInfoStorageValue(got[openCodeWindowTabsInfoKey], "https://lan.example.com"),
		openCodeWindowTabsRecentKey: translateOpenCodeRecentTabStorageValue(got[openCodeWindowTabsRecentKey], "https://lan.example.com"),
		openCodeHomeServersKey:      translateOpenCodeHomeServersStorageValue(got[openCodeHomeServersKey], "https://lan.example.com"),
	}
	for key, value := range materialized {
		containsLANIdentity := strings.Contains(value, "https://lan.example.com") || strings.Contains(value, "aHR0cHM6Ly9sYW4uZXhhbXBsZS5jb20")
		if strings.Contains(value, openCodeCanonicalServerID) || !containsLANIdentity {
			t.Fatalf("materialized %s = %s", key, value)
		}
	}
	if !strings.Contains(materialized[openCodeWindowTabsInfoKey], `draft:draft_1`) {
		t.Fatalf("draft tab ID was changed: %s", materialized[openCodeWindowTabsInfoKey])
	}
	if !strings.Contains(materialized[openCodeWindowTabsInfoKey], `/server/aHR0cHM6Ly9sYW4uZXhhbXBsZS5jb20/session/`) {
		t.Fatalf("tab route server was not materialized: %s", materialized[openCodeWindowTabsInfoKey])
	}
}

func TestLoadPaneStorageMigratesOpenCodeOrigins(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	raw := PaneStorageState{
		Items: map[string]string{
			openCodeDefaultServerKey: "https://vpn.example.com",
			openCodeLayoutStorageKey: `{"home":{"selection":{"server":"https://vpn.example.com"}}}`,
		},
		Version: 7,
	}
	if err := SavePaneStorage(openCodeStorageNamespace, raw); err != nil {
		t.Fatalf("SavePaneStorage failed: %v", err)
	}

	loaded := LoadPaneStorage()[openCodeStorageNamespace]
	if loaded == nil {
		t.Fatal("OpenCode storage was not loaded")
	}
	if loaded.Version != 8 || loaded.UpdatedBy != "migration" {
		t.Fatalf("migrated metadata = version %d updatedBy %q", loaded.Version, loaded.UpdatedBy)
	}
	if loaded.Items[openCodeDefaultServerKey] != openCodeCanonicalServerID {
		t.Fatalf("loaded default server = %q", loaded.Items[openCodeDefaultServerKey])
	}
	if strings.Contains(loaded.Items[openCodeLayoutStorageKey], "vpn.example.com") {
		t.Fatalf("loaded layout retained old origin: %s", loaded.Items[openCodeLayoutStorageKey])
	}

	path, err := paneStorageFilePath(openCodeStorageNamespace)
	if err != nil {
		t.Fatalf("paneStorageFilePath failed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read migrated storage: %v", err)
	}
	var persisted PaneStorageState
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("failed to parse migrated storage: %v", err)
	}
	if persisted.Items[openCodeDefaultServerKey] != openCodeCanonicalServerID {
		t.Fatalf("persisted default server = %q", persisted.Items[openCodeDefaultServerKey])
	}
}

func TestPaneStorageWritesCanonicalizeOpenCodeOrigins(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	server := &Server{
		paneStorage:     map[string]*PaneStorageState{},
		paneStorageSubs: map[string]map[chan paneStorageEvent]struct{}{},
	}

	snapshot := server.applyPaneStorageRequest(openCodeStorageNamespace, paneStorageRequest{
		Operation: "set",
		Key:       "opencode.global.dat:https://vpn.example.com\x00route.context",
		Value:     `{"server":"https://vpn.example.com"}`,
		ClientID:  "vpn-client",
	})
	if _, ok := snapshot.Items[openCodeRouteContextStorageKey]; !ok {
		t.Fatalf("pane write retained origin-scoped key: %#v", snapshot.Items)
	}
	if strings.Contains(snapshot.Items[openCodeRouteContextStorageKey], "vpn.example.com") {
		t.Fatalf("pane write retained origin in value: %s", snapshot.Items[openCodeRouteContextStorageKey])
	}

	snapshot = server.replacePaneStorage(openCodeStorageNamespace, map[string]string{
		openCodeDefaultServerKey: "https://lan.example.com",
	}, "admin")
	if snapshot.Items[openCodeDefaultServerKey] != openCodeCanonicalServerID {
		t.Fatalf("admin replacement retained origin: %q", snapshot.Items[openCodeDefaultServerKey])
	}
}

func TestCanonicalizeOpenCodeStoragePreservesMalformedValues(t *testing.T) {
	items := map[string]string{
		openCodeServerStorageKey: `{`,
		openCodeLayoutStorageKey: `{`,
	}
	got := canonicalizeOpenCodeStorageItems(items)
	for key, value := range items {
		if got[key] != value {
			t.Fatalf("malformed %s changed from %q to %q", key, value, got[key])
		}
	}
}

func decodeStorageObject(t *testing.T, value string) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		t.Fatalf("failed to decode storage value %q: %v", value, err)
	}
	return decoded
}
