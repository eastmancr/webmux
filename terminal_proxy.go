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
	"encoding/base64"
)

// SECTION: TERMINAL CLIPBOARD

// osc52Scanner scans a byte stream for OSC 52 clipboard escape sequences.
// It buffers partial sequences across multiple Scan() calls and extracts
// clipboard content when complete sequences are found.
//
// Supported formats:
//   - Direct OSC 52: \x1b]52;c;<base64>\x07 (BEL terminator)
//   - Direct OSC 52: \x1b]52;c;<base64>\x1b\\ (ST terminator)
//   - Tmux passthrough: \x1bPtmux;\x1b\x1b]52;c;<base64>\x07\x1b\\
type osc52Scanner struct {
	server *Server
	buf    []byte
}

const (
	// Maximum size of the scanner buffer (64KB should handle any reasonable clipboard)
	osc52MaxBufSize = 64 * 1024
	// Maximum decoded clipboard size we'll accept (10MB)
	osc52MaxClipboardSize = 10 * 1024 * 1024
)

func newOSC52Scanner(s *Server) *osc52Scanner {
	return &osc52Scanner{server: s}
}

// ObserveBackendToClient processes incoming data looking for OSC 52 sequences.
// Found clipboard content is broadcast to connected clients.
func (o *osc52Scanner) ObserveBackendToClient(data []byte) {
	// Append new data to buffer
	o.buf = append(o.buf, data...)

	// Process all complete sequences in buffer
	for {
		clipboardText, remaining, found := o.extractOSC52(o.buf)
		if !found {
			break
		}

		if clipboardText != "" && len(clipboardText) <= osc52MaxClipboardSize {
			o.server.setClipboard(clipboardText)
		}

		o.buf = remaining
	}

	// Prevent unbounded buffer growth by keeping only recent data
	// that might contain the start of an incomplete sequence
	if len(o.buf) > osc52MaxBufSize {
		o.buf = o.buf[len(o.buf)-osc52MaxBufSize:]
	}
}

// extractOSC52 finds and extracts the first complete OSC 52 sequence from data.
// Returns: clipboard text (empty if malformed), remaining data, and whether a sequence was found.
func (o *osc52Scanner) extractOSC52(data []byte) (clipboardText string, remaining []byte, found bool) {
	// Look for tmux passthrough first: \x1bPtmux;\x1b
	if idx := indexTmuxPassthrough(data); idx != -1 {
		text, rem, ok := o.extractTmuxPassthrough(data, idx)
		if ok {
			return text, rem, true
		}
		// Incomplete tmux sequence - keep data from idx onwards
		if idx > 0 {
			return "", data[idx:], false
		}
		return "", data, false
	}

	// Look for direct OSC 52: \x1b]52;
	idx := indexOSC52Start(data)
	if idx == -1 {
		// No OSC 52 start found - discard everything except last few bytes
		// (which might be the start of an escape sequence)
		if len(data) > 10 {
			return "", data[len(data)-10:], false
		}
		return "", data, false
	}

	// Find the terminator (BEL \x07 or ST \x1b\\)
	rest := data[idx:]
	endIdx := -1
	for i := 5; i < len(rest); i++ { // Start after "\x1b]52;"
		if rest[i] == 0x07 { // BEL terminator
			endIdx = i + 1
			break
		}
		if rest[i] == 0x1b && i+1 < len(rest) && rest[i+1] == '\\' { // ST terminator
			endIdx = i + 2
			break
		}
	}

	if endIdx == -1 {
		// No terminator yet - keep buffering from OSC start
		if idx > 0 {
			return "", data[idx:], false
		}
		return "", data, false
	}

	// Parse the complete sequence: \x1b]52;X;BASE64<term>
	seq := rest[:endIdx]
	clipboardText = o.parseOSC52Payload(seq)

	// Return remaining data after this sequence
	return clipboardText, data[idx+endIdx:], true
}

