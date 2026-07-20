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
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

const (
	maxTerminalCols    = 1000
	maxTerminalRows    = 500
	maxTerminalPixels  = 65535
	maxTerminalMessage = 64 * 1024
	terminalWriteWait  = 10 * time.Second
	terminalPongWait   = 60 * time.Second
	terminalPingPeriod = 50 * time.Second
	terminalHandshake  = 5 * time.Second
)

var terminalUpgrader = websocket.Upgrader{
	CheckOrigin: terminalOriginAllowed,
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

func firstForwardedValue(value string) string {
	if idx := strings.IndexByte(value, ','); idx != -1 {
		value = value[:idx]
	}
	return strings.TrimSpace(value)
}

func canonicalOriginHost(host, scheme string) string {
	u := &url.URL{Scheme: scheme, Host: host}
	hostname := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		return net.JoinHostPort(hostname, port)
	}
	return hostname
}

func terminalOriginAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	originURL, err := url.Parse(origin)
	if err != nil || originURL.Host == "" || (originURL.Scheme != "http" && originURL.Scheme != "https") {
		return false
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := firstForwardedValue(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		scheme = strings.ToLower(forwarded)
	}
	host := r.Host
	if forwarded := firstForwardedValue(r.Header.Get("X-Forwarded-Host")); forwarded != "" {
		host = forwarded
	}
	return strings.EqualFold(originURL.Scheme, scheme) &&
		canonicalOriginHost(originURL.Host, originURL.Scheme) == canonicalOriginHost(host, scheme)
}

func terminalWinsize(data []byte) (*pty.Winsize, bool) {
	var control terminalControlMessage
	if json.Unmarshal(data, &control) != nil || control.Type != "resize" {
		return nil, false
	}
	if !validTerminalSize(control.Cols, control.Rows) || !validTerminalPixels(control.PixelWidth, control.PixelHeight) {
		return nil, false
	}
	return &pty.Winsize{
		Cols: uint16(control.Cols), Rows: uint16(control.Rows),
		X: uint16(control.PixelWidth), Y: uint16(control.PixelHeight),
	}, true
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

func terminalAttachArgs(socketPath, configPath, session string, sixelSupported bool) []string {
	args := []string{"-S", socketPath}
	if configPath != "" {
		args = append(args, "-f", configPath)
	}
	if sixelSupported {
		args = append(args, "-T", "sixel")
	}
	return append(args, "attach-session", "-t", session)
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
	if !websocket.IsWebSocketUpgrade(r) {
		http.Error(w, "WebSocket upgrade required", http.StatusBadRequest)
		return
	}

	conn, err := terminalUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(maxTerminalMessage)
	_ = conn.SetReadDeadline(time.Now().Add(terminalHandshake))
	messageType, data, err := conn.ReadMessage()
	initialSize, validInitialSize := terminalWinsize(data)
	if err != nil || messageType != websocket.TextMessage || !validInitialSize {
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "initial terminal size required"),
			time.Now().Add(terminalWriteWait))
		_ = conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(terminalPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(terminalPongWait))
	})

	args := terminalAttachArgs(s.manager.terminal.tmuxSocketPath(), s.manager.terminal.tmuxConfigPath, state.tmuxSession, s.manager.terminal.sixelSupported)
	cmd := exec.Command("tmux", args...)
	cmd.Env = terminalClientEnvironment(os.Environ())
	ptmx, err := pty.StartWithSize(cmd, initialSize)
	if err != nil {
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "failed to attach terminal"),
			time.Now().Add(terminalWriteWait))
		_ = conn.Close()
		return
	}

	started := time.Now()
	s.diagnosticf("terminal", "event=open pane=%s remote=%s", diagSanitize(paneID, 48), diagSanitize(r.RemoteAddr, 80))
	defer func() {
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "terminal attachment closed"),
			time.Now().Add(terminalWriteWait))
		_ = conn.Close()
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		s.diagnosticf("terminal", "event=close pane=%s durationMs=%d", diagSanitize(paneID, 48), time.Since(started).Milliseconds())
	}()

	outputDone := make(chan error, 1)
	heartbeatDone := make(chan error, 1)
	stopHeartbeat := make(chan struct{})
	defer close(stopHeartbeat)
	go func() {
		ticker := time.NewTicker(terminalPingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(terminalWriteWait)); err != nil {
					heartbeatDone <- err
					return
				}
			case <-stopHeartbeat:
				return
			}
		}
	}()
	go func() {
		scanner := newOSC52Scanner(s)
		buf := make([]byte, 32*1024)
		for {
			n, readErr := ptmx.Read(buf)
			if n > 0 {
				chunk := append([]byte(nil), buf[:n]...)
				scanner.ObserveBackendToClient(chunk)
				_ = conn.SetWriteDeadline(time.Now().Add(terminalWriteWait))
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
				size, ok := terminalWinsize(data)
				if !ok {
					continue
				}
				if resizeErr := pty.Setsize(ptmx, size); resizeErr != nil {
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
	case err := <-heartbeatDone:
		if err != nil {
			log.Printf("Pane %s: terminal heartbeat closed: %v", paneID, err)
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
