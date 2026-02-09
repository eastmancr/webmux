# webmux

Browser-based terminal multiplexer. Go backend proxies to per-session ttyd instances with tmux for persistence.

## Requirements

- Go 1.25+
- [ttyd](https://github.com/tsl0922/ttyd)
- [tmux](https://github.com/tmux/tmux)

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
| `-shell` | `$SHELL` or `/bin/bash` | Shell to spawn in terminals |
| `-upload-dir` | `~/.local/share/webmux/uploads` | Directory for uploaded files |

The optional `DIRECTORY` argument sets the starting directory for new terminal sessions.

## CLI Helper

Inside webmux terminals, use `wm` to interact with the server:

```sh
wm info                  # show server info
wm ls                    # list sessions (alias: wm list)
wm new [name]            # create session
wm close <id>            # close session
wm rename <id> <name>    # rename session
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

`wm copy` updates a server-side clipboard that browser tabs poll and sync to the system clipboard (permission required).
`wm paste` returns the server-side clipboard; to paste your system clipboard into a terminal, use Ctrl+Shift+V.

In webmux terminals, wrapper scripts for `wl-copy`, `wl-paste`, `xclip`, `xsel`, `pbcopy`, and `pbpaste` call
`wm copy`/`wm paste` so TUI tools work without extra configuration.

To run `wm` outside a webmux terminal, set `WEBMUX_HOST=host:port` (or `WEBMUX_PORT`) to point it at the server.

## Features

- Multiple terminal sessions with persistent tmux backing
- Session management (create, rename, close)
- Split panes (2, 3, or 4 terminals per group)
- Drag-and-drop session reordering and grouping
- File browser with:
  - Mark files and directories for bulk download
  - Single file direct download
  - Directory download as zip
  - File info popup with copy path and send to scratch pad
- File upload via drag-and-drop or file picker
- Scratch pad for CLI-browser text exchange
- Customizable UI and terminal colors (Base24 theme support)
- Clipboard sync with OSC 52 support plus `wm copy`/`wm paste`
- Keyboard shortcuts (Ctrl+Shift+T for new session, etc.)

## Files

Settings and data follow XDG conventions:

| Path | Description |
|------|-------------|
| `$XDG_CONFIG_HOME/webmux/settings.json` | UI and terminal color settings (defaults to `~/.config`) |
| `$XDG_DATA_HOME/webmux/uploads` | Default upload directory (defaults to `~/.local/share`) |
| `$XDG_DATA_HOME/webmux/tmux.sock` | Tmux socket (defaults to `~/.local/share`) |

## License

GPLv3
