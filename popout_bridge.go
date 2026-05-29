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
    channel.postMessage({
      type: type,
      paneId: paneId,
      popoutId: popoutId,
      href: window.location.href,
      lastSeen: Date.now()
    });
  }

  send('webmux-popout-alive');
  setInterval(function() { send('webmux-popout-alive'); }, 1000);
  window.addEventListener('pagehide', function() { send('webmux-popout-closed'); });
})();
</script>`
