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
wm copy [-t type] [data] # copy typed data; reads stdin without data (alias: wm c)
wm paste                 # read the stored clipboard (aliases: wm p, wm v)
wm paste --request -t image/png
                         # request fresh typed data from the focused browser
wm init                  # output shell init script (wm wrapper)
```

`wm copy` updates the typed server-side clipboard. Text updates are synchronized to browser tabs when browser permissions allow it.
`wm paste --request` asks the focused browser for fresh clipboard data through a small paste prompt, which also works when enterprise browser policy denies programmatic clipboard reads.

In webmux terminals, wrapper scripts for `wl-copy`, `wl-paste`, `xclip`, `xsel`, `pbcopy`, and `pbpaste` call
`wm copy`/`wm paste` with MIME-aware reads so TUI tools can request images and other clipboard files without extra configuration.

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

## Backend persistence

Webmux preserves terminal tmux sessions and instance-lived backends such as OpenCode when the webmux process normally exits. On startup it adopts only backends whose persisted process identity still matches a live process. Closing the last OpenCode pane closes the view, but leaves the shared OpenCode backend available for a later pane.

Machine reboots do not recreate terminal panes, shells, commands, or working directories. If a persisted backend is no longer alive, its panes are discarded during startup. Use `-close-panes-on-exit` to close all backends on `SIGINT` or `SIGTERM`; `SIGQUIT` always requests this behavior. Webmux first asks each backend to exit, waits five seconds, and then force kills it. Recovery metadata is retained if termination cannot be confirmed.

For persistence under systemd, the service must use `KillMode=process`; otherwise systemd kills the tmux and OpenCode child processes along with webmux. State is scoped by webmux HTTP port, so changing `-port` selects a different workspace.

## Files

Settings and data follow XDG conventions:

| Path | Description |
|------|-------------|
| `$XDG_CONFIG_HOME/webmux/settings.json` | UI and terminal color settings (defaults to `~/.config`) |
| `$XDG_DATA_HOME/webmux/uploads` | Default upload directory (defaults to `~/.local/share`) |
| `$XDG_DATA_HOME/webmux/instances/port-<port>/tmux.sock` | Tmux socket, scoped by webmux server port (defaults to `~/.local/share`) |
| `$XDG_DATA_HOME/webmux/instances/port-<port>/state.json` | Pane, backend, and workspace layout recovery state |
| `$XDG_DATA_HOME/webmux/instances/port-<port>/scratch.txt` | Persisted scratch-pad text |
| `$XDG_DATA_HOME/webmux/instances/port-<port>/opencode.log` | Output from the persistent managed OpenCode backend |
| `$XDG_DATA_HOME/webmux/instances/port-<port>/runtime` | Stable terminal helper binaries and shell initialization files |
| `$XDG_DATA_HOME/webmux/pane-storage/*.json` | Mirrored browser storage for shared HTTP-backed panes, including OpenCode |

## License

GPLv3
