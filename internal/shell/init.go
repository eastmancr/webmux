/*
 * webmux - Browser-based terminal multiplexer
 * Copyright (C) 2025  Webmux contributors
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 */

// Package shell provides shell initialization scripts for webmux terminals.
package shell

import "fmt"

// InitScript generates the shell initialization script that defines the wm
// wrapper function and shell completions. If binDir is non-empty, it also
// adds binDir to PATH (for clipboard wrapper scripts).
func InitScript(wmPath, binDir string) string {
	script := fmt.Sprintf(`# webmux shell init
_wm_bin=%q
wm() {
  "$_wm_bin" "$@"
}
`, wmPath)

	// Add PATH export if binDir provided (server-side init)
	if binDir != "" {
		script += fmt.Sprintf(`# Add webmux bin dir to PATH for wl-copy/wl-paste wrappers
export PATH=%q:"$PATH"
`, binDir)
	}

	// Shell completions for bash and zsh
	script += `
# Shell completions (bash and zsh)
if [ -n "$BASH_VERSION" ]; then
  _wm_completions() {
    local cur prev words cword
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"

    # Top-level commands
    local commands="info ls list new close rename upload scratch mark init copy c paste p v help"

    case "$prev" in
      wm)
        COMPREPLY=($(compgen -W "$commands" -- "$cur"))
        return 0
        ;;
      scratch)
        COMPREPLY=($(compgen -W "get clear" -- "$cur"))
        return 0
        ;;
      mark)
        # mark can take: clear, unmark, or files
        COMPREPLY=($(compgen -W "clear unmark" -- "$cur"))
        COMPREPLY+=($(compgen -f -- "$cur"))
        return 0
        ;;
      unmark)
        # unmark takes files
        COMPREPLY=($(compgen -f -- "$cur"))
        return 0
        ;;
      upload)
        COMPREPLY=($(compgen -f -- "$cur"))
        return 0
        ;;
      *)
        # For other positions, check the command
        if [ "${COMP_WORDS[1]}" = "upload" ] || [ "${COMP_WORDS[1]}" = "mark" ]; then
          COMPREPLY=($(compgen -f -- "$cur"))
          return 0
        fi
        ;;
    esac
  }
  complete -F _wm_completions wm
elif [ -n "$ZSH_VERSION" ]; then
  _wm_completions() {
    local -a commands subcmds
    commands=(
      'info:Show server info'
      'ls:List all sessions'
      'list:List all sessions'
      'new:Create a new session'
      'close:Close a session'
      'rename:Rename a session'
      'upload:Upload files to the server'
      'scratch:Get or set scratch pad text'
      'mark:Mark files for download'
      'init:Output shell init code'
      'copy:Copy text to browser clipboard'
      'c:Copy text to browser clipboard'
      'paste:Paste from browser clipboard'
      'p:Paste from browser clipboard'
      'v:Paste from browser clipboard'
      'help:Show help'
    )

    if (( CURRENT == 2 )); then
      _describe 'command' commands
    else
      case "${words[2]}" in
        scratch)
          subcmds=('get:Get scratch pad content' 'clear:Clear scratch pad')
          _describe 'subcommand' subcmds
          ;;
        mark)
          if [[ "${words[3]}" == "unmark" ]]; then
            _files
          else
            subcmds=('clear:Clear all marked files' 'unmark:Unmark a file')
            _describe 'subcommand' subcmds
            _files
          fi
          ;;
        upload)
          _files
          ;;
      esac
    fi
  }
  compdef _wm_completions wm
fi
`
	return script
}
