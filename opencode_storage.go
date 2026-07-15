/* *
 * Webmux - a browser-based pane multiplexer
 * Copyright (C) 2026  Webmux contributors
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */
package main

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
)

const (
	openCodeStorageNamespace       = "opencode"
	openCodeCanonicalServerID      = "webmux"
	openCodeGlobalStoragePrefix    = "opencode.global.dat:"
	openCodeServerStorageKey       = "opencode.global.dat:server"
	openCodeLayoutStorageKey       = "opencode.global.dat:layout"
	openCodeLayoutPageStorageKey   = "opencode.global.dat:layout.page"
	openCodeRouteContextStorageKey = "opencode.global.dat:route.context"
	openCodeDefaultServerKey       = "opencode.settings.dat:defaultServerUrl"
	openCodeHomeServersKey         = "opencode.global.dat:home.servers"
	openCodeWindowTabsKey          = "opencode.window.browser.dat:tabs"
	openCodeWindowTabsClosedKey    = "opencode.window.browser.dat:tabs.closed"
	openCodeWindowTabsInfoKey      = "opencode.window.browser.dat:tabs.info"
	openCodeWindowTabsRecentKey    = "opencode.window.browser.dat:tabs.recent"
)

// canonicalizePaneStorageItems keeps the authoritative OpenCode snapshot free
// of browser origins. OpenCode sees the active origin only through the proxy's
// localStorage materialization layer.
func canonicalizePaneStorageItems(namespace string, items map[string]string) map[string]string {
	if namespace != openCodeStorageNamespace {
		return copyPaneStorageItems(items)
	}
	return canonicalizeOpenCodeStorageItems(items)
}

func canonicalizeOpenCodeStorageItems(items map[string]string) map[string]string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	// Alias keys are applied first so an already-canonical key wins collisions.
	sort.Slice(keys, func(i, j int) bool {
		iCanonical := canonicalizeOpenCodeStorageKey(keys[i]) == keys[i]
		jCanonical := canonicalizeOpenCodeStorageKey(keys[j]) == keys[j]
		if iCanonical != jCanonical {
			return !iCanonical
		}
		return keys[i] < keys[j]
	})

	canonical := make(map[string]string, len(items))
	for _, key := range keys {
		canonicalKey := canonicalizeOpenCodeStorageKey(key)
		canonical[canonicalKey] = canonicalizeOpenCodeStorageValue(canonicalKey, items[key])
	}
	return canonical
}

func canonicalizeOpenCodeStorageKey(key string) string {
	if !strings.HasPrefix(key, openCodeGlobalStoragePrefix) {
		return key
	}
	suffix := strings.TrimPrefix(key, openCodeGlobalStoragePrefix)
	if separator := strings.IndexByte(suffix, 0); separator >= 0 {
		return openCodeGlobalStoragePrefix + suffix[separator+1:]
	}
	return key
}

func canonicalizeOpenCodeStorageValue(key, value string) string {
	switch key {
	case openCodeDefaultServerKey:
		if value != "" {
			return openCodeCanonicalServerID
		}
	case openCodeServerStorageKey:
		return canonicalizeOpenCodeServerStorageValue(value)
	case openCodeLayoutStorageKey:
		return canonicalizeOpenCodeLayoutStorageValue(value)
	case openCodeRouteContextStorageKey:
		return canonicalizeOpenCodeRouteContextStorageValue(value)
	case openCodeHomeServersKey:
		return translateOpenCodeHomeServersStorageValue(value, openCodeCanonicalServerID)
	case openCodeWindowTabsKey:
		return translateOpenCodeTabsStorageValue(value, openCodeCanonicalServerID)
	case openCodeWindowTabsClosedKey:
		return translateOpenCodeClosedTabsStorageValue(value, openCodeCanonicalServerID)
	case openCodeWindowTabsInfoKey:
		return translateOpenCodeTabInfoStorageValue(value, openCodeCanonicalServerID)
	case openCodeWindowTabsRecentKey:
		return translateOpenCodeRecentTabStorageValue(value, openCodeCanonicalServerID)
	}
	return value
}

