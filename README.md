# webmux

Browser-based pane multiplexer. The Go backend manages local pane backends and proxies them into a shared browser workspace.

## Requirements

- Go 1.25+
- [tmux](https://github.com/tmux/tmux) (3.4+ built with SIXEL support for inline images)

## Build

```sh
make build  # production build (embeds static/)
make dev    # dev build (serves from disk with live reload)
make check  # verify compilation without producing binaries
make clean  # remove build artifacts
```

## Usage

```sh
webmux [OPTIONS] [DIRECTORY]
```

Then open `http://localhost:8080` in a browser.

## Options

| Flag | Default | Description |
|------|---------|-------------|
| `-port` | `8080` | HTTP server port |
| `-pane-port-start` | `7700` | Starting port for managed pane backends and pane IDs |
| `-shell` | `$SHELL` or `/bin/bash` | Shell to spawn in terminals |
| `-upload-dir` | `~/.local/share/webmux/uploads` | Directory for uploaded files |

The optional `DIRECTORY` argument sets the starting directory for new terminal panes.

## CLI Helper

Inside webmux terminals, use `wm` to interact with the server:

```sh
wm info                  # show server info
wm ls                    # list panes (alias: wm list)
wm new [--terminal|--opencode] [name]
                         # create pane (defaults to terminal)
wm close <id>            # close pane
wm rename <id> <name>    # rename pane
wm upload <file>...      # upload files
wm scratch               # get scratch pad
wm scratch [text]        # set scratch pad
wm scratch -             # send stdin to scratch pad
wm scratch clear         # clear and close scratch pad
wm mark                  # list marked files
wm mark <file|dir>...    # mark files/directories for download
wm mark unmark <path>    # unmark a file/directory
wm mark clear            # clear all marked files
wm copy [text]           # copy text to server clipboard (alias: wm c)
wm paste                 # paste server clipboard (aliases: wm p, wm v)
wm init                  # output shell init script (wm wrapper)
```

`wm copy` updates a server-side clipboard that browser tabs sync to the system clipboard through WebSocket notifications (permission required).
`wm paste` returns the server-side clipboard; to paste your system clipboard into a terminal, use Ctrl+Shift+V.

In webmux terminals, wrapper scripts for `wl-copy`, `wl-paste`, `xclip`, `xsel`, `pbcopy`, and `pbpaste` call
`wm copy`/`wm paste` so TUI tools work without extra configuration.

To run `wm` outside a webmux terminal, set `WEBMUX_HOST=host:port` (or `WEBMUX_PORT`) to point it at the server.

## Features

- Multiple terminal panes with persistent tmux backing
- Managed HTTP-backed pane types, including OpenCode when available
- Shared pane browser storage mirrored server-side for localStorage state
- Pane management (create, rename, refresh, close, pop out)
- Split panes (2, 3, or 4 panes per group)
- Drag-and-drop pane reordering and grouping
- File browser with:
  - Mark files and directories for bulk download
  - Single file direct download
  - Directory download as zip
  - File info popup with copy path and send to scratch pad
- File upload via drag-and-drop or file picker
- Scratch pad for CLI-browser text exchange
- Customizable UI and terminal colors (Base24 theme support)
- Clipboard sync with OSC 52 support plus `wm copy`/`wm paste`
- Mouse-aware TUI input and inline SIXEL images in terminal panes
- Keyboard shortcuts (Ctrl+Shift+T for new terminal pane, etc.)

## Pane Types

- Terminal panes are dedicated: each pane owns a tmux session, while xterm.js runs directly in the webmux page and connects through a webmux WebSocket. Keybar input is sent server-side through tmux.
- Terminal images use SIXEL through tmux. Images are limited to 4 megapixels and 8 MB of encoded data, with 32 MB of retained image storage per browser terminal. iTerm2 and Kitty image protocols are not supported. Convert regular images to SIXEL output before displaying them, for example: `magick image.png -resize '800x600>' sixel:-`.
- Hold Shift while dragging to select and automatically copy terminal text. Ctrl+Shift+C also copies the current selection.
- HTTP-backed pane types may be dedicated or shared depending on the backend. OpenCode is currently supported as a shared managed backend when `opencode` is available in `PATH`.
- Pane creation options are advertised by the server; unavailable optional backends are disabled in the UI.
- Popouts preserve the same dedicated/shared semantics. A popped-out shared backend suppresses duplicate in-page clients until it is popped back in or closed.

## Files

Settings and data follow XDG conventions:

| Path | Description |
|------|-------------|
| `$XDG_CONFIG_HOME/webmux/settings.json` | UI and terminal color settings (defaults to `~/.config`) |
| `$XDG_DATA_HOME/webmux/uploads` | Default upload directory (defaults to `~/.local/share`) |
| `$XDG_DATA_HOME/webmux/instances/port-<port>/tmux.sock` | Tmux socket, scoped by webmux server port (defaults to `~/.local/share`) |
| `$XDG_DATA_HOME/webmux/pane-storage/*.json` | Mirrored browser storage for shared HTTP-backed panes, including OpenCode |

## License

GPLv3
