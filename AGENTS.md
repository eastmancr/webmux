# Webmux

Browser-based pane multiplexer. The Go backend manages local pane backends and proxies them into a shared browser workspace.

## AI Agent Navigation

This codebase uses section comment markers to help AI agents quickly locate relevant code. Use these grep patterns to find specific functionality:

### Go Files (main.go, cmd/wm/main.go)
```bash
# Logging infrastructure
grep -n "SECTION: LOGGING" *.go

# Core data structures and types
grep -n "SECTION: TYPES" *.go cmd/wm/main.go

# HTTP API endpoints and handlers
grep -n "SECTION: API" *.go

# Pane management and lifecycle
grep -n "SECTION: PANES" *.go

# Server initialization and main
grep -n "SECTION: SERVER" *.go

# Settings and configuration
grep -n "SECTION: SETTINGS" *.go

# Clipboard (WebSocket-notified sync, server-side storage)
grep -n "SECTION: CLIPBOARD" *.go

# File operations (upload/download)
grep -n "SECTION: FILES" *.go

# WebSocket proxy and terminal handling
grep -n "SECTION: TERMINAL" *.go

# OpenCode managed backend and proxying
grep -n "SECTION: OPENCODE" *.go

# CLI commands and helpers
grep -n "SECTION: CLI" cmd/wm/main.go
```

### JavaScript Files (static/app.js)
```bash
# Core class and initialization
grep -n "SECTION: CORE" static/app.js

# Mobile UI and touch handling
grep -n "SECTION: MOBILE" static/app.js

# Pane and group management
grep -n "SECTION: PANES" static/app.js

# Sidebar UI and interactions
grep -n "SECTION: SIDEBAR" static/app.js

# File browser and marked files
grep -n "SECTION: FILES" static/app.js

# Settings and configuration UI
grep -n "SECTION: SETTINGS" static/app.js

# Server communication and API calls
grep -n "SECTION: API" static/app.js

# Event handling and bindings
grep -n "SECTION: EVENTS" static/app.js
```

### Search Strategy
1. **Start with SECTION markers** - Use the appropriate grep pattern above to jump to the relevant section
2. **Narrow with subsection markers** - Look for `SUBSECTION:` comments within each section
3. **Use function/method names** - Once in the right section, look for descriptive function names
4. **Check related files** - Some functionality spans multiple files (e.g., API has both Go handlers and JS calls)

### Example Workflow
```bash
# Want to modify pane creation?
grep -n "SECTION: PANES" *.go             # Find pane-related Go code
grep -n "CreatePane\|handlePanes" *.go    # Look for creation functions/routes

# Want to modify mobile UI?
grep -n "SECTION: MOBILE" static/app.js   # Jump to mobile section
grep -n "mobileMode\|Mobile" static/app.js # Find mobile-specific code
```

## Structure
- `main.go` - HTTP server, top-level API routes, settings, files, clipboard
- `dev.go` / `nodev.go` - Build tags for dev mode (live reload) vs production (embedded)
- `cmd/wm/main.go` - CLI helper for terminal-to-browser interaction
- `internal/shell/init.go` - Shell initialization scripts for webmux terminals
- `panes.go` - Generic pane model, manager, UI state types
- `terminal_runtime.go` / `terminal_ws.go` / `terminal_proxy.go` / `terminal_input.go` - Terminal pane runtime, WebSocket/PTTY transport, OSC 52 handling, tmux input
- `opencode_runtime.go` / `opencode_proxy.go` - Managed OpenCode shared backend and URL/storage proxy shims
- `proxy.go` / `proxy_helpers.go` - Generic pane HTTP/WebSocket proxy pipeline
- `static/` - Frontend SPA (vanilla JS, no framework)
  - `app.js` - Single class `TerminalMultiplexer` managing all UI state
  - `index.html` - Modals and layout structure
  - `style.css` - CSS variables for theming, Catppuccin-inspired defaults
  - `tmux.conf` - Injected into each terminal pane
  - `wm` - Built CLI binary (embedded in production builds)

## Clipboard
Clipboard sync uses WebSocket notifications (`/api/clipboard/events`) instead of SSE or polling.

## Style
- Go: Standard library preferred, minimal dependencies
- JS: Vanilla ES6+, no build step, single-class architecture
- CSS: CSS variables for colors, BEM-ish naming
- No emoji in code or UI unless user requests it

## Versioning

Webmux uses Semantic Versioning and is currently pre-1.0.

- `PKGBUILD` `pkgver` is the canonical version. Use the full `MAJOR.MINOR.PATCH` form and keep the version in `webmux.1` synchronized. Reset `pkgrel` to `1` when `pkgver` changes.
- Recommend a patch bump for backward-compatible bug fixes, security hardening, and reliability improvements.
- Recommend a minor bump for user-facing features, new CLI/API/configuration capabilities, meaningful behavior changes, or breaking changes while the project remains pre-1.0.
- Tests, refactors, documentation, development tooling, and cosmetic-only changes do not normally require a version bump.
- Judge the accumulated changes since the last release rather than bumping once per commit or feature. If an unreleased version is already being prepared, include additional changes in that release unless they justify changing its intended scope.
- Proactively mention when a patch or minor bump appears advisable, but do not modify version metadata unless the task includes release preparation or the user requests it.

## Build
```
make build     # production (embeds static/ including wm binary)
make dev       # dev mode (serves from disk, copies wm to project root)
make check     # verify compilation
make smoke-dev # isolated dev server and browser smoke test
make clean     # remove built binaries
```

## Verification
- Run `make check` for code changes.
- Every non-cosmetic change must also be verified with `make smoke-dev` before completion. This exercises a real development server, terminal pane, and headless browser using isolated temporary state and ports.
- Documentation, comments, and purely visual styling changes may skip `make smoke-dev` when they cannot affect loading or interaction behavior.