func canonicalizeOpenCodeServerStorageValue(value string) string {
	var parsed map[string]any
	if json.Unmarshal([]byte(value), &parsed) != nil || parsed == nil {
		return value
	}

	projects := mergeOpenCodeProjectLists(objectMap(parsed["projects"]))
	lastProject := firstOpenCodeScopedString(objectMap(parsed["lastProject"]))
	recentlyClosed := mergeOpenCodeStringLists(objectMap(parsed["recentlyClosed"]))

	parsed["list"] = []any{}
	parsed["projects"] = map[string]any{}
	if len(projects) > 0 {
		parsed["projects"].(map[string]any)[openCodeCanonicalServerID] = projects
	}
	parsed["lastProject"] = map[string]any{}
	if lastProject != "" {
		parsed["lastProject"].(map[string]any)[openCodeCanonicalServerID] = lastProject
	}
	parsed["recentlyClosed"] = map[string]any{}
	if len(recentlyClosed) > 0 {
		parsed["recentlyClosed"].(map[string]any)[openCodeCanonicalServerID] = recentlyClosed
	}
	return marshalOpenCodeStorageValue(parsed, value)
}

func canonicalizeOpenCodeLayoutStorageValue(value string) string {
	var parsed map[string]any
	if json.Unmarshal([]byte(value), &parsed) != nil || parsed == nil {
		return value
	}
	for _, field := range []string{"sessionTabs", "sessionView", "handoff"} {
		parsed[field] = canonicalizeOpenCodeScopedMap(objectMap(parsed[field]))
	}
	if home := objectMap(parsed["home"]); home != nil {
		if selection := objectMap(home["selection"]); selection != nil {
			if server, ok := selection["server"].(string); ok && server != "" {
				selection["server"] = openCodeCanonicalServerID
			}
		}
	}
	return marshalOpenCodeStorageValue(parsed, value)
}

func canonicalizeOpenCodeRouteContextStorageValue(value string) string {
	var parsed map[string]any
	if json.Unmarshal([]byte(value), &parsed) != nil || parsed == nil {
		return value
	}
	if server, ok := parsed["server"].(string); ok && server != "" {
		parsed["server"] = openCodeCanonicalServerID
	}
	return marshalOpenCodeStorageValue(parsed, value)
}

func translateOpenCodeTabsStorageValue(value, serverID string) string {
	var tabs []any
	if json.Unmarshal([]byte(value), &tabs) != nil {
		return value
	}
	for _, tab := range tabs {
		translateOpenCodeTabServer(objectMap(tab), serverID)
	}
	return marshalOpenCodeStorageValue(tabs, value)
}

func translateOpenCodeClosedTabsStorageValue(value, serverID string) string {
	var entries []any
	if json.Unmarshal([]byte(value), &entries) != nil {
		return value
	}
	for _, entry := range entries {
		translateOpenCodeTabServer(objectMap(objectMap(entry)["tab"]), serverID)
	}
	return marshalOpenCodeStorageValue(entries, value)
}

func translateOpenCodeTabInfoStorageValue(value, serverID string) string {
	var info map[string]any
	if json.Unmarshal([]byte(value), &info) != nil || info == nil {
		return value
	}
	keys := make([]string, 0, len(info))
	for key := range info {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		iCanonical := translateOpenCodeTabID(keys[i], serverID) == keys[i]
		jCanonical := translateOpenCodeTabID(keys[j], serverID) == keys[j]
		if iCanonical != jCanonical {
			return !iCanonical
		}
		return keys[i] < keys[j]
	})
	translated := make(map[string]any, len(info))
	for _, key := range keys {
		translated[translateOpenCodeTabID(key, serverID)] = info[key]
	}
	return marshalOpenCodeStorageValue(translated, value)
}

func translateOpenCodeRecentTabStorageValue(value, serverID string) string {
	var recent map[string]any
	if json.Unmarshal([]byte(value), &recent) != nil || recent == nil {
		return value
	}
	if key, ok := recent["key"].(string); ok {
		recent["key"] = translateOpenCodeTabID(key, serverID)
	}
	return marshalOpenCodeStorageValue(recent, value)
}

