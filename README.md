# webmux

Browser-based terminal multiplexer. Go backend proxies to per-session ttyd instances with tmux for persistence.

## Requirements

- Go 1.21+
- [ttyd](https://github.com/tsl0922/ttyd)
- [tmux](https://github.com/tmux/tmux)

## Build

```sh
make        # production build (embeds static/)
make dev    # dev build (serves from disk with live reload)
make check  # verify compilation without producing binaries
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
wm ls                    # list sessions  
wm new [name]            # create session
wm close <id>            # close session
wm rename <id> <name>    # rename session
wm upload <file>...      # upload files
wm scratch [text]        # get/set scratch pad
wm scratch -             # send stdin to scratch pad
wm scratch clear         # clear scratch pad
wm mark                  # list marked files
wm mark <file|dir>...    # mark files/directories for download
wm mark unmark <path>    # unmark a file/directory
wm mark clear            # clear all marked files
```

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
- Clipboard integration via OSC 52
- Keyboard shortcuts (Ctrl+Shift+T for new session, etc.)

## Files

Settings and data follow XDG conventions:

| Path | Description |
|------|-------------|
| `~/.config/webmux/settings.json` | UI and terminal color settings |
| `~/.local/share/webmux/uploads` | Default upload directory |
| `$XDG_RUNTIME_DIR/webmux-tmux.sock` | Tmux socket |

## License

GPLv3
