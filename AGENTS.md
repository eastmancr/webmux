# Webmux

Browser-based terminal multiplexer. Go backend proxies to per-session ttyd instances with tmux for persistence.

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
