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
	"strings"
)

// SECTION: OPENCODE PROXY

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

func (or *OpenCodeRuntime) modifyOpenCodeAssetResponse(paneID string, resp *http.Response) error {
	if resp.StatusCode >= 400 {
		return nil
	}
	contentType := resp.Header.Get("Content-Type")
	requestPath := ""
	if resp.Request != nil && resp.Request.URL != nil {
		requestPath = resp.Request.URL.Path
	}
	if !strings.Contains(contentType, "javascript") && !strings.HasSuffix(requestPath, ".js") {
		return nil
	}

	body, err := readProxyResponseBody(resp)
	if err != nil {
		return err
	}

	basePath := "/p/" + paneID
	content := string(body)
	assetPath := strings.TrimPrefix(basePath, "/") + "/assets/"
	content = strings.ReplaceAll(content, `"/assets/`, `"`+basePath+`/assets/`)
	content = strings.ReplaceAll(content, `'/assets/`, `'`+basePath+`/assets/`)
	content = strings.ReplaceAll(content, "`/assets/", "`"+basePath+"/assets/")
	content = strings.ReplaceAll(content, `"assets/`, `"`+assetPath)
	content = strings.ReplaceAll(content, `'assets/`, `'`+assetPath)
	content = strings.ReplaceAll(content, "`assets/", "`"+assetPath)

	writeProxyResponseBody(resp, content)
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Etag")

	return nil
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
  var originalFetch = window.fetch;
  var originalLocalStorage;
  try { originalLocalStorage = window.localStorage; } catch (e) {}

  function shouldImportNativeKey(key) {
    return key && key !== 'multiplexer-ui-state' && key.indexOf('webmux.') !== 0;
  }
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
    serverStorage = nextItems || {};
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
    originalFetch(storageBase, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
      keepalive: true
    }).then(function(response) {
      if (!response.ok) return null;
      return response.json();
    }).then(function(snapshot) {
      if (snapshot && snapshot.version >= storageVersion) {
        replaceStorage(snapshot.items || {}, snapshot.version || 0);
      }
    }).catch(function() {});
  }
  function pollStorage() {
    originalFetch(storageBase, { cache: 'no-store' }).then(function(response) {
      if (!response.ok) return null;
      return response.json();
    }).then(function(snapshot) {
      if (snapshot && snapshot.version > storageVersion) {
        replaceStorage(snapshot.items || {}, snapshot.version || 0);
      }
    }).catch(function() {});
  }
  function seedStorageFromBrowser() {
    if (!originalLocalStorage || storageVersion !== 0 || Object.keys(serverStorage).length !== 0) return;
    var items = {};
    try {
      for (var i = 0; i < originalLocalStorage.length; i++) {
        var key = originalLocalStorage.key(i);
        if (shouldImportNativeKey(key)) items[key] = originalLocalStorage.getItem(key);
      }
    } catch (e) {}
    if (Object.keys(items).length === 0) return;
    serverStorage = Object.assign({}, items);
    postStorageUpdate({ operation: 'seed', items: items });
  }
  var storageShim = {
    get length() { return sortedStorageKeys().length; },
    key: function(index) {
      var keys = sortedStorageKeys();
      return keys[index] || null;
    },
    getItem: function(key) {
      key = String(key);
      return Object.prototype.hasOwnProperty.call(serverStorage, key) ? serverStorage[key] : null;
    },
    setItem: function(key, value) {
      key = String(key);
      value = String(value);
      var oldValue = Object.prototype.hasOwnProperty.call(serverStorage, key) ? serverStorage[key] : null;
      if (oldValue === value) return;
      serverStorage[key] = value;
      dispatchStorageChange(key, oldValue, value);
      postStorageUpdate({ operation: 'set', key: key, value: value });
    },
    removeItem: function(key) {
      key = String(key);
      if (!Object.prototype.hasOwnProperty.call(serverStorage, key)) return;
      var oldValue = serverStorage[key];
      delete serverStorage[key];
      dispatchStorageChange(key, oldValue, null);
      postStorageUpdate({ operation: 'remove', key: key });
    },
    clear: function() {
      var oldStorage = serverStorage;
      serverStorage = {};
      Object.keys(oldStorage).forEach(function(key) { dispatchStorageChange(key, oldStorage[key], null); });
      postStorageUpdate({ operation: 'clear' });
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
  seedStorageFromBrowser();
  setInterval(pollStorage, 1000);

  function prefixURL(input) {
    if (typeof input !== 'string') return input;
    if (input.startsWith(base + '/') || input === base) return input;
    if (input.startsWith(fallbackBase + '/')) return base + input.slice(fallbackBase.length);
    if (input.startsWith('/')) return base + input;
    return input;
  }
  function prefixAbsoluteURL(input) {
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

  function patchURLProperty(proto, property) {
    var descriptor = Object.getOwnPropertyDescriptor(proto, property);
    if (!descriptor || !descriptor.set || !descriptor.get) return;
    Object.defineProperty(proto, property, {
      configurable: descriptor.configurable,
      enumerable: descriptor.enumerable,
      get: descriptor.get,
      set: function(value) { return descriptor.set.call(this, prefixElementURL(value)); }
    });
  }

  patchURLProperty(HTMLLinkElement.prototype, 'href');
  patchURLProperty(HTMLScriptElement.prototype, 'src');

  var originalSetAttribute = Element.prototype.setAttribute;
  Element.prototype.setAttribute = function(name, value) {
    if ((this.tagName === 'LINK' && name.toLowerCase() === 'href') || (this.tagName === 'SCRIPT' && name.toLowerCase() === 'src')) {
      value = prefixElementURL(value);
    }
    return originalSetAttribute.call(this, name, value);
  };

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
    if (input instanceof Request) {
      input = new Request(prefixAbsoluteURL(input.url), input);
    } else {
      input = prefixAbsoluteURL(input);
    }
    return originalFetch.call(this, input, init);
  };

  var OriginalEventSource = window.EventSource;
  window.EventSource = function(url, config) {
    return new OriginalEventSource(prefixAbsoluteURL(url), config);
  };
  window.EventSource.prototype = OriginalEventSource.prototype;

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
