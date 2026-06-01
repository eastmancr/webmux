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
	"fmt"
	"log"
	"net/http"
	"strings"
)

// SECTION: TERMINAL PROXY

func (tr *TerminalRuntime) modifyTtydIndexResponse(resp *http.Response, diagnostics DiagnosticsSettings) error {
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		return nil
	}

	body, err := readProxyResponseBody(resp)
	if err != nil {
		return err
	}

	// Inject WebSocket/clipboard fix at start of <head> (must run before ttyd's JS).
	bodyStr := string(body)
	bodyLower := strings.ToLower(bodyStr)
	headIdx := strings.Index(bodyLower, "<head>")
	var content string
	if headIdx != -1 {
		content = bodyStr[:headIdx] + ttydHeadScript(diagnostics) + bodyStr[headIdx+6:]
		log.Printf("[inject] Script injected into ttyd HTML at offset %d (%d -> %d bytes)", headIdx, len(body), len(content))
	} else {
		htmlIdx := strings.Index(bodyLower, "<html")
		if htmlIdx != -1 {
			closeIdx := strings.Index(bodyStr[htmlIdx:], ">")
			if closeIdx != -1 {
				insertAt := htmlIdx + closeIdx + 1
				content = bodyStr[:insertAt] + ttydHeadScript(diagnostics) + bodyStr[insertAt:]
				log.Printf("[inject] Script injected after <html> tag at offset %d (%d -> %d bytes)", insertAt, len(body), len(content))
			}
		}
		if content == "" {
			content = bodyStr
			log.Printf("[inject] WARNING: no injection point found in ttyd HTML (first 200 bytes: %q)", bodyStr[:min(200, len(bodyStr))])
		}
	}
	content = injectPanePopoutBridge(content)

	writeProxyResponseBody(resp, content)
	resp.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	resp.Header.Set("Pragma", "no-cache")
	resp.Header.Set("Expires", "0")
	resp.Header.Set("Permissions-Policy", "clipboard-read=*, clipboard-write=*")

	return nil
}

// ttydHeadScript is injected at the START of <head> to intercept WebSocket before ttyd loads
// This MUST run before any other scripts to properly intercept WebSocket connections
func ttydHeadScript(diagnostics DiagnosticsSettings) string {
	diagnosticsJSON, err := json.Marshal(diagnostics)
	if err != nil {
		diagnosticsJSON = []byte("{}")
	}
	return fmt.Sprintf(`<head><script>
// WebSocket proxy fix - must run before ttyd's JavaScript
(function() {
    var diagnostics = %s;
    var markerMatch = window.location.pathname.match(/\/p\/([^\/]+)/);
    var paneID = markerMatch ? markerMatch[1] : '';
    var diagnosticsBase = window.location.pathname.replace(/\/p\/[^\/]+.*$/, '/api/diagnostics/client');
    function diagnosticsEnabled() { return diagnostics && diagnostics.enabled && diagnostics.clientEvents; }
    function postDiagnostic(source, event, details) {
        if (!diagnosticsEnabled()) return;
        details = details || {};
        var payload = JSON.stringify([{
            source: source,
            event: event,
            paneId: paneID,
            paneType: 'terminal',
            path: details.path || '',
            ageMs: details.ageMs || 0,
            data: details.data || {}
        }]);
        try {
            if (navigator.sendBeacon && navigator.sendBeacon(diagnosticsBase, new Blob([payload], { type: 'application/json' }))) return;
        } catch (e) {}
        try { fetch(diagnosticsBase, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: payload, keepalive: true }).catch(function() {}); } catch (e) {}
    }
    var OrigWebSocket = window.WebSocket;
    window.WebSocket = function(url, protocols) {
        var startedAt = Date.now();
        var originalUrl = url;
        // Rewrite WebSocket URLs that point to localhost/127.0.0.1 or use a different host
        // than the current page (which happens when accessed through a reverse proxy)
        var currentHost = window.location.host;
        var urlMatch = url.match(/^(wss?):\/\/([^\/]+)(.*)/);
        if (urlMatch) {
            var wsProtocol = urlMatch[1];
            var wsHost = urlMatch[2];
            var wsPath = urlMatch[3];
            // Rewrite if: targeting localhost/127.0.0.1, OR if the host doesn't match current page
            // (the latter catches cases where ttyd generates URLs with internal hostnames)
            if (wsHost.match(/^(localhost|127\.0\.0\.1)(:\d+)?$/) || wsHost !== currentHost) {
                var pagePath = window.location.pathname.replace(/\/$/, '');
                var protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
                url = protocol + '//' + currentHost + pagePath + wsPath;
                console.log('[webmux] Rewriting WebSocket URL:', originalUrl, '->', url);
            }
        }
        postDiagnostic('terminal-ws', 'construct', { path: url });
        var ws = protocols ? new OrigWebSocket(url, protocols) : new OrigWebSocket(url);
        ws.addEventListener('open', function() { postDiagnostic('terminal-ws', 'open', { path: url, ageMs: Date.now() - startedAt }); });
        ws.addEventListener('close', function(event) { postDiagnostic('terminal-ws', 'close', { path: url, ageMs: Date.now() - startedAt, data: { code: event.code, reason: event.reason, clean: event.wasClean } }); });
        ws.addEventListener('error', function() { postDiagnostic('terminal-ws', 'error', { path: url, ageMs: Date.now() - startedAt }); });
        setTimeout(function() {
            if (ws.readyState === OrigWebSocket.CONNECTING) postDiagnostic('terminal-ws', 'still-connecting', { path: url, ageMs: Date.now() - startedAt });
        }, 10000);
        return ws;
    };
    window.WebSocket.prototype = OrigWebSocket.prototype;
    window.WebSocket.CONNECTING = OrigWebSocket.CONNECTING;
    window.WebSocket.OPEN = OrigWebSocket.OPEN;
    window.WebSocket.CLOSING = OrigWebSocket.CLOSING;
    window.WebSocket.CLOSED = OrigWebSocket.CLOSED;
    console.log('[webmux] WebSocket interceptor installed');

    // Clipboard bridge - parent delegates clipboard API calls to this iframe
    // because the Clipboard API requires the calling document to have focus,
    // and the terminal iframe holds focus during normal use
    var hasClipboardAPI = !!(navigator.clipboard && navigator.clipboard.writeText);

    // execCommand fallback for clipboard write
    function execCommandCopy(text) {
        var ta = document.createElement('textarea');
        ta.value = text;
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        var ok = false;
        try { ok = document.execCommand('copy'); } catch(e) {}
        document.body.removeChild(ta);
        return ok;
    }

    window.addEventListener('message', function(e) {
        if (e.source !== window.parent) return;
        var msg = e.data;
        if (!msg || !msg.type) return;

        if (msg.type === 'clipboard-write') {
            if (!document.hasFocus()) return;
            if (hasClipboardAPI) {
                navigator.clipboard.writeText(msg.text).then(function() {
                    window.parent.postMessage({ type: 'clipboard-write-result', success: true }, '*');
                }).catch(function() {
                    var ok = execCommandCopy(msg.text);
                    window.parent.postMessage({ type: 'clipboard-write-result', success: ok, fallback: true }, '*');
                });
            } else {
                var ok = execCommandCopy(msg.text);
                window.parent.postMessage({ type: 'clipboard-write-result', success: ok, fallback: true }, '*');
            }
        }
    });
})();
</script>`, string(diagnosticsJSON))
}

