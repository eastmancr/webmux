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
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
)

// SECTION: OPENCODE PROXY

var sourceMapCommentRE = regexp.MustCompile(`(?m)\n?(//# sourceMappingURL=.*$|/\*# sourceMappingURL=.*\*/)`)

type openCodeCompatibilityResult struct {
	Warning string
	Error   string
	Details []string
}

func analyzeOpenCodeIndexCompatibility(content string) openCodeCompatibilityResult {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return openCodeCompatibilityResult{Error: "OpenCode index response was empty"}
	}
	lower := strings.ToLower(trimmed)
	if !strings.Contains(lower, "<html") && !strings.Contains(lower, "<!doctype html") && !strings.Contains(lower, "<head") && !strings.Contains(lower, "<body") {
		return openCodeCompatibilityResult{Error: "OpenCode index response is not an HTML document"}
	}

	warnings := []string{}
	if !strings.Contains(lower, "<head") {
		warnings = append(warnings, "OpenCode HTML has no head element; Webmux will prepend proxy shims instead of injecting them into the document head.")
	}
	if !strings.Contains(lower, "<script") {
		warnings = append(warnings, "OpenCode HTML has no script tags; its web app structure may have changed.")
	}
	if !strings.Contains(lower, "assets/") && !strings.Contains(lower, "/assets/") {
		warnings = append(warnings, "OpenCode HTML has no recognizable asset references; routing or rendering may have changed.")
	}
	if len(warnings) > 0 {
		return openCodeCompatibilityResult{Warning: strings.Join(warnings, " "), Details: warnings}
	}
	return openCodeCompatibilityResult{}
}

func analyzeOpenCodeStorageSchema(items map[string]string) openCodeCompatibilityResult {
	if len(items) == 0 {
		return openCodeCompatibilityResult{}
	}
	knownGlobal := map[string]bool{
		"opencode.global.dat:server":               true,
		"opencode.global.dat:layout":               true,
		"opencode.global.dat:layout.page":          true,
		"opencode.global.dat:model":                true,
		"opencode.global.dat:command.catalog.v1":   true,
		"opencode.global.dat:notification":         true,
		"opencode.global.dat:prompt-history":       true,
		"opencode.global.dat:prompt-history-shell": true,
		"opencode.global.dat:route.context":        true,
		"opencode.global.dat:go-upsell":            true,
		"opencode.global.dat:home.servers":         true,
		"opencode.global.dat:language":             true,
		"opencode.global.dat:open.app":             true,
		"opencode.global.dat:review-panel-v2":      true,
	}
	structuredGlobal := map[string]bool{
		"opencode.global.dat:server":        true,
		"opencode.global.dat:layout":        true,
		"opencode.global.dat:layout.page":   true,
		"opencode.global.dat:route.context": true,
		"opencode.global.dat:home.servers":  true,
	}
	unknownGlobal := []string{}
	malformedKnown := []string{}
	for key, value := range items {
		if !strings.HasPrefix(key, "opencode.global.dat:") {
			continue
		}
		if !knownGlobal[key] {
			unknownGlobal = append(unknownGlobal, key)
			continue
		}
		if !structuredGlobal[key] || value == "" || value == "null" {
			continue
		}
		var parsed any
		if err := json.Unmarshal([]byte(value), &parsed); err != nil {
			malformedKnown = append(malformedKnown, key)
		}
	}
	if len(malformedKnown) > 0 {
		return openCodeCompatibilityResult{
			Warning: fmt.Sprintf("OpenCode storage contains %s. Shared pane state may not restore correctly.", pluralizeCount(len(malformedKnown), "malformed known key", "malformed known keys")),
			Details: []string{"malformed known storage keys: " + strings.Join(malformedKnown, ", ")},
		}
	}
	if len(unknownGlobal) > 0 {
		return openCodeCompatibilityResult{
			Warning: fmt.Sprintf("OpenCode storage contains %s. Rendering will continue, but shared pane state may be incomplete.", pluralizeCount(len(unknownGlobal), "unrecognized global key", "unrecognized global keys")),
			Details: []string{"unrecognized global storage keys: " + strings.Join(unknownGlobal, ", ")},
		}
	}
	return openCodeCompatibilityResult{}
}

func pluralizeCount(count int, singular string, plural string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %s", count, plural)
}

func combineOpenCodeCompatibilityWarnings(results ...openCodeCompatibilityResult) (string, []string) {
	warnings := []string{}
	details := []string{}
	for _, result := range results {
		if result.Warning != "" {
			warnings = append(warnings, result.Warning)
		}
		details = append(details, result.Details...)
	}
	return strings.TrimSpace(strings.Join(warnings, " ")), details
}

func logOpenCodeCompatibilityWarning(paneID, backendID, warning string, details []string) {
	if len(details) == 0 {
		log.Printf("OpenCode compatibility warning for pane %s backend %s: %s", paneID, backendID, warning)
		return
	}
	log.Printf("OpenCode compatibility warning for pane %s backend %s: %s details=%q", paneID, backendID, warning, strings.Join(details, "; "))
}

func (or *OpenCodeRuntime) modifyOpenCodeIndexResponse(s *Server, paneID, backendID string, storage PaneStorageState, diagnostics DiagnosticsSettings, resp *http.Response) error {
	if resp.StatusCode >= 400 {
		return nil
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		return nil
	}

	body, err := readProxyResponseBody(resp)
	if err != nil {
		return err
	}

	content := string(body)
	compatibility := analyzeOpenCodeIndexCompatibility(content)
	if compatibility.Error != "" {
		return errors.New(compatibility.Error)
	}
	storageCompatibility := analyzeOpenCodeStorageSchema(storage.Items)
	warning, details := combineOpenCodeCompatibilityWarnings(compatibility, storageCompatibility)
	detail := strings.Join(details, "; ")
	if or.updateWarning(warning, detail) && warning != "" && s != nil {
		logOpenCodeCompatibilityWarning(paneID, backendID, warning, details)
		s.diagnosticf("proxy", "event=opencode-compat-warning pane=%s backend=%s warning=%q", diagSanitize(paneID, 48), diagSanitize(backendID, 48), warning)
	}
	content = rewriteRootRelativeHTML(content)
	content = injectOpenCodeBaseElement(content, paneID)
	content = injectOpenCodeProxyScript(content, paneID, backendID, storage, diagnostics)
	content = injectPanePopoutBridge(content)

	writeProxyResponseBody(resp, content)
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Etag")
	resp.Header.Del("Content-Security-Policy")
	resp.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	resp.Header.Set("Pragma", "no-cache")
	resp.Header.Set("Expires", "0")

	return nil
}

func injectOpenCodeBaseElement(content, paneID string) string {
	script := fmt.Sprintf(`<script>
(function() {
  var marker = '/p/' + %q;
  var markerIndex = window.location.pathname.indexOf(marker);
  var base = markerIndex === -1 ? marker : window.location.pathname.slice(0, markerIndex + marker.length);
  document.write('<base href="' + base.replace(/"/g, '%%22') + '/">');
})();
</script>`, paneID)
	return injectIntoHTMLHead(content, script)
}

func (or *OpenCodeRuntime) modifyOpenCodeAssetResponse(_ string, resp *http.Response) error {
	if resp.StatusCode >= 400 {
		return nil
	}
	contentType := resp.Header.Get("Content-Type")
	requestPath := ""
	if resp.Request != nil && resp.Request.URL != nil {
		requestPath = resp.Request.URL.Path
	}
	isJS := strings.Contains(contentType, "javascript") || strings.HasSuffix(requestPath, ".js")
	isCSS := strings.Contains(contentType, "text/css") || strings.HasSuffix(requestPath, ".css")
	if !isJS && !isCSS {
		return nil
	}

	body, err := readProxyResponseBody(resp)
	if err != nil {
		return err
	}

	content := string(body)
	if isJS {
		content = rewriteOpenCodeJSInitialLocation(content)
	}
	if isCSS {
		content = rewriteOpenCodeCSSAssetURLs(content)
	}
	content = sourceMapCommentRE.ReplaceAllString(content, "")

	writeProxyResponseBody(resp, content)
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Etag")

	return nil
}

func rewriteOpenCodeJSInitialLocation(content string) string {
	return strings.ReplaceAll(content,
		`window.location.pathname.replace(/^\/+/,"/")`,
		`(window.__webmuxOpenCodePathname||window.location.pathname).replace(/^\/+/,"/")`,
	)
}

func rewriteOpenCodeCSSAssetURLs(content string) string {
	replacements := []struct {
		old string
		new string
	}{
		{`url("/assets/`, `url("`},
		{`url('/assets/`, `url('`},
		{`url(/assets/`, `url(`},
	}
	for _, replacement := range replacements {
		content = strings.ReplaceAll(content, replacement.old, replacement.new)
	}
	return content
}

