/* *
 * Webmux - a browser-based pane multiplexer
 * Copyright (C) 2026  Webmux contributors
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 */
package main

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

const (
	defaultTerminalCols = 80
	defaultTerminalRows = 24
	maxTerminalCols     = 1000
	maxTerminalRows     = 500
	maxTerminalPixels   = 65535
)

var terminalUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type terminalControlMessage struct {
	Type        string `json:"type"`
	Cols        int    `json:"cols"`
	Rows        int    `json:"rows"`
	PixelWidth  int    `json:"pixelWidth"`
	PixelHeight int    `json:"pixelHeight"`
}

func validTerminalSize(cols, rows int) bool {
	return cols >= 2 && cols <= maxTerminalCols && rows >= 1 && rows <= maxTerminalRows
}

func validTerminalPixels(width, height int) bool {
	if width == 0 || height == 0 {
		return width == 0 && height == 0
	}
	return width > 0 && width <= maxTerminalPixels && height > 0 && height <= maxTerminalPixels
}

func writeTerminalInput(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func terminalClientEnvironment(base []string) []string {
	env := make([]string, 0, len(base)+3)
	for _, entry := range base {
		if strings.HasPrefix(entry, "TERM=") || strings.HasPrefix(entry, "COLORTERM=") || strings.HasPrefix(entry, "TERM_PROGRAM=") {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"TERM_PROGRAM=webmux",
	)
}

func terminalAttachArgs(socketPath, configPath, session string) []string {
	args := []string{"-S", socketPath}
	if configPath != "" {
		args = append(args, "-f", configPath)
	}
	return append(args, "-T", "sixel", "attach-session", "-t", session)
}

// handleTerminalWebSocket attaches one browser client to the pane's durable
// tmux session. Closing this connection only ends the temporary attachment.
func (s *Server) handleTerminalWebSocket(w http.ResponseWriter, r *http.Request, paneID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pane, ok := s.manager.GetPane(paneID)
	if !ok || pane.Type != "terminal" {
		http.Error(w, "terminal pane not found", http.StatusNotFound)
		return
	}
	state, ok := s.manager.terminal.getState(paneID)
	if !ok {
		http.Error(w, "terminal session not found", http.StatusNotFound)
		return
	}

	args := terminalAttachArgs(s.manager.terminal.tmuxSocketPath(), s.manager.terminal.tmuxConfigPath, state.tmuxSession)
	cmd := exec.Command("tmux", args...)
	cmd.Env = terminalClientEnvironment(os.Environ())
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: defaultTerminalCols, Rows: defaultTerminalRows})
	if err != nil {
		http.Error(w, "failed to attach terminal", http.StatusBadGateway)
		return
	}

	conn, err := terminalUpgrader.Upgrade(w, r, nil)
	if err != nil {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return
	}

	started := time.Now()
	s.diagnosticf("terminal", "event=open pane=%s remote=%s", diagSanitize(paneID, 48), diagSanitize(r.RemoteAddr, 80))
	defer func() {
		_ = conn.Close()
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		s.diagnosticf("terminal", "event=close pane=%s durationMs=%d", diagSanitize(paneID, 48), time.Since(started).Milliseconds())
	}()

	outputDone := make(chan error, 1)
	go func() {
		scanner := newOSC52Scanner(s)
		buf := make([]byte, 32*1024)
		for {
			n, readErr := ptmx.Read(buf)
			if n > 0 {
				chunk := append([]byte(nil), buf[:n]...)
				scanner.ObserveBackendToClient(chunk)
				if writeErr := conn.WriteMessage(websocket.BinaryMessage, chunk); writeErr != nil {
					outputDone <- writeErr
					return
				}
			}
			if readErr != nil {
				outputDone <- readErr
				return
			}
		}
	}()

	inputDone := make(chan error, 1)
	go func() {
		for {
			messageType, data, readErr := conn.ReadMessage()
			if readErr != nil {
				inputDone <- readErr
				return
			}
			switch messageType {
			case websocket.BinaryMessage:
				if len(data) > 0 {
					if writeErr := writeTerminalInput(ptmx, data); writeErr != nil {
						inputDone <- writeErr
						return
					}
				}
			case websocket.TextMessage:
				var control terminalControlMessage
				if json.Unmarshal(data, &control) != nil || control.Type != "resize" {
					continue
				}
				if !validTerminalSize(control.Cols, control.Rows) || !validTerminalPixels(control.PixelWidth, control.PixelHeight) {
					continue
				}
				if resizeErr := pty.Setsize(ptmx, &pty.Winsize{
					Cols: uint16(control.Cols), Rows: uint16(control.Rows),
					X: uint16(control.PixelWidth), Y: uint16(control.PixelHeight),
				}); resizeErr != nil {
					inputDone <- resizeErr
					return
				}
			}
		}
	}()

	select {
	case err := <-inputDone:
		if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
			log.Printf("Pane %s: terminal input closed: %v", paneID, err)
		}
	case err := <-outputDone:
		if err != nil && !strings.Contains(err.Error(), "input/output error") {
			log.Printf("Pane %s: terminal output closed: %v", paneID, err)
		}
	case <-r.Context().Done():
	}
}

func (s *Server) serveTerminalPopout(w http.ResponseWriter, r *http.Request, pane *Pane) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/p/"+pane.ID)
	if path != "" && path != "/" && path != "/index.html" {
		http.NotFound(w, r)
		return
	}

	document := fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1,user-scalable=no">
  <title>Terminal %s</title>
  <link rel="stylesheet" href="../../vendor/xterm/xterm.css">
  <link rel="stylesheet" href="../../terminal-popout.css">
  <script src="../../vendor/xterm/xterm.js"></script>
  <script src="../../vendor/xterm/addon-fit.js"></script>
  <script src="../../vendor/xterm/addon-webgl.js"></script>
  <script src="../../vendor/xterm/addon-image.js"></script>
</head>
<body><div id="terminal"></div><script src="../../terminal-popout.js"></script></body>
</html>`, html.EscapeString(pane.Name))
	document = injectPanePopoutBridge(document)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	_, _ = w.Write([]byte(document))
}