// osc52Scanner scans a byte stream for OSC 52 clipboard escape sequences.
// It buffers partial sequences across multiple Scan() calls and extracts
// clipboard content when complete sequences are found.
//
// Supported formats:
//   - Direct OSC 52: \x1b]52;c;<base64>\x07 (BEL terminator)
//   - Direct OSC 52: \x1b]52;c;<base64>\x1b\\ (ST terminator)
//   - Tmux passthrough: \x1bPtmux;\x1b\x1b]52;c;<base64>\x07\x1b\\
type osc52Scanner struct {
	server *Server
	buf    []byte
}

const (
	// Maximum size of the scanner buffer (64KB should handle any reasonable clipboard)
	osc52MaxBufSize = 64 * 1024
	// Maximum decoded clipboard size we'll accept (10MB)
	osc52MaxClipboardSize = 10 * 1024 * 1024
)

func newOSC52Scanner(s *Server) *osc52Scanner {
	return &osc52Scanner{server: s}
}

// ObserveBackendToClient processes incoming data looking for OSC 52 sequences.
// Found clipboard content is broadcast to connected clients.
func (o *osc52Scanner) ObserveBackendToClient(data []byte) {
	// Append new data to buffer
	o.buf = append(o.buf, data...)

	// Process all complete sequences in buffer
	for {
		clipboardText, remaining, found := o.extractOSC52(o.buf)
		if !found {
			break
		}

		if clipboardText != "" && len(clipboardText) <= osc52MaxClipboardSize {
			o.server.setClipboard(clipboardText)
		}

		o.buf = remaining
	}

	// Prevent unbounded buffer growth by keeping only recent data
	// that might contain the start of an incomplete sequence
	if len(o.buf) > osc52MaxBufSize {
		o.buf = o.buf[len(o.buf)-osc52MaxBufSize:]
	}
}