// extractTmuxPassthrough handles tmux DCS passthrough format:
// \x1bPtmux;\x1b\x1b]52;c;<base64>\x07\x1b\\
func (o *osc52Scanner) extractTmuxPassthrough(data []byte, idx int) (clipboardText string, remaining []byte, found bool) {
	rest := data[idx:]

	// Minimum: \x1bPtmux;\x1b\x1b]52;c;\x07\x1b\\ = ~20 bytes
	if len(rest) < 20 {
		return "", nil, false
	}

	// Find the ST terminator for the DCS: \x1b\\
	// The inner OSC 52 will have its own terminator (BEL or ST)
	endIdx := -1
	for i := 15; i < len(rest)-1; i++ {
		if rest[i] == 0x1b && rest[i+1] == '\\' {
			// Check if this is the outer DCS terminator (not inner ST)
			// Inner ST would be doubled: \x1b\x1b\\
			if i >= 2 && rest[i-1] == 0x1b {
				continue // This is escaped, keep looking
			}
			endIdx = i + 2
			break
		}
	}

	if endIdx == -1 {
		return "", nil, false
	}

	// Extract inner OSC 52 from tmux passthrough
	// Format: \x1bPtmux;\x1b<inner>\x1b\\
	// The inner content has doubled escapes
	inner := rest[8 : endIdx-2] // Skip "\x1bPtmux;\x1b" and trailing "\x1b\\"

	// Undouble the escapes in the inner content
	undoubled := undoubleEscapes(inner)

	// Now parse as regular OSC 52
	if len(undoubled) > 5 && undoubled[0] == 0x1b && undoubled[1] == ']' {
		clipboardText = o.parseOSC52Payload(undoubled)
	}

	return clipboardText, data[idx+endIdx:], true
}

// parseOSC52Payload extracts and decodes the base64 clipboard data from an OSC 52 sequence.
// Input format: \x1b]52;c;<base64><terminator>
func (o *osc52Scanner) parseOSC52Payload(seq []byte) string {
	// Find the second semicolon (after "52;X")
	secondSemi := -1
	for i := 5; i < len(seq)-1; i++ {
		if seq[i] == ';' {
			secondSemi = i
			break
		}
	}

	if secondSemi == -1 || secondSemi >= len(seq)-1 {
		return ""
	}

	// Determine terminator length
	termLen := 1 // BEL
	if len(seq) >= 2 && seq[len(seq)-2] == 0x1b {
		termLen = 2 // ST (\x1b\\)
	}

	// Extract base64 data
	if secondSemi+1 >= len(seq)-termLen {
		return ""
	}
	b64Data := seq[secondSemi+1 : len(seq)-termLen]

	if len(b64Data) == 0 {
		return ""
	}

	decoded, err := base64.StdEncoding.DecodeString(string(b64Data))
	if err != nil {
		return ""
	}

	return string(decoded)
}

// indexOSC52Start finds the start of an OSC 52 sequence (\x1b]52;)
func indexOSC52Start(data []byte) int {
	for i := 0; i <= len(data)-5; i++ {
		if data[i] == 0x1b && data[i+1] == ']' && data[i+2] == '5' && data[i+3] == '2' && data[i+4] == ';' {
			return i
		}
	}
	return -1
}

// indexTmuxPassthrough finds the start of a tmux DCS passthrough (\x1bPtmux;)
func indexTmuxPassthrough(data []byte) int {
	for i := 0; i <= len(data)-7; i++ {
		if data[i] == 0x1b && data[i+1] == 'P' &&
			data[i+2] == 't' && data[i+3] == 'm' && data[i+4] == 'u' && data[i+5] == 'x' && data[i+6] == ';' {
			return i
		}
	}
	return -1
}

// undoubleEscapes converts doubled escape characters back to single escapes.
// In tmux passthrough, ESC is represented as ESC ESC.
func undoubleEscapes(data []byte) []byte {
	result := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		if data[i] == 0x1b && i+1 < len(data) && data[i+1] == 0x1b {
			result = append(result, 0x1b)
			i++ // Skip the doubled escape
		} else {
			result = append(result, data[i])
		}
	}
	return result
}
