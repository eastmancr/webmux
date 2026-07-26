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

func injectPanePopoutBridge(content string) string {
	return injectIntoHTMLHead(content, panePopoutBridgeScript)
}

const panePopoutBridgeScript = `<script>
(function() {
  var match = window.location.pathname.match(/\/p\/([^\/]+)/);
  if (!match) return;

  var paneId = decodeURIComponent(match[1]);
  var channel = 'BroadcastChannel' in window ? new BroadcastChannel('webmux-popouts') : null;
  var invalidated = false;
  var trackedSockets = [];
  var OriginalWebSocket = window.WebSocket;
  var originalFetch = window.fetch;

  if (OriginalWebSocket) {
    window.WebSocket = function(url, protocols) {
      if (invalidated) throw new Error('webmux pane client is disabled');
      var socket = protocols === undefined ? new OriginalWebSocket(url) : new OriginalWebSocket(url, protocols);
      trackedSockets.push(socket);
      return socket;
    };
    window.WebSocket.prototype = OriginalWebSocket.prototype;
    Object.keys(OriginalWebSocket).forEach(function(key) {
      try { window.WebSocket[key] = OriginalWebSocket[key]; } catch (e) {}
    });
  }

  if (originalFetch) {
    window.fetch = function() {
      if (invalidated) return Promise.reject(new Error('webmux pane client is disabled'));
      return originalFetch.apply(this, arguments);
    };
  }

  if (!channel) return;

  channel.onmessage = function(event) {
    var msg = event.data;
    if (window.parent !== window) return;
    if (!msg || !msg.type) return;
    if (msg.type === 'webmux-popout-discover') {
      send('webmux-popout-alive');
      return;
    }
    if (msg.type === 'webmux-popout-close' && (msg.popoutId === popoutId || msg.paneId === paneId)) {
      window.close();
      setTimeout(function() { disablePaneClient('This pane has been returned to webmux.'); }, 100);
      return;
    }
    if (msg.type === 'webmux-popout-reload' && (msg.popoutId === popoutId || msg.paneId === paneId)) {
      window.location.reload();
      return;
    }
    if (msg.type === 'webmux-pane-owner-main' && msg.paneId === paneId && shouldAcceptMainOwnership(msg)) {
      disablePaneClient('This pane is active in the webmux workspace.');
      return;
    }
  };

  if (window.parent !== window) return;

  var popoutId = sessionStorage.getItem('webmux.popoutId');
  if (!popoutId) {
    popoutId = 'popout-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2);
    sessionStorage.setItem('webmux.popoutId', popoutId);
  }

  function send(type) {
    if (invalidated) return;
    channel.postMessage({
      type: type,
      paneId: paneId,
      popoutId: popoutId,
      windowName: window.name || '',
      href: window.location.href,
      lastSeen: Date.now()
    });
  }

  function disablePaneClient(message) {
    if (invalidated) return;
    invalidated = true;
    try { channel.close(); } catch (e) {}
    trackedSockets.forEach(function(socket) {
      try { socket.close(1000, 'webmux pane disabled'); } catch (e) {}
    });
    trackedSockets = [];
    try { window.stop(); } catch (e) {}
    try { document.title = 'Pane returned to webmux'; } catch (e) {}
    renderDisabledPane(message);
    requestAnimationFrame(function() { renderDisabledPane(message); });
    setTimeout(function() { renderDisabledPane(message); }, 50);
    setTimeout(function() { renderDisabledPane(message); }, 250);
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', function() { renderDisabledPane(message); }, { once: true });
    }
  }

  function renderDisabledPane(message) {
    try {
      var head = document.head || document.getElementsByTagName('head')[0] || document.documentElement;
      var style = document.getElementById('webmux-disabled-pane-style');
      if (!style) {
        style = document.createElement('style');
        style.id = 'webmux-disabled-pane-style';
        style.textContent = 'html,body{height:100%!important;margin:0!important;overflow:hidden!important;background:#1e1e2e!important;color:#cdd6f4!important}body{font:15px/1.5 system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif!important}.webmux-disabled-pane{position:fixed!important;inset:0!important;z-index:2147483647!important;display:grid!important;place-items:center!important;background:#1e1e2e!important;color:#cdd6f4!important;padding:24px!important;box-sizing:border-box!important}.webmux-disabled-pane-card{max-width:520px!important;padding:28px!important;border:1px solid #45475a!important;border-radius:18px!important;background:#181825!important;box-shadow:0 20px 60px rgba(0,0,0,.35)!important}.webmux-disabled-pane h1{margin:0 0 10px!important;font-size:22px!important;color:#f5e0dc!important}.webmux-disabled-pane p{margin:0!important;color:#bac2de!important}.webmux-disabled-pane .muted{margin-top:16px!important;color:#7f849c!important;font-size:13px!important}';
        head.appendChild(style);
      }
      var body = document.body;
      if (!body) {
        body = document.createElement('body');
        document.documentElement.appendChild(body);
      }
      body.innerHTML = '<main class="webmux-disabled-pane" role="status" aria-live="polite"><section class="webmux-disabled-pane-card"><h1>Pane returned to webmux</h1><p>' + escapeHTML(message || 'This pane client has been disabled.') + '</p><p class="muted">Use the pane inside the webmux workspace to continue.</p></section></main>';
    } catch (e) {}
  }

  function shouldAcceptMainOwnership(msg) {
    if (!msg.popoutId && !msg.windowName) return true;
    if (msg.windowName) return msg.windowName === (window.name || '');
    return msg.popoutId === popoutId;
  }

  function escapeHTML(value) {
    return String(value).replace(/[&<>"']/g, function(ch) {
      return ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[ch];
    });
  }

  send('webmux-popout-alive');
  setInterval(function() { send('webmux-popout-alive'); }, 1000);
  window.addEventListener('pagehide', function() { send('webmux-popout-closed'); });
})();
</script>`
