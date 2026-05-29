/* *
 * Webmux - a browser-based pane multiplexer
 * Copyright (C) 2026  Webmux contributors
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */
package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// SECTION: TERMINAL INPUT

// Limits for terminal input requests to prevent abuse.
const (
	maxKeysPerRequest  = 100   // Maximum number of keys/steps in a single request
	maxKeyNameLength   = 32    // Maximum length of a key name (e.g. "C-c", "Enter")
	maxTextStepLength  = 4096  // Maximum length of a text step
	maxTotalTextLength = 16384 // Maximum total text length across all steps
)

// validTmuxKeyNames contains known valid tmux key names.
// This is not exhaustive but covers common cases; unknown keys are validated by pattern.
var validTmuxKeyNames = map[string]bool{
	// Control keys
	"C-a": true, "C-b": true, "C-c": true, "C-d": true, "C-e": true, "C-f": true,
	"C-g": true, "C-h": true, "C-i": true, "C-j": true, "C-k": true, "C-l": true,
	"C-m": true, "C-n": true, "C-o": true, "C-p": true, "C-q": true, "C-r": true,
	"C-s": true, "C-t": true, "C-u": true, "C-v": true, "C-w": true, "C-x": true,
	"C-y": true, "C-z": true, "C-\\": true, "C-]": true, "C-^": true, "C-_": true,
	"C-@": true, "C-[": true,
	// Special keys
	"Enter": true, "Tab": true, "BTab": true, "Space": true, "BSpace": true,
	"Escape": true, "DC": true, "IC": true,
	"Up": true, "Down": true, "Left": true, "Right": true,
	"Home": true, "End": true, "PPage": true, "NPage": true,
	"F1": true, "F2": true, "F3": true, "F4": true, "F5": true, "F6": true,
	"F7": true, "F8": true, "F9": true, "F10": true, "F11": true, "F12": true,
	// Meta/Alt keys (M- prefix)
	"M-a": true, "M-b": true, "M-c": true, "M-d": true, "M-e": true, "M-f": true,
	"M-g": true, "M-h": true, "M-i": true, "M-j": true, "M-k": true, "M-l": true,
	"M-m": true, "M-n": true, "M-o": true, "M-p": true, "M-q": true, "M-r": true,
	"M-s": true, "M-t": true, "M-u": true, "M-v": true, "M-w": true, "M-x": true,
	"M-y": true, "M-z": true,
}

// isValidTmuxKeyName checks if a key name is valid for tmux send-keys.
func isValidTmuxKeyName(key string) bool {
	if key == "" || len(key) > maxKeyNameLength {
		return false
	}

	if validTmuxKeyNames[key] {
		return true
	}

	// Allow single printable ASCII characters for direct key input.
	if len(key) == 1 && key[0] >= 0x20 && key[0] <= 0x7E {
		return true
	}

	// Allow common tmux key-combo characters while rejecting shell metacharacters.
	for _, r := range key {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') &&
			!(r >= '0' && r <= '9') && r != '-' && r != '_' &&
			r != '[' && r != ']' && r != '\\' && r != '^' && r != '@' {
			return false
		}
	}

	return true
}

// SendInput sends input sequences to a terminal pane's tmux session.
func (tr *TerminalRuntime) SendInput(id string, req *PaneInputRequest) error {
	state, ok := tr.getState(id)
	if !ok {
		return fmt.Errorf("pane not found: %s", id)
	}
	tmuxSession := state.tmuxSession

	// Validate tmux session name format (defense in depth).
	if !strings.HasPrefix(tmuxSession, "mux-") || len(tmuxSession) > 15 {
		return fmt.Errorf("invalid tmux session name")
	}

	steps, err := terminalInputSteps(req)
	if err != nil {
		return err
	}

	tmuxSocket := tr.tmuxSocketPath()
	for _, step := range steps {
		var args []string

		switch step.Type {
		case "key":
			if step.Value == "" {
				continue
			}
			args = []string{"-S", tmuxSocket, "send-keys", "-t", tmuxSession, step.Value}
		case "text":
			if step.Value == "" {
				continue
			}
			args = []string{"-S", tmuxSocket, "send-keys", "-t", tmuxSession, "-l", step.Value}
		}

		cmd := exec.Command("tmux", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("tmux send-keys failed: %w: %s", err, string(out))
		}
	}

	return nil
}

func terminalInputSteps(req *PaneInputRequest) ([]PaneInputStep, error) {
	var steps []PaneInputStep

	if len(req.Sequence) > 0 {
		steps = req.Sequence
	} else if len(req.Keys) > 0 {
		for _, key := range req.Keys {
			steps = append(steps, PaneInputStep{Type: "key", Value: key})
		}
	} else {
		return nil, fmt.Errorf("no keys or sequence provided")
	}

	if len(steps) > maxKeysPerRequest {
		return nil, fmt.Errorf("too many steps: %d (max %d)", len(steps), maxKeysPerRequest)
	}

	totalTextLength := 0
	for i, step := range steps {
		switch step.Type {
		case "key":
			if !isValidTmuxKeyName(step.Value) {
				return nil, fmt.Errorf("invalid key name at step %d: %q", i, step.Value)
			}
		case "text":
			if len(step.Value) > maxTextStepLength {
				return nil, fmt.Errorf("text too long at step %d: %d bytes (max %d)", i, len(step.Value), maxTextStepLength)
			}
			totalTextLength += len(step.Value)
			if totalTextLength > maxTotalTextLength {
				return nil, fmt.Errorf("total text length exceeds limit: %d bytes (max %d)", totalTextLength, maxTotalTextLength)
			}
		default:
			return nil, fmt.Errorf("invalid step type at step %d: %q", i, step.Type)
		}
	}

	return steps, nil
}
