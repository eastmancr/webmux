/*
 * webmux - Browser-based pane multiplexer
 * Copyright (C) 2026  Webmux contributors
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 */

package shell

import "fmt"

// WrapperScript represents an executable helper script installed next to wm.
type WrapperScript struct {
	Name    string
	Content string
}

// ClipboardWrapperScripts generates clipboard compatibility wrappers that call wm.
func ClipboardWrapperScripts(wmPath string) []WrapperScript {
	return []WrapperScript{
		{
			Name: "wl-copy",
			Content: fmt.Sprintf(`#!/bin/sh
# webmux wl-copy wrapper - copies to browser clipboard via HTTP API
# Supports typed data through --type

type="text/plain"
while [ $# -gt 0 ]; do
  case "$1" in
    -n|--trim-newline|-p|--primary|-o|--paste-once|-f|--foreground|-c|--clear)
      shift ;;
    -t|--type)
      [ $# -ge 2 ] || { echo "wl-copy: $1 requires an argument" >&2; exit 1; }
      type="$2"; shift 2 ;;
    --type=*)
      type="${1#*=}"; shift ;;
    -s|--seat)
      shift 2 ;;
    --)
      shift; break ;;
    -*)
      shift ;;  # skip unknown flags
    *)
      break ;;
  esac
done

if [ $# -gt 0 ]; then
  # Text provided as arguments
  printf "%%s" "$*" | %q copy --type "$type"
else
  # Read from stdin
  %q copy --type "$type"
fi
`, wmPath, wmPath),
		},
		{
			Name: "wl-paste",
			Content: fmt.Sprintf(`#!/bin/sh
# webmux wl-paste wrapper - requests the focused browser clipboard

type=""
list_types=false
while [ $# -gt 0 ]; do
  case "$1" in
    -n|--no-newline|-p|--primary)
      shift ;;
    -l|--list-types)
      list_types=true; shift ;;
    -t|--type)
      [ $# -ge 2 ] || { echo "wl-paste: $1 requires an argument" >&2; exit 1; }
      type="$2"; shift 2 ;;
    --type=*)
      type="${1#*=}"; shift ;;
    -s|--seat)
      shift 2 ;;
    -w|--watch)
      # --watch is not supported, just exit
      echo "wl-paste --watch not supported in webmux" >&2
      exit 1 ;;
    --)
      shift; break ;;
    -*)
      shift ;;
    *)
      break ;;
  esac
done

if [ "$list_types" = true ]; then
  %q paste --request --list-types
elif [ -n "$type" ]; then
  %q paste --request --type "$type"
else
  %q paste --request --type text/plain
fi
`, wmPath, wmPath, wmPath),
		},
		{
			Name: "xclip",
			Content: fmt.Sprintf(`#!/bin/sh
# webmux xclip wrapper - copies/pastes to browser clipboard via HTTP API
# Supports: xclip -selection clipboard -i, xclip -selection clipboard -o

selection="clipboard"
mode="in"  # default is copy (input)
target="text/plain"

while [ $# -gt 0 ]; do
  case "$1" in
    -selection|-sel)
      shift
      selection="$1"
      shift ;;
    -i|-in)
      mode="in"
      shift ;;
    -o|-out)
      mode="out"
      shift ;;
    -target|-t)
      shift
      [ $# -gt 0 ] || { echo "xclip: target requires an argument" >&2; exit 1; }
      target="$1"
      shift ;;
    -d|-display|-loops|-l|-quiet|-q|-verbose|-v|-silent|-f|-r|-rmlastnl|-sensitive|-noutf8)
      shift ;;
    -*)
      shift ;;
    *)
      shift ;;
  esac
done

# Only handle clipboard selection (primary selection not supported via OSC 52)
case "$target" in
  UTF8_STRING|STRING|TEXT|text/plain\;charset=utf-8)
    target="text/plain" ;;
esac
if [ "$mode" = "out" ]; then
  if [ "$target" = "TARGETS" ]; then
    %q paste --request --list-types
  else
    %q paste --request --type "$target"
  fi
else
  %q copy --type "$target"
fi
`, wmPath, wmPath, wmPath),
		},
		{
			Name: "xsel",
			Content: fmt.Sprintf(`#!/bin/sh
# webmux xsel wrapper - copies/pastes to browser clipboard via HTTP API
# Supports: xsel -b -i, xsel -b -o, xsel --clipboard --input, etc.

mode="in"  # default is copy (input)

while [ $# -gt 0 ]; do
  case "$1" in
    -i|--input)
      mode="in"
      shift ;;
    -o|--output)
      mode="out"
      shift ;;
    -a|--append)
      mode="in"
      shift ;;
    -c|--clear)
      # Clear clipboard - just copy empty string
      echo -n "" | %q copy
      exit 0 ;;
    -b|--clipboard|-p|--primary|-s|--secondary)
      shift ;;  # ignore selection type (we only support clipboard)
    -d|--display|-t|--selectionTimeout|-l|--logfile|-n|--nodetach|-k|--keep|-x|--delete|-f|--follow|-z|--zeroflush|-v|--verbose)
      shift ;;
    -*)
      shift ;;
    *)
      shift ;;
  esac
done

if [ "$mode" = "out" ]; then
  %q paste --request --type text/plain
else
  %q copy
fi
`, wmPath, wmPath, wmPath),
		},
		{
			Name: "pbcopy",
			Content: fmt.Sprintf(`#!/bin/sh
# webmux pbcopy wrapper - copies to clipboard via HTTP API
%q copy
`, wmPath),
		},
		{
			Name: "pbpaste",
			Content: fmt.Sprintf(`#!/bin/sh
# webmux pbpaste wrapper - requests the focused browser clipboard
%q paste --request --type text/plain
`, wmPath),
		},
	}
}