func rewriteOpenCodeRequestOrigin(targetHost string, req *http.Request) {
	backendOrigin := "http://" + targetHost
	if req.Header.Get("Origin") != "" {
		req.Header.Set("Origin", backendOrigin)
	}
	if req.Header.Get("Referer") != "" {
		req.Header.Set("Referer", backendOrigin+"/")
	}
}

func rewriteRootRelativeHTML(content string) string {
	replacements := []struct {
		old string
		new string
	}{
		{`href="/`, `href="`},
		{`src="/`, `src="`},
		{`content="/`, `content="`},
		{`href='/`, `href='`},
		{`src='/`, `src='`},
		{`content='/`, `content='`},
	}
	for _, replacement := range replacements {
		content = strings.ReplaceAll(content, replacement.old, replacement.new)
	}
	return content
}

func injectOpenCodeProxyScript(content, paneID, backendID string, storage PaneStorageState, diagnostics DiagnosticsSettings) string {
	storageJSON, err := json.Marshal(storage.Items)
	if err != nil {
		storageJSON = []byte("{}")
	}
	diagnosticsJSON, err := json.Marshal(diagnostics)
	if err != nil {
		diagnosticsJSON = []byte("{}")
	}
	script := fmt.Sprintf(`<script>
(function() {
  var paneID = %q;
  var backendID = %q;
  var serverStorage = %s;
  var diagnostics = %s;
  var storageVersion = %d;
  var clientID = 'client-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2);
  var fallbackBase = '/p/' + paneID;
  var marker = '/p/' + paneID;
  var markerIndex = window.location.pathname.indexOf(marker);
  var base = markerIndex === -1 ? fallbackBase : window.location.pathname.slice(0, markerIndex + marker.length);
  var requestedOpenCodePath = markerIndex === -1 ? '/' : (window.location.pathname.slice(markerIndex + marker.length) || '/');
  var activePathStorageKey = 'webmux.internal.opencode.activePath';
  var activeOpenCodePath = Object.prototype.hasOwnProperty.call(serverStorage, activePathStorageKey) ? serverStorage[activePathStorageKey] : '';
  var activePathNeedsReset = false;
  delete serverStorage[activePathStorageKey];
  if (requestedOpenCodePath === '/' && typeof activeOpenCodePath === 'string' && activeOpenCodePath.startsWith('/')) {
    if (activeOpenCodePathReferencesOpenTab(activeOpenCodePath, serverStorage)) {
      requestedOpenCodePath = translateOpenCodeRoute(activeOpenCodePath, window.location.origin);
      if (requestedOpenCodePath !== '/') {
        try { window.history.replaceState(window.history.state, '', base + requestedOpenCodePath); } catch (e) {}
      }
    } else {
      activeOpenCodePath = recentOpenCodePathForOpenTab(serverStorage) || '/';
      activePathNeedsReset = true;
      requestedOpenCodePath = translateOpenCodeRoute(activeOpenCodePath, window.location.origin);
      if (requestedOpenCodePath !== '/') {
        try { window.history.replaceState(window.history.state, '', base + requestedOpenCodePath); } catch (e) {}
      }
    }
  }
  window.__webmuxOpenCodePathname = requestedOpenCodePath;
  var storageBase = base.replace(marker, '/api/pane-storage/' + encodeURIComponent(backendID));
  var storageEventsBase = base.replace(marker, '/api/pane-storage-events/' + encodeURIComponent(backendID));
  var diagnosticsBase = base.replace(marker, '/api/diagnostics/client');
  var originalFetch = window.fetch;
  var OriginalWebSocketForStorage = window.WebSocket;
  var storageSyncIdleDelay = 1000;
  var storageSyncMaxDelay = 5000;
  var storageSyncKeepaliveLimit = 60 * 1024;
  var pendingStorageClear = false;
  var pendingStorageSets = {};
  var pendingStorageRemoves = {};
  var pendingStorageStartedAt = 0;
  var storageSyncIdleTimer = null;
  var storageSyncMaxTimer = null;
  var storageFlushInFlight = false;
  var storageFlushPromise = Promise.resolve(true);
  var pendingSnapshotFetch = false;
  var opencodeServerStorageKey = 'opencode.global.dat:server';
  var opencodeDefaultServerStorageKey = 'opencode.settings.dat:defaultServerUrl';
  var opencodeHomeServersKey = 'opencode.global.dat:home.servers';
  var opencodeWindowTabsKey = 'opencode.window.browser.dat:tabs';
  var opencodeWindowTabsClosedKey = 'opencode.window.browser.dat:tabs.closed';
  var opencodeWindowTabsInfoKey = 'opencode.window.browser.dat:tabs.info';
  var opencodeWindowTabsRecentKey = 'opencode.window.browser.dat:tabs.recent';
  var opencodeNotificationStorageKey = 'opencode.global.dat:notification';
  var opencodeGlobalStoragePrefix = 'opencode.global.dat:';
  var opencodeScopeSeparator = '\u0000';
  var canonicalServerID = 'webmux';
  // The OpenCode web app treats the current page origin as its canonical local
  // server and maps that server scope to "local" internally. Store as webmux,
  // but present local-scoped state back to OpenCode for the active origin.
  var currentServerID = 'local';
  var attentionChannel = 'BroadcastChannel' in window ? new BroadcastChannel('webmux-popouts') : null;

  function requestWebmuxAttention() {
    if (!document.hidden && document.hasFocus()) return;
    var message = { type: 'webmux-pane-attention', paneId: paneID, backendId: backendID };
    try {
      if (window.parent !== window) window.parent.postMessage(message, window.location.origin);
    } catch (e) {}
    try { if (attentionChannel) attentionChannel.postMessage(message); } catch (e) {}
  }

  function isOpenCodeAttentionRequest(value) {
    try {
      if (typeof value === 'string') value = JSON.parse(value);
      if (!value || typeof value !== 'object') return false;
      if (Array.isArray(value)) return value.some(isOpenCodeAttentionRequest);
      if (value.type === 'permission.asked' || value.type === 'question.asked') return true;
      return value.payload ? isOpenCodeAttentionRequest(value.payload) : false;
    } catch (e) {
      return false;
    }
  }

  function watchOpenCodeEventStream(response) {
    try {
      if (!response || !response.headers || !response.body) return;
      if (!(response.headers.get('Content-Type') || '').toLowerCase().includes('text/event-stream')) return;
      var reader = response.clone().body.getReader();
      var decoder = new TextDecoder();
      var buffer = '';
      function inspectBlock(block) {
        var data = block.split(/\r?\n/).filter(function(line) { return line.indexOf('data:') === 0; }).map(function(line) {
          return line.slice(5).replace(/^ /, '');
        }).join('\n');
        if (data && isOpenCodeAttentionRequest(data)) requestWebmuxAttention();
      }
      function readNext() {
        reader.read().then(function(result) {
          if (result.done) return;
          buffer += decoder.decode(result.value, { stream: true });
          var separator;
          while ((separator = buffer.search(/\r?\n\r?\n/)) !== -1) {
            var block = buffer.slice(0, separator);
            var delimiter = buffer.slice(separator).match(/^\r?\n\r?\n/)[0];
            buffer = buffer.slice(separator + delimiter.length);
            inspectBlock(block);
          }
          readNext();
        }).catch(function() {});
      }
      readNext();
    } catch (e) {}
  }

  function hasNewOpenCodeAttention(oldValue, newValue) {
    try {
      var oldList = oldValue ? JSON.parse(oldValue).list : [];
      var newList = newValue ? JSON.parse(newValue).list : [];
      if (!Array.isArray(oldList) || !Array.isArray(newList)) return false;
      var existing = {};
      oldList.forEach(function(item) {
        if (!item || (item.type !== 'turn-complete' && item.type !== 'error')) return;
        existing[item.type + '\u0000' + item.session + '\u0000' + item.time] = true;
      });
      return newList.some(function(item) {
        if (!item || (item.type !== 'turn-complete' && item.type !== 'error')) return false;
        return !existing[item.type + '\u0000' + item.session + '\u0000' + item.time];
      });
    } catch (e) {
      return false;
    }
  }

  if (window.Notification) {
    var OriginalNotification = window.Notification;
    var WebmuxNotification = function(title, options) {
      requestWebmuxAttention();
      return new OriginalNotification(title, options);
    };
    WebmuxNotification.prototype = OriginalNotification.prototype;
    try { Object.setPrototypeOf(WebmuxNotification, OriginalNotification); } catch (e) {}
    try {
      Object.defineProperty(WebmuxNotification, 'permission', {
        configurable: true,
        get: function() { return OriginalNotification.permission; }
      });
    } catch (e) {}
    if (OriginalNotification.requestPermission) {
      WebmuxNotification.requestPermission = OriginalNotification.requestPermission.bind(OriginalNotification);
    }
    try { window.Notification = WebmuxNotification; } catch (e) {}
  }

  if (window.ServiceWorkerRegistration && ServiceWorkerRegistration.prototype.showNotification) {
    var originalShowNotification = ServiceWorkerRegistration.prototype.showNotification;
    ServiceWorkerRegistration.prototype.showNotification = function() {
      requestWebmuxAttention();
      return originalShowNotification.apply(this, arguments);
    };
  }

  function diagnosticsEnabled() {
    return diagnostics && diagnostics.enabled && diagnostics.clientEvents;
  }
  function postDiagnostic(source, event, details) {
    if (!diagnosticsEnabled()) return;
    details = details || {};
    var payload = JSON.stringify([{
      source: source,
      event: event,
      paneId: paneID,
      backendId: backendID,
      paneType: 'opencode',
      path: details.path || '',
      ageMs: details.ageMs || 0,
      data: details.data || {}
    }]);
    try {
      if (navigator.sendBeacon && navigator.sendBeacon(diagnosticsBase, new Blob([payload], { type: 'application/json' }))) return;
    } catch (e) {}
    try { originalFetch(diagnosticsBase, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: payload, keepalive: true }).catch(function() {}); } catch (e) {}
  }

  function sortedStorageKeys() {
    var seen = {};
    return Object.keys(serverStorage).map(materializeOpenCodeStorageKey).filter(function(key) {
      if (seen[key]) return false;
      seen[key] = true;
      return true;
    }).sort();
  }
  function dispatchStorageChange(key, oldValue, newValue) {
    try {
      window.dispatchEvent(new StorageEvent('storage', {
        key: key,
        oldValue: oldValue,
        newValue: newValue,
        url: window.location.href
      }));
    } catch (e) {
      try { window.dispatchEvent(new Event('storage')); } catch (ignored) {}
    }
  }
  function replaceStorage(nextItems, nextVersion) {
    nextItems = nextItems || {};
    if (Object.prototype.hasOwnProperty.call(nextItems, activePathStorageKey)) {
      activeOpenCodePath = nextItems[activePathStorageKey];
      delete nextItems[activePathStorageKey];
    }
    var oldStorage = serverStorage;
    serverStorage = normalizeOpenCodeStorageItems(nextItems);
    storageVersion = nextVersion || 0;
    var seen = {};
    Object.keys(oldStorage).forEach(function(key) {
      seen[key] = true;
      if (oldStorage[key] !== serverStorage[key]) {
        dispatchStorageChange(materializeOpenCodeStorageKey(key), materializeOpenCodeStorageValue(key, oldStorage[key]), Object.prototype.hasOwnProperty.call(serverStorage, key) ? materializeOpenCodeStorageValue(key, serverStorage[key]) : null);
      }
    });
    Object.keys(serverStorage).forEach(function(key) {
      if (!seen[key]) {
        dispatchStorageChange(materializeOpenCodeStorageKey(key), null, materializeOpenCodeStorageValue(key, serverStorage[key]));
      }
    });
  }
  function postStorageUpdate(payload, keepalive) {
    payload.clientId = clientID;
    return originalFetch(storageBase, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
      keepalive: keepalive === true
    }).then(function(response) {
      if (!response.ok) throw new Error('storage update failed: HTTP ' + response.status);
      return response.json();
    }).then(function(snapshot) {
      if (snapshot && snapshot.version >= storageVersion) {
        if (hasPendingStorageUpdates()) {
          storageVersion = snapshot.version || storageVersion;
          pendingSnapshotFetch = true;
          return;
        }
        replaceStorage(snapshot.items || {}, snapshot.version || 0);
      }
    }).catch(function(err) { throw err; });
  }
  function hasPendingStorageUpdates() {
    return pendingStorageClear || Object.keys(pendingStorageSets).length > 0 || Object.keys(pendingStorageRemoves).length > 0;
  }
  function resetStorageSyncTimers() {
    if (storageSyncIdleTimer) clearTimeout(storageSyncIdleTimer);
    if (storageSyncMaxTimer) clearTimeout(storageSyncMaxTimer);
    storageSyncIdleTimer = null;
    storageSyncMaxTimer = null;
  }
  function scheduleStorageFlush() {
    if (!pendingStorageStartedAt) {
      pendingStorageStartedAt = Date.now();
      storageSyncMaxTimer = setTimeout(flushStorageUpdates, storageSyncMaxDelay);
    }
    if (storageSyncIdleTimer) clearTimeout(storageSyncIdleTimer);
    storageSyncIdleTimer = setTimeout(flushStorageUpdates, storageSyncIdleDelay);
  }
  function queueStorageUpdate(payload) {
    if (payload.operation === 'set' && payload.key === opencodeServerStorageKey) {
      payload.value = normalizeOpenCodeServerStorageValue(payload.value);
    } else if (payload.operation === 'set') {
      payload.value = normalizeOpenCodeRoutingStorageValue(payload.key, payload.value);
    }
    postStorageRoutingDiagnostic(payload);
    if (payload.operation === 'clear') {
      pendingStorageClear = true;
      pendingStorageSets = {};
      pendingStorageRemoves = {};
    } else if (payload.operation === 'set') {
      pendingStorageSets[payload.key] = payload.value;
      delete pendingStorageRemoves[payload.key];
    } else if (payload.operation === 'remove') {
      delete pendingStorageSets[payload.key];
      if (!pendingStorageClear) pendingStorageRemoves[payload.key] = true;
    }
    scheduleStorageFlush();
  }
  function pendingStorageUpdateSize() {
    var operations = [];
    if (pendingStorageClear) operations.push({ operation: 'clear' });
    Object.keys(pendingStorageSets).forEach(function(key) {
      operations.push({ operation: 'set', key: key, value: pendingStorageSets[key] });
    });
    Object.keys(pendingStorageRemoves).forEach(function(key) {
      operations.push({ operation: 'remove', key: key });
    });
    return new Blob([JSON.stringify({ operation: 'batch', operations: operations, clientId: clientID })]).size;
  }
  function takePendingStorageOperations() {
    var operations = [];
    if (pendingStorageClear) operations.push({ operation: 'clear' });
    Object.keys(pendingStorageSets).forEach(function(key) {
      operations.push({ operation: 'set', key: key, value: pendingStorageSets[key] });
    });
    Object.keys(pendingStorageRemoves).forEach(function(key) {
      operations.push({ operation: 'remove', key: key });
    });
    pendingStorageClear = false;
    pendingStorageSets = {};
    pendingStorageRemoves = {};
    pendingStorageStartedAt = 0;
    resetStorageSyncTimers();
    return operations;
  }
  function isOpenCodeServerStorageKey(key) {
    return canonicalizeOpenCodeStorageKey(key) === opencodeServerStorageKey;
  }
  function isOpenCodeRoutingStorageKey(key) {
    key = canonicalizeOpenCodeStorageKey(key);
    return key === opencodeServerStorageKey
      || key === 'opencode.global.dat:layout'
      || key === 'opencode.global.dat:layout.page';
  }
  function postStorageRoutingDiagnostic(payload) {
    if (!payload || !isOpenCodeRoutingStorageKey(payload.key)) return;
    postDiagnostic('opencode-storage-route', payload.operation || 'unknown', {
      data: summarizeOpenCodeRoutingStorageValue(payload.key, payload.value)
    });
  }
  function summarizeOpenCodeRoutingStorageValue(key, value) {
    key = canonicalizeOpenCodeStorageKey(key);
    var summary = { key: String(key) };
    if (typeof value !== 'string') return summary;
    try {
      var parsed = JSON.parse(value);
      if (key === opencodeServerStorageKey) {
        summary.projectServers = parsed.projects && typeof parsed.projects === 'object' ? Object.keys(parsed.projects) : [];
        summary.lastProjectServers = parsed.lastProject && typeof parsed.lastProject === 'object' ? Object.keys(parsed.lastProject) : [];
      } else if (key === 'opencode.global.dat:layout.page') {
        summary.lastProjectSessionCount = parsed.lastProjectSession && typeof parsed.lastProjectSession === 'object' ? Object.keys(parsed.lastProjectSession).length : 0;
        summary.lastProjectSessionIDs = [];
        if (parsed.lastProjectSession && typeof parsed.lastProjectSession === 'object') {
          Object.keys(parsed.lastProjectSession).slice(0, 8).forEach(function(project) {
            var session = parsed.lastProjectSession[project];
            if (session && session.id) summary.lastProjectSessionIDs.push(project + ':' + session.id);
          });
        }
      } else if (key === 'opencode.global.dat:layout') {
        summary.sessionTabsCount = parsed.sessionTabs && typeof parsed.sessionTabs === 'object' ? Object.keys(parsed.sessionTabs).length : 0;
        summary.sessionViewCount = parsed.sessionView && typeof parsed.sessionView === 'object' ? Object.keys(parsed.sessionView).length : 0;
      }
    } catch (e) {}
    return summary;
  }
  function projectsKeyForServerID(serverID) {
    try {
      var host = new URL(serverID).hostname;
      if (host === 'localhost' || host === '127.0.0.1') return 'local';
    } catch (e) {}
    return serverID;
  }
  function hasOwn(object, key) {
    return Object.prototype.hasOwnProperty.call(object, key);
  }
  function isOpenCodeGlobalStorageKey(key) {
    return String(key).startsWith(opencodeGlobalStoragePrefix);
  }
  function scopedOpenCodeGlobalKey(scope, name) {
    return opencodeGlobalStoragePrefix + scope + opencodeScopeSeparator + name;
  }
  function canonicalizeOpenCodeStorageKey(key) {
    key = String(key);
    if (!isOpenCodeGlobalStorageKey(key)) return key;
    var suffix = key.slice(opencodeGlobalStoragePrefix.length);
    var currentPrefix = currentServerID + opencodeScopeSeparator;
    var canonicalPrefix = canonicalServerID + opencodeScopeSeparator;
    if (suffix.startsWith(currentPrefix)) return opencodeGlobalStoragePrefix + suffix.slice(currentPrefix.length);
    if (suffix.startsWith(canonicalPrefix)) return opencodeGlobalStoragePrefix + suffix.slice(canonicalPrefix.length);
    return key;
  }
  function materializeOpenCodeStorageKey(key) {
    key = String(key);
    if (currentServerID === 'local' || currentServerID === canonicalServerID || !isOpenCodeGlobalStorageKey(key)) return key;
    var suffix = key.slice(opencodeGlobalStoragePrefix.length);
    if (suffix.indexOf(opencodeScopeSeparator) !== -1) return key;
    if (suffix === 'server') return key;
    return scopedOpenCodeGlobalKey(currentServerID, suffix);
  }
  function materializeOpenCodeStorageValue(key, value) {
    key = canonicalizeOpenCodeStorageKey(key);
    if (key === opencodeServerStorageKey) return materializeOpenCodeServerStorageValue(value);
    if (key === opencodeDefaultServerStorageKey) return window.location.origin;
    if (key === opencodeHomeServersKey) return translateOpenCodeHomeServersStorageValue(value, window.location.origin);
    if (key === opencodeWindowTabsKey) return translateOpenCodeTabsStorageValue(value, window.location.origin);
    if (key === opencodeWindowTabsClosedKey) return translateOpenCodeClosedTabsStorageValue(value, window.location.origin);
    if (key === opencodeWindowTabsInfoKey) return translateOpenCodeTabInfoStorageValue(value, window.location.origin);
    if (key === opencodeWindowTabsRecentKey) return translateOpenCodeRecentTabStorageValue(value, window.location.origin);
    if (key === 'opencode.global.dat:route.context') return translateOpenCodeRouteContextStorageValue(value, window.location.origin);
    if (key === 'opencode.global.dat:layout') return materializeOpenCodeLayoutStorageValue(value);
    if (key === 'opencode.global.dat:layout.page') return materializeOpenCodeLayoutPageStorageValue(value);
    return value;
  }
  function currentOpenCodeScopeID() {
    return currentServerID || 'local';
  }
  function canonicalizeOpenCodeScopedObjectKeys(object) {
    return translateOpenCodeScopedObjectKeys(object, canonicalServerID);
  }
  function materializeOpenCodeScopedObjectKeys(object) {
    return translateOpenCodeScopedObjectKeys(object, currentOpenCodeScopeID());
  }
  function canonicalizeOpenCodeServerReference(value) {
    return typeof value === 'string' && value !== '' ? canonicalServerID : value;
  }
  function materializeOpenCodeServerReference(value) {
    return typeof value === 'string' && value !== '' ? window.location.origin : value;
  }
  function translateOpenCodeTabID(value, serverID) {
    if (typeof value !== 'string') return value;
    var separator = value.indexOf('\n');
    if (separator === -1) return value;
    return serverID + '\n' + translateOpenCodeRoute(value.slice(separator + 1), serverID);
  }
  function translateOpenCodeRoute(route, serverID) {
    if (typeof route !== 'string') return route;
    if (route.startsWith('/server/')) {
      var routeSeparator = route.indexOf('/', '/server/'.length);
      if (routeSeparator !== -1) route = '/server/' + encodeOpenCodeServerID(serverID) + route.slice(routeSeparator);
    }
    return route;
  }
  function activeOpenCodePathReferencesOpenTab(path, items) {
    if (typeof path !== 'string') return false;
    var match = path.match(/\/session\/([^\/?#]+)/);
    if (!match || match[1] === 'new') return true;
    try {
      var tabs = JSON.parse(items['opencode.window.browser.dat:tabs'] || '[]');
      return Array.isArray(tabs) && tabs.some(function(tab) {
        return tab && tab.type === 'session' && tab.sessionId === match[1];
      });
    } catch (e) {
      return false;
    }
  }
  function recentOpenCodePathForOpenTab(items) {
    try {
      var recent = JSON.parse(items['opencode.window.browser.dat:tabs.recent'] || '{}');
      var separator = typeof recent.key === 'string' ? recent.key.indexOf('\n') : -1;
      if (separator === -1) return null;
      var path = recent.key.slice(separator + 1);
      return activeOpenCodePathReferencesOpenTab(path, items) ? path : null;
    } catch (e) {
      return null;
    }
  }
  function encodeOpenCodeServerID(serverID) {
    var bytes = new TextEncoder().encode(serverID);
    var binary = '';
    bytes.forEach(function(byte) { binary += String.fromCharCode(byte); });
    return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '');
  }
  function translateOpenCodeTabServer(tab, serverID) {
    if (tab && typeof tab === 'object' && typeof tab.server === 'string' && tab.server !== '') tab.server = serverID;
  }
  function translateOpenCodeTabsStorageValue(value, serverID) {
    if (typeof value !== 'string') return value;
    try {
      var tabs = JSON.parse(value);
      if (!Array.isArray(tabs)) return value;
      tabs.forEach(function(tab) { translateOpenCodeTabServer(tab, serverID); });
      return JSON.stringify(tabs);
    } catch (e) { return value; }
  }
  function translateOpenCodeClosedTabsStorageValue(value, serverID) {
    if (typeof value !== 'string') return value;
    try {
      var entries = JSON.parse(value);
      if (!Array.isArray(entries)) return value;
      entries.forEach(function(entry) { translateOpenCodeTabServer(entry && entry.tab, serverID); });
      return JSON.stringify(entries);
    } catch (e) { return value; }
  }
  function translateOpenCodeTabInfoStorageValue(value, serverID) {
    if (typeof value !== 'string') return value;
    try {
      var info = JSON.parse(value);
      if (!info || typeof info !== 'object' || Array.isArray(info)) return value;
      var translated = {};
      Object.keys(info).forEach(function(key) { translated[translateOpenCodeTabID(key, serverID)] = info[key]; });
      return JSON.stringify(translated);
    } catch (e) { return value; }
  }
  function translateOpenCodeRecentTabStorageValue(value, serverID) {
    if (typeof value !== 'string') return value;
    try {
      var recent = JSON.parse(value);
      if (!recent || typeof recent !== 'object' || Array.isArray(recent)) return value;
      if (typeof recent.key === 'string') recent.key = translateOpenCodeTabID(recent.key, serverID);
      return JSON.stringify(recent);
    } catch (e) { return value; }
  }
  function translateOpenCodeHomeServersStorageValue(value, serverID) {
    if (typeof value !== 'string') return value;
    try {
      var homeServers = JSON.parse(value);
      if (!homeServers || typeof homeServers !== 'object' || Array.isArray(homeServers)) return value;
      if (homeServers.collapsed && typeof homeServers.collapsed === 'object' && !Array.isArray(homeServers.collapsed)) {
        var isCollapsed = Object.keys(homeServers.collapsed).some(function(key) { return homeServers.collapsed[key] === true; });
        homeServers.collapsed = {};
        if (isCollapsed) homeServers.collapsed[serverID] = true;
      }
      return JSON.stringify(homeServers);
    } catch (e) { return value; }
  }
  function translateOpenCodeRouteContextStorageValue(value, serverID) {
    if (typeof value !== 'string') return value;
    try {
      var context = JSON.parse(value);
      if (!context || typeof context !== 'object' || Array.isArray(context)) return value;
      if (typeof context.server === 'string' && context.server !== '') context.server = serverID;
      return JSON.stringify(context);
    } catch (e) { return value; }
  }
  function translateOpenCodeScopedObjectKeys(object, targetScope) {
    if (!object || typeof object !== 'object' || Array.isArray(object)) return object;
    var translated = {};
    Object.keys(object).forEach(function(key) {
      translated[translateOpenCodeScopedKey(key, targetScope)] = object[key];
    });
    return translated;
  }
  function translateOpenCodeScopedKey(key, targetScope) {
    key = String(key);
    var sep = key.indexOf(opencodeScopeSeparator);
    if (sep === -1) return key;
    var scope = key.slice(0, sep);
    if (scope === currentOpenCodeScopeID() || scope === canonicalServerID || scope === 'local') {
      return targetScope + opencodeScopeSeparator + key.slice(sep + 1);
    }
    return key;
  }
  function firstProjectList(projects) {
    var keys = Object.keys(projects);
    for (var i = 0; i < keys.length; i++) {
      if (Array.isArray(projects[keys[i]])) return projects[keys[i]];
    }
    return null;
  }
  function firstLastProject(lastProject) {
    var keys = Object.keys(lastProject);
    for (var i = 0; i < keys.length; i++) {
      if (lastProject[keys[i]]) return lastProject[keys[i]];
    }
    return '';
  }
  function firstStringList(values) {
    var keys = Object.keys(values);
    for (var i = 0; i < keys.length; i++) {
      if (Array.isArray(values[keys[i]])) return values[keys[i]];
    }
    return null;
  }
  function normalizeOpenCodeServerStorageValue(value) {
    if (typeof value !== 'string' || value === '') return value;
    try {
      var parsed = JSON.parse(value);
      if (!parsed || typeof parsed !== 'object') return value;
      var projects = parsed.projects && typeof parsed.projects === 'object' && !Array.isArray(parsed.projects) ? parsed.projects : {};
      var lastProject = parsed.lastProject && typeof parsed.lastProject === 'object' && !Array.isArray(parsed.lastProject) ? parsed.lastProject : {};
      var recentlyClosed = parsed.recentlyClosed && typeof parsed.recentlyClosed === 'object' && !Array.isArray(parsed.recentlyClosed) ? parsed.recentlyClosed : {};
      var canonicalProjects = hasOwn(projects, currentServerID) ? projects[currentServerID]
        : hasOwn(projects, canonicalServerID) ? projects[canonicalServerID]
        : firstProjectList(projects);
      var canonicalLastProject = hasOwn(lastProject, currentServerID) ? lastProject[currentServerID]
        : hasOwn(lastProject, canonicalServerID) ? lastProject[canonicalServerID]
        : firstLastProject(lastProject);
      var canonicalRecentlyClosed = hasOwn(recentlyClosed, currentServerID) ? recentlyClosed[currentServerID]
        : hasOwn(recentlyClosed, canonicalServerID) ? recentlyClosed[canonicalServerID]
        : firstStringList(recentlyClosed);
      parsed.projects = {};
      if (Array.isArray(canonicalProjects)) parsed.projects[canonicalServerID] = canonicalProjects;
      parsed.lastProject = {};
      if (canonicalLastProject) parsed.lastProject[canonicalServerID] = canonicalLastProject;
      parsed.recentlyClosed = {};
      if (Array.isArray(canonicalRecentlyClosed)) parsed.recentlyClosed[canonicalServerID] = canonicalRecentlyClosed;
      return JSON.stringify(parsed);
    } catch (e) {
      return value;
    }
  }
  function materializeOpenCodeServerStorageValue(value) {
    if (typeof value !== 'string' || currentServerID === canonicalServerID) return value;
    try {
      var parsed = JSON.parse(normalizeOpenCodeServerStorageValue(value));
      if (!parsed || typeof parsed !== 'object') return value;
      if (parsed.projects && parsed.projects[canonicalServerID]) {
        var canonicalProjects = parsed.projects[canonicalServerID];
        delete parsed.projects[canonicalServerID];
        parsed.projects[currentServerID] = canonicalProjects;
      }
      if (parsed.lastProject && parsed.lastProject[canonicalServerID]) {
        var canonicalLastProject = parsed.lastProject[canonicalServerID];
        delete parsed.lastProject[canonicalServerID];
        parsed.lastProject[currentServerID] = canonicalLastProject;
      }
      if (parsed.recentlyClosed && parsed.recentlyClosed[canonicalServerID]) {
        var canonicalRecentlyClosed = parsed.recentlyClosed[canonicalServerID];
        delete parsed.recentlyClosed[canonicalServerID];
        parsed.recentlyClosed[currentServerID] = canonicalRecentlyClosed;
      }
      return JSON.stringify(parsed);
    } catch (e) {
      return value;
    }
  }
  function normalizeOpenCodeStorageItems(items) {
    if (!items || typeof items !== 'object') return {};
    var normalized = {};
    Object.keys(items).forEach(function(key) {
      var canonicalKey = canonicalizeOpenCodeStorageKey(key);
      if (canonicalKey === opencodeServerStorageKey) {
        normalized[canonicalKey] = normalizeOpenCodeServerStorageValue(items[key]);
      } else {
        normalized[canonicalKey] = normalizeOpenCodeRoutingStorageValue(canonicalKey, items[key]);
      }
    });
    return normalized;
  }
  function normalizeOpenCodeRoutingStorageValue(key, value) {
    if (key === opencodeDefaultServerStorageKey && typeof value === 'string' && value !== '') return canonicalServerID;
    if (key === opencodeHomeServersKey) return translateOpenCodeHomeServersStorageValue(value, canonicalServerID);
    if (key === opencodeWindowTabsKey) return translateOpenCodeTabsStorageValue(value, canonicalServerID);
    if (key === opencodeWindowTabsClosedKey) return translateOpenCodeClosedTabsStorageValue(value, canonicalServerID);
    if (key === opencodeWindowTabsInfoKey) return translateOpenCodeTabInfoStorageValue(value, canonicalServerID);
    if (key === opencodeWindowTabsRecentKey) return translateOpenCodeRecentTabStorageValue(value, canonicalServerID);
    if (key === 'opencode.global.dat:route.context') return translateOpenCodeRouteContextStorageValue(value, canonicalServerID);
    if (key === 'opencode.global.dat:layout') return normalizeOpenCodeLayoutStorageValue(value);
    if (key === 'opencode.global.dat:layout.page') return normalizeOpenCodeLayoutPageStorageValue(value);
    return value;
  }
  function normalizeOpenCodeLayoutStorageValue(value) {
    if (typeof value !== 'string' || value === '') return value;
    try {
      var parsed = JSON.parse(value);
      if (!parsed || typeof parsed !== 'object') return value;
      if (!parsed.sessionTabs || typeof parsed.sessionTabs !== 'object' || Array.isArray(parsed.sessionTabs)) parsed.sessionTabs = {};
      if (!parsed.sessionView || typeof parsed.sessionView !== 'object' || Array.isArray(parsed.sessionView)) parsed.sessionView = {};
      if (!parsed.handoff || typeof parsed.handoff !== 'object' || Array.isArray(parsed.handoff)) parsed.handoff = {};
      parsed.sessionTabs = canonicalizeOpenCodeScopedObjectKeys(parsed.sessionTabs);
      parsed.sessionView = canonicalizeOpenCodeScopedObjectKeys(parsed.sessionView);
      parsed.handoff = canonicalizeOpenCodeScopedObjectKeys(parsed.handoff);
      if (parsed.home && parsed.home.selection) {
        parsed.home.selection.server = canonicalizeOpenCodeServerReference(parsed.home.selection.server);
      }
      return JSON.stringify(parsed);
    } catch (e) {
      return value;
    }
  }
  function materializeOpenCodeLayoutStorageValue(value) {
    if (typeof value !== 'string' || value === '') return value;
    try {
      var parsed = JSON.parse(normalizeOpenCodeLayoutStorageValue(value));
      if (!parsed || typeof parsed !== 'object') return value;
      parsed.sessionTabs = materializeOpenCodeScopedObjectKeys(parsed.sessionTabs);
      parsed.sessionView = materializeOpenCodeScopedObjectKeys(parsed.sessionView);
      parsed.handoff = materializeOpenCodeScopedObjectKeys(parsed.handoff);
      if (parsed.home && parsed.home.selection) {
        parsed.home.selection.server = materializeOpenCodeServerReference(parsed.home.selection.server);
      }
      return JSON.stringify(parsed);
    } catch (e) {
      return value;
    }
  }
  function normalizeOpenCodeLayoutPageStorageValue(value) {
    if (typeof value !== 'string' || value === '') return value;
    try {
      var parsed = JSON.parse(value);
      if (!parsed || typeof parsed !== 'object') return value;
      if (!parsed.lastProjectSession || typeof parsed.lastProjectSession !== 'object' || Array.isArray(parsed.lastProjectSession)) parsed.lastProjectSession = {};
      if (!parsed.workspaceOrder || typeof parsed.workspaceOrder !== 'object' || Array.isArray(parsed.workspaceOrder)) parsed.workspaceOrder = {};
      if (!parsed.workspaceName || typeof parsed.workspaceName !== 'object' || Array.isArray(parsed.workspaceName)) parsed.workspaceName = {};
      if (!parsed.workspaceBranchName || typeof parsed.workspaceBranchName !== 'object' || Array.isArray(parsed.workspaceBranchName)) parsed.workspaceBranchName = {};
      if (!parsed.workspaceExpanded || typeof parsed.workspaceExpanded !== 'object' || Array.isArray(parsed.workspaceExpanded)) parsed.workspaceExpanded = {};
      return JSON.stringify(parsed);
    } catch (e) {
      return value;
    }
  }
  function materializeOpenCodeLayoutPageStorageValue(value) {
    if (typeof value !== 'string' || value === '') return value;
    try {
      var parsed = JSON.parse(normalizeOpenCodeLayoutPageStorageValue(value));
      if (!parsed || typeof parsed !== 'object') return value;
      return JSON.stringify(parsed);
    } catch (e) {
      return value;
    }
  }
  var originalOpenCodeServerStorageValue = Object.prototype.hasOwnProperty.call(serverStorage, opencodeServerStorageKey) ? serverStorage[opencodeServerStorageKey] : null;
  var originalOpenCodeDefaultServerStorageValue = Object.prototype.hasOwnProperty.call(serverStorage, opencodeDefaultServerStorageKey) ? serverStorage[opencodeDefaultServerStorageKey] : null;
  var originalOpenCodeLayoutStorageValue = Object.prototype.hasOwnProperty.call(serverStorage, 'opencode.global.dat:layout') ? serverStorage['opencode.global.dat:layout'] : null;
  serverStorage = normalizeOpenCodeStorageItems(serverStorage);
  if (originalOpenCodeServerStorageValue !== null
      && serverStorage[opencodeServerStorageKey] !== originalOpenCodeServerStorageValue) {
    queueStorageUpdate({ operation: 'set', key: opencodeServerStorageKey, value: serverStorage[opencodeServerStorageKey] });
  }
  if (originalOpenCodeDefaultServerStorageValue !== null
      && serverStorage[opencodeDefaultServerStorageKey] !== originalOpenCodeDefaultServerStorageValue) {
    queueStorageUpdate({ operation: 'set', key: opencodeDefaultServerStorageKey, value: serverStorage[opencodeDefaultServerStorageKey] });
  }
  if (originalOpenCodeLayoutStorageValue !== null
      && serverStorage['opencode.global.dat:layout'] !== originalOpenCodeLayoutStorageValue) {
    queueStorageUpdate({ operation: 'set', key: 'opencode.global.dat:layout', value: serverStorage['opencode.global.dat:layout'] });
  }
  function flushStorageUpdates(keepalive) {
    if (storageFlushInFlight) return storageFlushPromise;
    if (!hasPendingStorageUpdates()) {
      resetStorageSyncTimers();
      pendingStorageStartedAt = 0;
      return Promise.resolve(true);
    }
    var operations = takePendingStorageOperations();
    if (operations.length === 0) return Promise.resolve(true);
    storageFlushInFlight = true;
    storageFlushPromise = postStorageUpdate({ operation: 'batch', operations: operations }, keepalive === true).then(function() {
      storageFlushInFlight = false;
      if (hasPendingStorageUpdates()) {
        scheduleStorageFlush();
      } else if (pendingSnapshotFetch) {
        pendingSnapshotFetch = false;
        fetchStorageSnapshot();
      }
      return true;
    }).catch(function() {
      storageFlushInFlight = false;
      requeueFailedStorageOperations(operations);
      postDiagnostic('opencode-storage', 'flush-error', {
        data: { keepalive: keepalive === true, operations: operations.length }
      });
      return false;
    });
    return storageFlushPromise;
  }
  function requeueFailedStorageOperations(operations) {
    var newerClear = pendingStorageClear;
    if (!newerClear && operations.some(function(operation) { return operation.operation === 'clear'; })) {
      pendingStorageClear = true;
    }
    operations.forEach(function(operation) {
      if (operation.operation === 'clear' || !operation.key || newerClear
          || Object.prototype.hasOwnProperty.call(pendingStorageSets, operation.key)
          || Object.prototype.hasOwnProperty.call(pendingStorageRemoves, operation.key)) return;
      if (operation.operation === 'set') pendingStorageSets[operation.key] = operation.value;
      if (operation.operation === 'remove' && !pendingStorageClear) pendingStorageRemoves[operation.key] = true;
    });
    scheduleStorageFlush();
  }
  function flushAllStorageUpdates() {
    return Promise.resolve(flushStorageUpdates()).then(function(success) {
      if (!success) return false;
      if (storageFlushInFlight || hasPendingStorageUpdates()) return flushAllStorageUpdates();
      return true;
    });
  }
  function fetchStorageSnapshot() {
    if (hasPendingStorageUpdates() || storageFlushInFlight) {
      pendingSnapshotFetch = true;
      return;
    }
    postDiagnostic('opencode-storage', 'snapshot-start', { data: { version: storageVersion } });
    originalFetch(storageBase, { cache: 'no-store' }).then(function(response) {
      if (!response.ok) {
        postDiagnostic('opencode-storage', 'snapshot-http-error', { data: { status: response.status } });
        return null;
      }
      return response.json();
    }).then(function(snapshot) {
      if (snapshot && snapshot.version > storageVersion) {
        if (hasPendingStorageUpdates() || storageFlushInFlight) {
          pendingSnapshotFetch = true;
          return;
        }
        postDiagnostic('opencode-storage', 'snapshot-apply', { data: { from: storageVersion, to: snapshot.version } });
        replaceStorage(snapshot.items || {}, snapshot.version || 0);
      }
    }).catch(function(err) { postDiagnostic('opencode-storage', 'snapshot-error', { data: { error: err && err.message } }); });
  }
  function storageWebSocketURL(path) {
    var protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    return protocol + '//' + window.location.host + path;
  }
  function connectStorageEvents() {
    var reconnectDelay = 2000;
    var reconnectTimer = null;
    var connectedOnce = false;
    var connect = function() {
      var ws = new OriginalWebSocketForStorage(storageWebSocketURL(storageEventsBase));
      var diagnosticPingTimer = null;
      ws.onopen = function() {
        postDiagnostic('opencode-storage-ws', 'open', { path: storageEventsBase });
        if (diagnostics && diagnostics.enabled && diagnostics.optionalPing) {
          diagnosticPingTimer = setInterval(function() {
            if (ws.readyState === OriginalWebSocketForStorage.OPEN) ws.send(JSON.stringify({ type: 'diagnostic-ping', ts: Date.now() }));
          }, Math.max(5, diagnostics.pingIntervalSeconds || 30) * 1000);
        }
        if (connectedOnce) fetchStorageSnapshot();
        connectedOnce = true;
      };
      ws.onmessage = function(event) {
        try {
          var message = JSON.parse(event.data);
          postDiagnostic('opencode-storage-ws', 'message', { data: { version: message && message.version, updatedBy: message && message.updatedBy } });
          if (message && message.updatedBy === clientID) return;
          if (message && message.type === 'storage' && message.version > storageVersion) {
            fetchStorageSnapshot();
          }
        } catch (e) {}
      };
      ws.onclose = function() {
        if (diagnosticPingTimer) clearInterval(diagnosticPingTimer);
        postDiagnostic('opencode-storage-ws', 'close', { path: storageEventsBase });
        if (!reconnectTimer) {
          reconnectTimer = setTimeout(function() {
            reconnectTimer = null;
            connect();
          }, reconnectDelay);
        }
      };
      ws.onerror = function() { postDiagnostic('opencode-storage-ws', 'error', { path: storageEventsBase }); ws.close(); };
    };
    connect();
  }
  var storageShim = {
    get length() { return sortedStorageKeys().length; },
    key: function(index) {
      var keys = sortedStorageKeys();
      return keys[index] || null;
    },
    getItem: function(key) {
      key = String(key);
      var canonicalKey = canonicalizeOpenCodeStorageKey(key);
      if (!Object.prototype.hasOwnProperty.call(serverStorage, canonicalKey)) return null;
      return materializeOpenCodeStorageValue(canonicalKey, serverStorage[canonicalKey]);
    },
    setItem: function(key, value) {
      key = String(key);
      value = String(value);
      var canonicalKey = canonicalizeOpenCodeStorageKey(key);
      if (canonicalKey !== key) {
        postDiagnostic('opencode-storage-key', 'canonicalize', { data: { from: key, to: canonicalKey } });
      }
      if (isOpenCodeServerStorageKey(canonicalKey)) value = normalizeOpenCodeServerStorageValue(value);
      else value = normalizeOpenCodeRoutingStorageValue(canonicalKey, value);
      var oldValue = Object.prototype.hasOwnProperty.call(serverStorage, canonicalKey) ? serverStorage[canonicalKey] : null;
      if (oldValue === value) return;
      if (canonicalKey === opencodeNotificationStorageKey && hasNewOpenCodeAttention(oldValue, value)) {
        requestWebmuxAttention();
      }
      serverStorage[canonicalKey] = value;
      dispatchStorageChange(key, oldValue === null ? null : materializeOpenCodeStorageValue(canonicalKey, oldValue), materializeOpenCodeStorageValue(canonicalKey, value));
      queueStorageUpdate({ operation: 'set', key: canonicalKey, value: value });
    },
    removeItem: function(key) {
      key = String(key);
      var canonicalKey = canonicalizeOpenCodeStorageKey(key);
      if (!Object.prototype.hasOwnProperty.call(serverStorage, canonicalKey)) return;
      var oldValue = serverStorage[canonicalKey];
      delete serverStorage[canonicalKey];
      dispatchStorageChange(key, materializeOpenCodeStorageValue(canonicalKey, oldValue), null);
      queueStorageUpdate({ operation: 'remove', key: canonicalKey });
    },
    clear: function() {
      var oldStorage = serverStorage;
      serverStorage = {};
      Object.keys(oldStorage).forEach(function(key) { dispatchStorageChange(materializeOpenCodeStorageKey(key), materializeOpenCodeStorageValue(key, oldStorage[key]), null); });
      queueStorageUpdate({ operation: 'clear' });
    }
  };
  if (typeof Proxy === 'function') {
    storageShim = new Proxy(storageShim, {
      get: function(target, property) {
        if (property in target) return target[property];
        if (typeof property === 'string') return target.getItem(property);
      },
      set: function(target, property, value) {
        if (typeof property !== 'string' || property in target) {
          target[property] = value;
          return true;
        }
        target.setItem(property, value);
        return true;
      },
      deleteProperty: function(target, property) {
        if (typeof property === 'string') target.removeItem(property);
        return true;
      },
      has: function(target, property) {
        return property in target || (typeof property === 'string' && target.getItem(property) !== null);
      },
      ownKeys: function(target) {
        return Reflect.ownKeys(target).concat(sortedStorageKeys());
      },
      getOwnPropertyDescriptor: function(target, property) {
        if (property in target) return Object.getOwnPropertyDescriptor(target, property);
        if (typeof property === 'string' && target.getItem(property) !== null) {
          return { configurable: true, enumerable: true, value: target.getItem(property) };
        }
      }
    });
  }
  try {
    Object.defineProperty(window, 'localStorage', { configurable: true, value: storageShim });
  } catch (e) {
    try { window.localStorage = storageShim; } catch (ignored) {}
  }
  window.__webmuxFlushOpenCodeStorage = flushAllStorageUpdates;
  connectStorageEvents();
  window.addEventListener('pagehide', function() {
    flushStorageUpdates(pendingStorageUpdateSize() <= storageSyncKeepaliveLimit);
  });

  function prefixURL(input) {
    if (input instanceof URL) input = input.toString();
    if (typeof input !== 'string') return input;
    if (input.startsWith(base + '/') || input === base) return input;
    if (input.startsWith(fallbackBase + '/')) return base + input.slice(fallbackBase.length);
    if (input.startsWith('/')) return base + input;
    return input;
  }
  function prefixAbsoluteURL(input) {
    if (input instanceof URL) input = input.toString();
    if (typeof input !== 'string') return input;
    try {
      var url = new URL(input, window.location.href);
      if (url.origin === window.location.origin && !url.pathname.startsWith(base + '/')) {
        if (url.pathname.startsWith(fallbackBase + '/')) {
          url.pathname = base + url.pathname.slice(fallbackBase.length);
        } else {
          url.pathname = base + url.pathname;
        }
        return url.toString();
      }
    } catch (e) {}
    return prefixURL(input);
  }
  function prefixWebSocketURL(input) {
    if (input instanceof URL) input = input.toString();
    if (typeof input !== 'string') return input;
    try {
      var url = new URL(input, window.location.href);
      var httpOrigin = window.location.origin;
      var wsOrigin = (window.location.protocol === 'https:' ? 'wss://' : 'ws://') + window.location.host;
      if ((url.origin === httpOrigin || url.origin === wsOrigin) && !url.pathname.startsWith(base + '/')) {
        if (url.pathname.startsWith(fallbackBase + '/')) {
          url.pathname = base + url.pathname.slice(fallbackBase.length);
        } else {
          url.pathname = base + url.pathname;
        }
      }
      if (url.protocol === 'http:') url.protocol = 'ws:';
      if (url.protocol === 'https:') url.protocol = 'wss:';
      return url.toString();
    } catch (e) {}
    return input;
  }
  function prefixElementURL(value) {
    if (typeof value !== 'string') return value;
    try {
      var url = new URL(value, window.location.href);
      if (url.origin === window.location.origin && !url.pathname.startsWith(base + '/')) {
        if (url.pathname.startsWith(fallbackBase + '/')) {
          url.pathname = base + url.pathname.slice(fallbackBase.length);
          return url.toString();
        }
        if (url.pathname.startsWith('/assets/')) {
          url.pathname = base + url.pathname;
          return url.toString();
        }
      }
    } catch (e) {}
    return value;
  }
  function prefixAnchorURL(value) {
    if (typeof value !== 'string') return value;
    return prefixAbsoluteURL(value);
  }
  function prefixStyleAssetURLs(value) {
    if (typeof value !== 'string') return value;
    return value.replace(/url\((['"]?)\/assets\//g, 'url($1' + base + '/assets/');
  }
  function prefixHistoryURL(input) {
    if (typeof input === 'undefined' || input === null) return input;
    return prefixAbsoluteURL(input instanceof URL ? input.toString() : input);
  }
  function diagnosticFetchInfo(input, init) {
    try {
      var method = (init && init.method) || (input && input.method) || 'GET';
      var raw = input instanceof OriginalRequest ? input.url : input;
      var url = new URL(raw instanceof URL ? raw.toString() : String(raw), window.location.href);
      if (url.pathname.indexOf('/api/diagnostics/client') !== -1) return null;
      if (url.pathname.indexOf('/assets/') !== -1 || /\.(css|js|png|svg|ico|woff2?)$/.test(url.pathname)) return null;
      return { method: String(method).toUpperCase(), path: url.pathname + url.search };
    } catch (e) {
      return null;
    }
  }

  var OriginalRequest = window.Request;
  window.Request = function(input, init) {
    if (!(input instanceof OriginalRequest)) {
      input = prefixAbsoluteURL(input instanceof URL ? input.toString() : input);
    }
    return new OriginalRequest(input, init);
  };
  window.Request.prototype = OriginalRequest.prototype;
  try { Object.setPrototypeOf(window.Request, OriginalRequest); } catch (e) {}

  function patchURLProperty(proto, property, transform) {
    var descriptor = Object.getOwnPropertyDescriptor(proto, property);
    if (!descriptor || !descriptor.set || !descriptor.get) return;
    transform = transform || prefixElementURL;
    Object.defineProperty(proto, property, {
      configurable: descriptor.configurable,
      enumerable: descriptor.enumerable,
      get: descriptor.get,
      set: function(value) { return descriptor.set.call(this, transform(value)); }
    });
  }

  if (window.HTMLAnchorElement) patchURLProperty(HTMLAnchorElement.prototype, 'href', prefixAnchorURL);
  patchURLProperty(HTMLLinkElement.prototype, 'href');
  patchURLProperty(HTMLScriptElement.prototype, 'src');

  if (window.HTMLImageElement) patchURLProperty(HTMLImageElement.prototype, 'src');
  if (window.HTMLSourceElement) patchURLProperty(HTMLSourceElement.prototype, 'src');
  if (window.HTMLVideoElement) patchURLProperty(HTMLVideoElement.prototype, 'poster');

  var originalSetAttribute = Element.prototype.setAttribute;
  Element.prototype.setAttribute = function(name, value) {
    var lowerName = name.toLowerCase();
    if (this.tagName === 'A' && lowerName === 'href') {
      value = prefixAnchorURL(value);
    } else if ((this.tagName === 'LINK' && lowerName === 'href')
        || (this.tagName === 'SCRIPT' && lowerName === 'src')
        || (this.tagName === 'IMG' && lowerName === 'src')
        || (this.tagName === 'SOURCE' && lowerName === 'src')
        || (this.tagName === 'VIDEO' && lowerName === 'poster')
        || ((lowerName === 'href' || lowerName === 'xlink:href') && typeof value === 'string' && value.startsWith('/assets/'))) {
      value = prefixElementURL(value);
    }
    return originalSetAttribute.call(this, name, value);
  };

  if (window.CSSStyleSheet && CSSStyleSheet.prototype.insertRule) {
    var originalInsertRule = CSSStyleSheet.prototype.insertRule;
    CSSStyleSheet.prototype.insertRule = function(rule, index) {
      return originalInsertRule.call(this, prefixStyleAssetURLs(rule), index);
    };
  }
  if (window.CSSStyleDeclaration && CSSStyleDeclaration.prototype.setProperty) {
    var originalSetProperty = CSSStyleDeclaration.prototype.setProperty;
    CSSStyleDeclaration.prototype.setProperty = function(property, value, priority) {
      return originalSetProperty.call(this, property, prefixStyleAssetURLs(value), priority);
    };
  }
  if (window.HTMLStyleElement && window.Node) {
    var textContentDescriptor = Object.getOwnPropertyDescriptor(Node.prototype, 'textContent');
    if (textContentDescriptor && textContentDescriptor.set && textContentDescriptor.get) {
      Object.defineProperty(HTMLStyleElement.prototype, 'textContent', {
        configurable: true,
        enumerable: textContentDescriptor.enumerable,
        get: textContentDescriptor.get,
        set: function(value) { return textContentDescriptor.set.call(this, prefixStyleAssetURLs(value)); }
      });
    }
    var originalAppendChild = Node.prototype.appendChild;
    Node.prototype.appendChild = function(child) {
      if (this instanceof HTMLStyleElement && child && child.nodeType === Node.TEXT_NODE) {
        child.nodeValue = prefixStyleAssetURLs(child.nodeValue);
      }
      return originalAppendChild.call(this, child);
    };
  }

  if (navigator.clipboard && navigator.clipboard.writeText) {
    var originalWriteText = navigator.clipboard.writeText.bind(navigator.clipboard);
    navigator.clipboard.writeText = function(text) {
      try {
        window.parent.postMessage({ type: 'webmux-clipboard-write', text: String(text || '') }, window.location.origin);
      } catch (e) {}
      return originalWriteText(text).catch(function() { return undefined; });
    };
  }

  function decodeOpenCodeRouteDir(slug) {
    try {
      var binary = atob(String(slug).replace(/-/g, '+').replace(/_/g, '/'));
      var bytes = Uint8Array.from(binary, function(ch) { return ch.charCodeAt(0); });
      return new TextDecoder().decode(bytes);
    } catch (e) {
      return '';
    }
  }
  function currentOpenCodeRouteInfo(url) {
    try {
      var parsed = new URL(url || window.location.href, window.location.href);
      var path = parsed.pathname;
      if (path.startsWith(base + '/')) path = path.slice(base.length);
      var match = path.match(/^\/([^\/]+)\/session\/?([^\/?#]*)/);
      if (!match) return null;
      return {
        slug: match[1],
        directory: decodeOpenCodeRouteDir(match[1]),
        sessionID: match[2] || '',
        currentServerID: currentServerID,
        canonicalServerID: canonicalServerID,
        path: path
      };
    } catch (e) {
      return null;
    }
  }
  function postRouteDiagnostic(event, url) {
    var info = currentOpenCodeRouteInfo(url);
    if (info) postDiagnostic('opencode-route', event, { path: info.path, data: info });
  }
  function openCodePathFromURL(url) {
    try {
      var path = new URL(url || window.location.href, window.location.href).pathname;
      if (path === base) return '/';
      if (path.startsWith(base + '/')) return path.slice(base.length);
    } catch (e) {}
    return null;
  }
  function persistActiveOpenCodePath(url) {
    var path = openCodePathFromURL(url);
    if (!path) return;
    path = translateOpenCodeRoute(path, canonicalServerID);
    if (path === activeOpenCodePath) return;
    activeOpenCodePath = path;
    queueStorageUpdate({ operation: 'set', key: activePathStorageKey, value: path });
  }
  document.addEventListener('click', function(event) {
    var anchor = event.target && event.target.closest ? event.target.closest('a[href]') : null;
    if (!anchor) return;
    var href = anchor.getAttribute('href') || '';
    if (href.indexOf('/session') !== -1) postRouteDiagnostic('session-link-click', anchor.href);
  }, true);

  window.fetch = function(input, init) {
    if (!(input instanceof OriginalRequest)) {
      input = prefixAbsoluteURL(input);
    }
    var info = diagnosticFetchInfo(input, init);
    var startedAt = Date.now();
    if (info && info.method !== 'GET') postDiagnostic('opencode-fetch', 'start', { path: info.path, data: { method: info.method } });
    return originalFetch.call(this, input, init).then(function(response) {
      watchOpenCodeEventStream(response);
      if (info) {
        var age = Date.now() - startedAt;
        if (!response.ok || age > 2000 || info.method !== 'GET') {
          postDiagnostic('opencode-fetch', 'headers', { path: info.path, ageMs: age, data: { method: info.method, status: response.status, ok: response.ok } });
        }
      }
      return response;
    }).catch(function(err) {
      if (info) postDiagnostic('opencode-fetch', 'error', { path: info.path, ageMs: Date.now() - startedAt, data: { method: info.method, error: err && err.message } });
      throw err;
    });
  };

  function patchHistoryMethod(method) {
    var original = window.history && window.history[method];
    if (typeof original !== 'function') return;
    window.history[method] = function(state, title, url) {
      if (arguments.length > 2) arguments[2] = prefixHistoryURL(url);
      var result = original.apply(this, arguments);
      if (arguments.length > 2) {
        postRouteDiagnostic(method, arguments[2]);
        persistActiveOpenCodePath(arguments[2]);
      }
      return result;
    };
  }
  patchHistoryMethod('pushState');
  patchHistoryMethod('replaceState');
  window.addEventListener('popstate', function() { persistActiveOpenCodePath(window.location.href); });
  if (activePathNeedsReset) queueStorageUpdate({ operation: 'set', key: activePathStorageKey, value: activeOpenCodePath });
  persistActiveOpenCodePath(window.location.href);

  var OriginalEventSource = window.EventSource;
  window.EventSource = function(url, config) {
    var prefixed = prefixAbsoluteURL(url);
    var es = new OriginalEventSource(prefixed, config);
    try {
      es.addEventListener('open', function() { postDiagnostic('opencode-eventsource', 'open', { path: prefixed }); });
      es.addEventListener('error', function() { postDiagnostic('opencode-eventsource', 'error', { path: prefixed }); });
      es.addEventListener('message', function(event) {
        if (isOpenCodeAttentionRequest(event.data)) requestWebmuxAttention();
      });
      es.addEventListener('permission.asked', requestWebmuxAttention);
      es.addEventListener('question.asked', requestWebmuxAttention);
    } catch (e) {}
    return es;
  };
  window.EventSource.prototype = OriginalEventSource.prototype;

  var OriginalWorker = window.Worker;
  window.Worker = function(url, options) {
    return new OriginalWorker(prefixAbsoluteURL(url), options);
  };
  window.Worker.prototype = OriginalWorker.prototype;

  var OriginalWebSocket = window.WebSocket;
  window.WebSocket = function(url, protocols) {
    var startedAt = Date.now();
    var prefixed = prefixWebSocketURL(url);
    postDiagnostic('opencode-ws', 'construct', { path: prefixed });
    var ws = protocols ? new OriginalWebSocket(prefixed, protocols) : new OriginalWebSocket(prefixed);
    ws.addEventListener('open', function() { postDiagnostic('opencode-ws', 'open', { path: prefixed, ageMs: Date.now() - startedAt }); });
    ws.addEventListener('close', function(event) { postDiagnostic('opencode-ws', 'close', { path: prefixed, ageMs: Date.now() - startedAt, data: { code: event.code, reason: event.reason, clean: event.wasClean } }); });
    ws.addEventListener('error', function() { postDiagnostic('opencode-ws', 'error', { path: prefixed, ageMs: Date.now() - startedAt }); });
    return ws;
  };
  window.WebSocket.prototype = OriginalWebSocket.prototype;
  window.WebSocket.CONNECTING = OriginalWebSocket.CONNECTING;
  window.WebSocket.OPEN = OriginalWebSocket.OPEN;
  window.WebSocket.CLOSING = OriginalWebSocket.CLOSING;
  window.WebSocket.CLOSED = OriginalWebSocket.CLOSED;

  var originalOpen = XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open = function(method, url) {
    arguments[1] = prefixAbsoluteURL(url);
    return originalOpen.apply(this, arguments);
  };
})();
</script>`, paneID, backendID, string(storageJSON), string(diagnosticsJSON), storage.Version)

	return injectIntoHTMLHead(content, script)
}