func translateOpenCodeHomeServersStorageValue(value, serverID string) string {
	var homeServers map[string]any
	if json.Unmarshal([]byte(value), &homeServers) != nil || homeServers == nil {
		return value
	}
	if collapsed := objectMap(homeServers["collapsed"]); collapsed != nil {
		isCollapsed := false
		for _, value := range collapsed {
			if value == true {
				isCollapsed = true
				break
			}
		}
		homeServers["collapsed"] = map[string]any{}
		if isCollapsed {
			homeServers["collapsed"].(map[string]any)[serverID] = true
		}
	}
	return marshalOpenCodeStorageValue(homeServers, value)
}

func translateOpenCodeTabServer(tab map[string]any, serverID string) {
	if tab == nil {
		return
	}
	if server, ok := tab["server"].(string); ok && server != "" {
		tab["server"] = serverID
	}
}

func translateOpenCodeTabID(value, serverID string) string {
	separator := strings.IndexByte(value, '\n')
	if separator < 0 {
		return value
	}
	return serverID + "\n" + translateOpenCodeRoute(value[separator+1:], serverID)
}

func translateOpenCodeRoute(route, serverID string) string {
	const serverRoutePrefix = "/server/"
	if strings.HasPrefix(route, serverRoutePrefix) {
		if routeSeparator := strings.IndexByte(route[len(serverRoutePrefix):], '/'); routeSeparator >= 0 {
			routeSeparator += len(serverRoutePrefix)
			route = serverRoutePrefix + base64.RawURLEncoding.EncodeToString([]byte(serverID)) + route[routeSeparator:]
		}
	}
	return route
}

func canonicalizeOpenCodeScopedMap(items map[string]any) map[string]any {
	if items == nil {
		return map[string]any{}
	}
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		iCanonical := strings.IndexByte(keys[i], 0) < 0 || strings.HasPrefix(keys[i], openCodeCanonicalServerID+"\x00")
		jCanonical := strings.IndexByte(keys[j], 0) < 0 || strings.HasPrefix(keys[j], openCodeCanonicalServerID+"\x00")
		if iCanonical != jCanonical {
			return !iCanonical
		}
		return keys[i] < keys[j]
	})
	canonical := make(map[string]any, len(items))
	for _, originalKey := range keys {
		key := originalKey
		if separator := strings.IndexByte(key, 0); separator >= 0 {
			key = openCodeCanonicalServerID + key[separator:]
		}
		canonical[key] = items[originalKey]
	}
	return canonical
}

func mergeOpenCodeProjectLists(scopes map[string]any) []any {
	seen := map[string]bool{}
	merged := []any{}
	for _, scope := range orderedOpenCodeScopes(scopes) {
		projects, ok := scopes[scope].([]any)
		if !ok {
			continue
		}
		for _, project := range projects {
			projectMap, _ := project.(map[string]any)
			worktree, _ := projectMap["worktree"].(string)
			if worktree != "" && seen[worktree] {
				continue
			}
			if worktree != "" {
				seen[worktree] = true
			}
			merged = append(merged, project)
		}
	}
	return merged
}

func mergeOpenCodeStringLists(scopes map[string]any) []any {
	seen := map[string]bool{}
	merged := []any{}
	for _, scope := range orderedOpenCodeScopes(scopes) {
		values, ok := scopes[scope].([]any)
		if !ok {
			continue
		}
		for _, value := range values {
			text, ok := value.(string)
			if !ok || seen[text] {
				continue
			}
			seen[text] = true
			merged = append(merged, text)
		}
	}
	return merged
}

func firstOpenCodeScopedString(scopes map[string]any) string {
	for _, scope := range orderedOpenCodeScopes(scopes) {
		if value, ok := scopes[scope].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func orderedOpenCodeScopes(scopes map[string]any) []string {
	keys := make([]string, 0, len(scopes))
	for key := range scopes {
		if key != openCodeCanonicalServerID && key != "local" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	ordered := make([]string, 0, len(keys)+2)
	for _, preferred := range []string{openCodeCanonicalServerID, "local"} {
		if _, ok := scopes[preferred]; ok {
			ordered = append(ordered, preferred)
		}
	}
	return append(ordered, keys...)
}

func objectMap(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func marshalOpenCodeStorageValue(value any, fallback string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fallback
	}
	return string(encoded)
}