// extractOSC52 finds and extracts the first complete OSC 52 sequence from data.
// Returns: clipboard text (empty if malformed), remaining data, and whether a sequence was found.
func (o *osc52Scanner) extractOSC52(data []byte) (clipboardText string, remaining []byte, found bool) {
	// Look for tmux passthrough first: \x1bPtmux;\x1b
	if idx := indexTmuxPassthrough(data); idx != -1 {
		text, rem, ok := o.extractTmuxPassthrough(data, idx)
		if ok {
			return text, rem, true
		}
		// Incomplete tmux sequence - keep data from idx onwards
		if idx > 0 {
			return "", data[idx:], false
		}
		return "", data, false
	}

	// Look for direct OSC 52: \x1b]52;
	idx := indexOSC52Start(data)
	if idx == -1 {
		// No OSC 52 start found - discard everything except last few bytes
		// (which might be the start of an escape sequence)
		if len(data) > 10 {
			return "", data[len(data)-10:], false
		}
		return "", data, false
	}

	// Find the terminator (BEL \x07 or ST \x1b\\)
	rest := data[idx:]
	endIdx := -1
	for i := 5; i < len(rest); i++ { // Start after "\x1b]52;"
		if rest[i] == 0x07 { // BEL terminator
			endIdx = i + 1
			break
		}
		if rest[i] == 0x1b && i+1 < len(rest) && rest[i+1] == '\\' { // ST terminator
			endIdx = i + 2
			break
		}
	}

	if endIdx == -1 {
		// No terminator yet - keep buffering from OSC start
		if idx > 0 {
			return "", data[idx:], false
		}
		return "", data, false
	}

	// Parse the complete sequence: \x1b]52;X;BASE64<term>
	seq := rest[:endIdx]
	clipboardText = o.parseOSC52Payload(seq)

	// Return remaining data after this sequence
	return clipboardText, data[idx+endIdx:], true
}

// extractTmuxPassthrough handles tmux DCS passthrough format:
// \x1bPtmux;\x1b\x1b]52;c;<base64>\x07\x1b\\
func (o *osc52Scanner) extractTmuxPassthrough(data []byte, idx int) (clipboardText string, remaining []byte, found bool) {
	rest := data[idx:]

	// Minimum: \x1bPtmux;\x1b\x1b]52;c;\x07\x1b\\ = ~20 bytes
	if len(rest) < 20 {
		return "", nil, false
	}

	// Find the ST terminator for the DCS: \x1b\\
	// The inner OSC 52 will have its own terminator (BEL or ST)
	endIdx := -1
	for i := 15; i < len(rest)-1; i++ {
		if rest[i] == 0x1b && rest[i+1] == '\\' {
			// Check if this is the outer DCS terminator (not inner ST)
			// Inner ST would be doubled: \x1b\x1b\\
			if i >= 2 && rest[i-1] == 0x1b {
				continue // This is escaped, keep looking
			}
			endIdx = i + 2
			break
		}
	}

	if endIdx == -1 {
		return "", nil, false
	}

	// Extract inner OSC 52 from tmux passthrough
	// Format: \x1bPtmux;\x1b<inner>\x1b\\
	// The inner content has doubled escapes
	inner := rest[8 : endIdx-2] // Skip "\x1bPtmux;\x1b" and trailing "\x1b\\"

	// Undouble the escapes in the inner content
	undoubled := undoubleEscapes(inner)

	// Now parse as regular OSC 52
	if len(undoubled) > 5 && undoubled[0] == 0x1b && undoubled[1] == ']' {
		clipboardText = o.parseOSC52Payload(undoubled)
	}

	return clipboardText, data[idx+endIdx:], true
}

// parseOSC52Payload extracts and decodes the base64 clipboard data from an OSC 52 sequence.
// Input format: \x1b]52;c;<base64><terminator>
func (o *osc52Scanner) parseOSC52Payload(seq []byte) string {
	// Find the second semicolon (after "52;X")
	secondSemi := -1
	for i := 5; i < len(seq)-1; i++ {
		if seq[i] == ';' {
			secondSemi = i
			break
		}
	}

	if secondSemi == -1 || secondSemi >= len(seq)-1 {
		return ""
	}

	// Determine terminator length
	termLen := 1 // BEL
	if len(seq) >= 2 && seq[len(seq)-2] == 0x1b {
		termLen = 2 // ST (\x1b\\)
	}

	// Extract base64 data
	if secondSemi+1 >= len(seq)-termLen {
		return ""
	}
	b64Data := seq[secondSemi+1 : len(seq)-termLen]

	if len(b64Data) == 0 {
		return ""
	}

	decoded, err := base64.StdEncoding.DecodeString(string(b64Data))
	if err != nil {
		return ""
	}

	return string(decoded)
}

// indexOSC52Start finds the start of an OSC 52 sequence (\x1b]52;)
func indexOSC52Start(data []byte) int {
	for i := 0; i <= len(data)-5; i++ {
		if data[i] == 0x1b && data[i+1] == ']' && data[i+2] == '5' && data[i+3] == '2' && data[i+4] == ';' {
			return i
		}
	}
	return -1
}

// indexTmuxPassthrough finds the start of a tmux DCS passthrough (\x1bPtmux;)
func indexTmuxPassthrough(data []byte) int {
	for i := 0; i <= len(data)-7; i++ {
		if data[i] == 0x1b && data[i+1] == 'P' &&
			data[i+2] == 't' && data[i+3] == 'm' && data[i+4] == 'u' && data[i+5] == 'x' && data[i+6] == ';' {
			return i
		}
	}
	return -1
}

// undoubleEscapes converts doubled escape characters back to single escapes.
// In tmux passthrough, ESC is represented as ESC ESC.
func undoubleEscapes(data []byte) []byte {
	result := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		if data[i] == 0x1b && i+1 < len(data) && data[i+1] == 0x1b {
			result = append(result, 0x1b)
			i++ // Skip the doubled escape
		} else {
			result = append(result, data[i])
		}
	}
	return result
}
