# SSE Clipboard Architecture (Removed)

This documents the SSE-based clipboard synchronization that was removed in favor of polling. Preserved here for reference if real-time push is needed for a future feature.

## Why SSE Was Removed

Reverse proxies (nginx, caddy, etc.) buffer SSE responses by default, causing ~30 second delays before events reach the browser. The `X-Accel-Buffering: no` header did not resolve this in all proxy configurations. This made SSE unusable for clipboard synchronization where latency matters.

Additionally, `navigator.clipboard.readText()` requires both document focus and explicit `clipboard-read` permission. In the iframe architecture (where ttyd terminals run inside iframes), calling `readText()` from a postMessage handler consistently failed with "Read permission denied" and actively stole focus from the terminal, causing subsequent `writeText()` calls to fail as well.

## What Was Removed

### Server (main.go)
- `handleClipboardEvents` -- SSE endpoint at `/api/clipboard/events` that streamed clipboard updates and clipboard-request events to the browser
- `broadcastClipboard` -- sent clipboard content to all SSE subscribers
- `handleClipboardRequest` / `handleClipboardResponse` -- round-trip mechanism where `wm paste` asked the browser for its clipboard via SSE, browser read it with `navigator.clipboard.readText()`, and POSTed back
- `broadcastClipboardRequest` -- sent clipboard-read requests to SSE subscribers
- `clipboardClients` / `clipboardClientsMu` -- SSE subscriber tracking
- `clipboardRequests` / `clipboardRequestsMu` / `clipboardResponse` -- request/response correlation for paste

### Client (static/app.js)
- `EventSource` connection to `/api/clipboard/events`
- `handleClipboardRequest` -- responded to server clipboard-read requests by calling `navigator.clipboard.readText()` in the terminal iframe
- `readClipboardViaIframe` -- postMessage-based bridge to read clipboard from the focused iframe
- `initClipboardPermission` / `requestClipboardPermission` / `updateClipboardPermissionUI` -- clipboard-read permission tracking and UI

### CLI (cmd/wm/main.go)
- `cmdPaste` previously POSTed to `/api/clipboard/request` to trigger the browser round-trip; now uses `GET /api/clipboard` directly

## Current Architecture

### Copy (wl-copy / xclip / OSC 52 -> browser clipboard)
1. CLI shim or OSC 52 sets server-side clipboard via `POST /api/clipboard`
2. Server increments `clipboardVersion`
3. Browser polls `GET /api/clipboard/version` every 300ms
4. When version changes, browser fetches `GET /api/clipboard` and writes to system clipboard via `navigator.clipboard.writeText()` in the focused terminal iframe (postMessage bridge)

### Paste (wl-paste / xclip -o -> server clipboard)
1. CLI shim calls `GET /api/clipboard`
2. Server returns stored clipboard content directly

## If Real-Time Push Is Needed in the Future

Use **WebSocket** instead of SSE. WebSocket frames are forwarded immediately by reverse proxies (they upgrade the connection and proxy bidirectionally). The terminal proxy already demonstrates WebSocket handling in `proxyWebSocket`. A dedicated `/api/clipboard/ws` endpoint could replace both the polling and the removed SSE infrastructure.
