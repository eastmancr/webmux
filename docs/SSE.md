# SSE Usage

Webmux still uses Server-Sent Events (SSE) for small server-to-browser update streams, such as scratch pad and marked-file changes. Clipboard notifications no longer use SSE; they use WebSocket notifications so idle clients do not need to poll.

This document records why the old clipboard-over-SSE design was removed and what remains true about SSE in Webmux today.

## Current SSE Requirements

SSE works well when each hop treats `text/event-stream` as a streaming response:

- forward response headers promptly
- do not buffer the response body
- do not synthesize `Content-Length` for open streams
- allow long-lived responses

Webmux SSE handlers set `Content-Type: text/event-stream`, `Cache-Control: no-cache`, and `X-Accel-Buffering: no`. Reverse proxies still need to honor streaming semantics for SSE to remain low-latency.

## Why SSE Was Removed

The removed clipboard design depended on low-latency SSE delivery for clipboard updates and browser clipboard-read requests. Some reverse-proxy configurations buffer SSE responses or impose whole-request timeouts, causing delayed or interrupted delivery. The `X-Accel-Buffering: no` header helps with compatible proxies, but it cannot fix every intermediary by itself.

That made SSE a poor fit for clipboard synchronization, where latency and reliability matter and users often access Webmux through different proxy stacks.

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
2. Server increments `clipboardVersion` and broadcasts the new version over `/api/clipboard/events` WebSocket connections
3. Browser fetches `GET /api/clipboard` only when the version changes
4. Browser writes the content to system clipboard via `navigator.clipboard.writeText()` in the focused terminal iframe (postMessage bridge)

### Paste (wl-paste / xclip -o -> server clipboard)
1. CLI shim calls `GET /api/clipboard`
2. Server returns stored clipboard content directly

## Real-Time Push

Clipboard change notifications use **WebSocket** instead of SSE. WebSocket frames are forwarded immediately by reverse proxies (they upgrade the connection and proxy bidirectionally), avoiding the SSE buffering problem described above without continuous polling.
