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
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// SECTION: OPENCODE PROXY

var sourceMapCommentRE = regexp.MustCompile(`(?m)\n?(//# sourceMappingURL=.*$|/\*# sourceMappingURL=.*\*/)`)

func (or *OpenCodeRuntime) modifyOpenCodeIndexResponse(paneID, backendID string, storage PaneStorageState, resp *http.Response) error {
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
	content = rewriteRootRelativeHTML(content)
	content = injectPanePopoutBridge(content)
	content = injectOpenCodeProxyScript(content, paneID, backendID, storage)

	writeProxyResponseBody(resp, content)
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Etag")
	resp.Header.Del("Content-Security-Policy")
	resp.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	resp.Header.Set("Pragma", "no-cache")
	resp.Header.Set("Expires", "0")

	return nil
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
	if isCSS {
		content = rewriteOpenCodeCSSAssetURLs(content)
	}
	content = sourceMapCommentRE.ReplaceAllString(content, "")

	writeProxyResponseBody(resp, content)
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Etag")

	return nil
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

func injectOpenCodeProxyScript(content, paneID, backendID string, storage PaneStorageState) string {
	storageJSON, err := json.Marshal(storage.Items)
	if err != nil {
		storageJSON = []byte("{}")
	}
	script := fmt.Sprintf(`<script>
(function() {
  var paneID = %q;
  var backendID = %q;
  var serverStorage = %s;
  var storageVersion = %d;
  var clientID = 'client-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2);
  var fallbackBase = '/p/' + paneID;
  var marker = '/p/' + paneID;
  var markerIndex = window.location.pathname.indexOf(marker);
  var base = markerIndex === -1 ? fallbackBase : window.location.pathname.slice(0, markerIndex + marker.length);
  var storageBase = base.replace(marker, '/api/pane-storage/' + encodeURIComponent(backendID));
  var storageEventsBase = base.replace(marker, '/api/pane-storage-events/' + encodeURIComponent(backendID));
  var originalFetch = window.fetch;
  var OriginalWebSocketForStorage = window.WebSocket;
  var storageSyncIdleDelay = 1000;
  var storageSyncMaxDelay = 5000;
  var pendingStorageClear = false;
  var pendingStorageSets = {};
  var pendingStorageRemoves = {};
  var pendingStorageStartedAt = 0;
  var storageSyncIdleTimer = null;
  var storageSyncMaxTimer = null;
  var storageFlushInFlight = false;
  var pendingSnapshotFetch = false;
  var opencodeServerStorageKey = 'opencode.global.dat:server';
  var canonicalServerID = 'webmux';
  var currentServerID = projectsKeyForServerID(window.location.origin);

  function sortedStorageKeys() {
    return Object.keys(serverStorage).sort();
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
    var oldStorage = serverStorage;
    serverStorage = normalizeOpenCodeStorageItems(nextItems || {});
    storageVersion = nextVersion || 0;
    var seen = {};
    Object.keys(oldStorage).forEach(function(key) {
      seen[key] = true;
      if (oldStorage[key] !== serverStorage[key]) {
        dispatchStorageChange(key, oldStorage[key], Object.prototype.hasOwnProperty.call(serverStorage, key) ? serverStorage[key] : null);
      }
    });
    Object.keys(serverStorage).forEach(function(key) {
      if (!seen[key]) {
        dispatchStorageChange(key, null, serverStorage[key]);
      }
    });
  }
  function postStorageUpdate(payload) {
    payload.clientId = clientID;
    return originalFetch(storageBase, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
      keepalive: true
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
    }
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
    return String(key) === opencodeServerStorageKey;
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
  function normalizeOpenCodeServerStorageValue(value) {
    if (typeof value !== 'string' || value === '') return value;
    try {
      var parsed = JSON.parse(value);
      if (!parsed || typeof parsed !== 'object') return value;
      var projects = parsed.projects && typeof parsed.projects === 'object' && !Array.isArray(parsed.projects) ? parsed.projects : {};
      var lastProject = parsed.lastProject && typeof parsed.lastProject === 'object' && !Array.isArray(parsed.lastProject) ? parsed.lastProject : {};
      var canonicalProjects = hasOwn(projects, currentServerID) ? projects[currentServerID]
        : hasOwn(projects, canonicalServerID) ? projects[canonicalServerID]
        : firstProjectList(projects);
      var canonicalLastProject = hasOwn(lastProject, currentServerID) ? lastProject[currentServerID]
        : hasOwn(lastProject, canonicalServerID) ? lastProject[canonicalServerID]
        : firstLastProject(lastProject);
      parsed.projects = {};
      if (Array.isArray(canonicalProjects)) parsed.projects[canonicalServerID] = canonicalProjects;
      parsed.lastProject = {};
      if (canonicalLastProject) parsed.lastProject[canonicalServerID] = canonicalLastProject;
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
      return JSON.stringify(parsed);
    } catch (e) {
      return value;
    }
  }
  function normalizeOpenCodeStorageItems(items) {
    if (!items || typeof items !== 'object') return {};
    if (Object.prototype.hasOwnProperty.call(items, opencodeServerStorageKey)) {
      items[opencodeServerStorageKey] = normalizeOpenCodeServerStorageValue(items[opencodeServerStorageKey]);
    }
    return items;
  }
  var originalOpenCodeServerStorageValue = Object.prototype.hasOwnProperty.call(serverStorage, opencodeServerStorageKey) ? serverStorage[opencodeServerStorageKey] : null;
  serverStorage = normalizeOpenCodeStorageItems(serverStorage);
  if (originalOpenCodeServerStorageValue !== null
      && serverStorage[opencodeServerStorageKey] !== originalOpenCodeServerStorageValue) {
    queueStorageUpdate({ operation: 'set', key: opencodeServerStorageKey, value: serverStorage[opencodeServerStorageKey] });
  }
  function flushStorageUpdates() {
    if (storageFlushInFlight) return;
    if (!hasPendingStorageUpdates()) {
      resetStorageSyncTimers();
      pendingStorageStartedAt = 0;
      return;
    }
    var operations = takePendingStorageOperations();
    if (operations.length === 0) return;
    storageFlushInFlight = true;
    postStorageUpdate({ operation: 'batch', operations: operations }).then(function() {
      storageFlushInFlight = false;
      if (hasPendingStorageUpdates()) {
        scheduleStorageFlush();
      } else if (pendingSnapshotFetch) {
        pendingSnapshotFetch = false;
        fetchStorageSnapshot();
      }
    }).catch(function() {
      storageFlushInFlight = false;
      operations.forEach(queueStorageUpdate);
    });
  }
  function fetchStorageSnapshot() {
    if (hasPendingStorageUpdates() || storageFlushInFlight) {
      pendingSnapshotFetch = true;
      return;
    }
    originalFetch(storageBase, { cache: 'no-store' }).then(function(response) {
      if (!response.ok) return null;
      return response.json();
    }).then(function(snapshot) {
      if (snapshot && snapshot.version > storageVersion) {
        replaceStorage(snapshot.items || {}, snapshot.version || 0);
      }
    }).catch(function() {});
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
      ws.onopen = function() {
        if (connectedOnce) fetchStorageSnapshot();
        connectedOnce = true;
      };
      ws.onmessage = function(event) {
        try {
          var message = JSON.parse(event.data);
          if (message && message.updatedBy === clientID) return;
          if (message && message.type === 'storage' && message.version > storageVersion) {
            fetchStorageSnapshot();
          }
        } catch (e) {}
      };
      ws.onclose = function() {
        if (!reconnectTimer) {
          reconnectTimer = setTimeout(function() {
            reconnectTimer = null;
            connect();
          }, reconnectDelay);
        }
      };
      ws.onerror = function() { ws.close(); };
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
      if (!Object.prototype.hasOwnProperty.call(serverStorage, key)) return null;
      return isOpenCodeServerStorageKey(key) ? materializeOpenCodeServerStorageValue(serverStorage[key]) : serverStorage[key];
    },
    setItem: function(key, value) {
      key = String(key);
      value = String(value);
      if (isOpenCodeServerStorageKey(key)) value = normalizeOpenCodeServerStorageValue(value);
      var oldValue = Object.prototype.hasOwnProperty.call(serverStorage, key) ? serverStorage[key] : null;
      if (oldValue === value) return;
      serverStorage[key] = value;
      dispatchStorageChange(key, oldValue, value);
      queueStorageUpdate({ operation: 'set', key: key, value: value });
    },
    removeItem: function(key) {
      key = String(key);
      if (!Object.prototype.hasOwnProperty.call(serverStorage, key)) return;
      var oldValue = serverStorage[key];
      delete serverStorage[key];
      dispatchStorageChange(key, oldValue, null);
      queueStorageUpdate({ operation: 'remove', key: key });
    },
    clear: function() {
      var oldStorage = serverStorage;
      serverStorage = {};
      Object.keys(oldStorage).forEach(function(key) { dispatchStorageChange(key, oldStorage[key], null); });
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
  connectStorageEvents();
  window.addEventListener('pagehide', flushStorageUpdates);

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
        window.parent.postMessage({ type: 'webmux-clipboard-write', text: String(text || '') }, '*');
      } catch (e) {}
      return originalWriteText(text).catch(function() { return undefined; });
    };
  }

  window.fetch = function(input, init) {
    if (!(input instanceof OriginalRequest)) {
      input = prefixAbsoluteURL(input);
    }
    return originalFetch.call(this, input, init);
  };

  function patchHistoryMethod(method) {
    var original = window.history && window.history[method];
    if (typeof original !== 'function') return;
    window.history[method] = function(state, title, url) {
      if (arguments.length > 2) arguments[2] = prefixHistoryURL(url);
      return original.apply(this, arguments);
    };
  }
  patchHistoryMethod('pushState');
  patchHistoryMethod('replaceState');

  var OriginalEventSource = window.EventSource;
  window.EventSource = function(url, config) {
    return new OriginalEventSource(prefixAbsoluteURL(url), config);
  };
  window.EventSource.prototype = OriginalEventSource.prototype;

  var OriginalWorker = window.Worker;
  window.Worker = function(url, options) {
    return new OriginalWorker(prefixAbsoluteURL(url), options);
  };
  window.Worker.prototype = OriginalWorker.prototype;

  var OriginalWebSocket = window.WebSocket;
  window.WebSocket = function(url, protocols) {
    url = prefixWebSocketURL(url);
    if (protocols) return new OriginalWebSocket(url, protocols);
    return new OriginalWebSocket(url);
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
</script>`, paneID, backendID, string(storageJSON), storage.Version)

	return injectIntoHTMLHead(content, script)
}
