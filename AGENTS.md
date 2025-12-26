# Webmux

Browser-based terminal multiplexer. Go backend proxies to per-session ttyd instances with tmux for persistence.

## AI Agent Navigation

This codebase uses section comment markers to help AI agents quickly locate relevant code. Use these grep patterns to find specific functionality:

### Go Files (main.go, cmd/wm/main.go)
```bash
# Core data structures and types
grep -n "SECTION: TYPES" *.go cmd/wm/main.go

# HTTP API endpoints and handlers  
grep -n "SECTION: API" *.go

# Session management and lifecycle
grep -n "SECTION: SESSIONS" *.go

# Server initialization and main
grep -n "SECTION: SERVER" *.go

# Settings and configuration
grep -n "SECTION: SETTINGS" *.go

# File operations (upload/download)
grep -n "SECTION: FILES" *.go

# WebSocket proxy and terminal handling
grep -n "SECTION: TERMINAL" *.go

# CLI commands and helpers
grep -n "SECTION: CLI" cmd/wm/main.go
```

### JavaScript Files (static/app.js)
```bash
# Core class and initialization
grep -n "SECTION: CORE" static/app.js

# Mobile UI and touch handling
grep -n "SECTION: MOBILE" static/app.js

# Session and group management
grep -n "SECTION: SESSIONS" static/app.js

# Sidebar UI and interactions
grep -n "SECTION: SIDEBAR" static/app.js

# Terminal layout and display
grep -n "SECTION: TERMINAL" static/app.js

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
# Want to modify session creation?
grep -n "SECTION: SESSIONS" *.go          # Find session-related Go code
grep -n "createSession\|CreateSession" *.go  # Look for creation functions

# Want to modify mobile UI?
grep -n "SECTION: MOBILE" static/app.js   # Jump to mobile section
grep -n "mobileMode\|Mobile" static/app.js # Find mobile-specific code
```

## Structure
- `main.go` - HTTP server, session management, ttyd process lifecycle
- `dev.go` / `nodev.go` - Build tags for dev mode (live reload) vs production (embedded)
- `cmd/wm/main.go` - CLI helper for terminal-to-browser interaction
- `static/` - Frontend SPA (vanilla JS, no framework)
  - `app.js` - Single class `TerminalMultiplexer` managing all UI state
  - `index.html` - Modals and layout structure
  - `style.css` - CSS variables for theming, Catppuccin-inspired defaults
  - `tmux.conf` - Injected into each session
  - `wm` - Built CLI binary (embedded in production builds)

## Style
- Go: Standard library preferred, minimal dependencies
- JS: Vanilla ES6+, no build step, single-class architecture
- CSS: CSS variables for colors, BEM-ish naming
- No emoji in code or UI unless user requests it

## Build
```
make build   # production (embeds static/ including wm binary)
make dev     # dev mode (serves from disk, copies wm to project root)
make check   # verify compilation
make clean   # remove built binaries
```
