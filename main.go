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
	"archive/zip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed static/*
var staticFiles embed.FS

// SECTION: LOGGING

// RotatingLogWriter writes to both stdout and a rotating log file
type RotatingLogWriter struct {
	file      *os.File
	filePath  string
	maxLines  int
	lineCount int
	mu        sync.Mutex
}

// Global log writer for the application
var logWriter *RotatingLogWriter

// NewRotatingLogWriter creates a log writer that tees to stdout and a file in temp dir
func NewRotatingLogWriter(maxLines int) (*RotatingLogWriter, error) {
	logPath := filepath.Join(os.TempDir(), fmt.Sprintf("webmux-%d.log", os.Getpid()))
	// Use 0600 permissions - logs may contain sensitive paths or error details
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to create log file: %w", err)
	}

	return &RotatingLogWriter{
		file:     file,
		filePath: logPath,
		maxLines: maxLines,
	}, nil
}

// Write implements io.Writer, writing to both stdout and the log file
func (w *RotatingLogWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Always write to stdout
	os.Stdout.Write(p)

	// Count newlines in the data
	newLines := 0
	for _, b := range p {
		if b == '\n' {
			newLines++
		}
	}

	// Check if we need to rotate (simple rotation: truncate when limit reached)
	if w.lineCount+newLines > w.maxLines {
		w.file.Truncate(0)
		w.file.Seek(0, 0)
		w.lineCount = 0
		// Write rotation marker
		w.file.WriteString(fmt.Sprintf("[%s] --- Log rotated (max %d lines) ---\n",
			time.Now().Format("2006/01/02 15:04:05"), w.maxLines))
		w.lineCount++
	}

	// Write to file
	n, err = w.file.Write(p)
	w.lineCount += newLines
	return n, err
}

// Path returns the log file path
func (w *RotatingLogWriter) Path() string {
	return w.filePath
}

// ReadLogs reads the last n lines from the log file
func (w *RotatingLogWriter) ReadLogs(maxLines int) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Validate input
	if maxLines <= 0 {
		maxLines = 1000
	}

	// Sync file to ensure all writes are visible
	if err := w.file.Sync(); err != nil {
		return "", fmt.Errorf("sync failed: %w", err)
	}

	// Read the entire file
	content, err := os.ReadFile(w.filePath)
	if err != nil {
		return "", fmt.Errorf("read failed: %w", err)
	}

	// Handle empty file
	if len(content) == 0 {
		return "", nil
	}

	lines := strings.Split(string(content), "\n")

	// Return last maxLines lines
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}

	return strings.Join(lines, "\n"), nil
}

// Close closes the log file
func (w *RotatingLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		return w.file.Close()
	}
	return nil
}

// SECTION: TYPES

// Settings represents user-configurable settings
type Settings struct {
	// Multiplexer UI colors
	UI UIColors `json:"ui"`
	// Terminal colors
	Terminal TerminalColors `json:"terminal"`
	// Keybar configuration
	Keybar KeybarSettings `json:"keybar"`
}

// KeybarSettings represents keybar button configuration
type KeybarSettings struct {
	Buttons []string `json:"buttons"`
}

// UIColors represents the multiplexer UI color scheme
type UIColors struct {
	BgPrimary     string `json:"bgPrimary"`
	BgSecondary   string `json:"bgSecondary"`
	BgTertiary    string `json:"bgTertiary"`
	TextPrimary   string `json:"textPrimary"`
	TextSecondary string `json:"textSecondary"`
	TextMuted     string `json:"textMuted"`
	Accent        string `json:"accent"`
	AccentHover   string `json:"accentHover"`
	Border        string `json:"border"`
}

// TerminalColors represents terminal color scheme using Base24 naming
// Base24 maps: base00=bg, base01-03=grays, base04-05=fg, base06-07=bright fg
// base08-0F=colors (red,orange,yellow,green,cyan,blue,magenta,brown)
// base10-11=darker bg, base12-17=bright colors
type TerminalColors struct {
	Base00 string `json:"base00"` // Background
	Base01 string `json:"base01"` // Lighter Background (status bars)
	Base02 string `json:"base02"` // Selection Background
	Base03 string `json:"base03"` // Comments, Invisibles
	Base04 string `json:"base04"` // Dark Foreground (status bars)
	Base05 string `json:"base05"` // Default Foreground
	Base06 string `json:"base06"` // Light Foreground
	Base07 string `json:"base07"` // Lightest Foreground
	Base08 string `json:"base08"` // Red
	Base09 string `json:"base09"` // Orange
	Base0A string `json:"base0A"` // Yellow
	Base0B string `json:"base0B"` // Green
	Base0C string `json:"base0C"` // Cyan
	Base0D string `json:"base0D"` // Blue
	Base0E string `json:"base0E"` // Magenta
	Base0F string `json:"base0F"` // Brown/Dark Red
	Base10 string `json:"base10"` // Darker Background
	Base11 string `json:"base11"` // Darkest Background
	Base12 string `json:"base12"` // Bright Red
	Base13 string `json:"base13"` // Bright Yellow
	Base14 string `json:"base14"` // Bright Green
	Base15 string `json:"base15"` // Bright Cyan
	Base16 string `json:"base16"` // Bright Blue
	Base17 string `json:"base17"` // Bright Magenta
}

// SECTION: SETTINGS

// DefaultSettings returns the default settings
func DefaultSettings() *Settings {
	return &Settings{
		UI: UIColors{
			BgPrimary:     "#1e1e2e",
			BgSecondary:   "#181825",
			BgTertiary:    "#313244",
			TextPrimary:   "#cdd6f4",
			TextSecondary: "#a6adc8",
			TextMuted:     "#6c7086",
			Accent:        "#89b4fa",
			AccentHover:   "#b4befe",
			Border:        "#45475a",
		},
		Terminal: TerminalColors{
			Base00: "#1e1e2e", // Background
			Base01: "#181825", // Lighter Background
			Base02: "#313244", // Selection
			Base03: "#45475a", // Comments
			Base04: "#585b70", // Dark Foreground
			Base05: "#cdd6f4", // Foreground
			Base06: "#f5e0dc", // Light Foreground
			Base07: "#ffffff", // Lightest
			Base08: "#f38ba8", // Red
			Base09: "#fab387", // Orange
			Base0A: "#f9e2af", // Yellow
			Base0B: "#a6e3a1", // Green
			Base0C: "#94e2d5", // Cyan
			Base0D: "#89b4fa", // Blue
			Base0E: "#cba6f7", // Magenta
			Base0F: "#f2cdcd", // Brown
			Base10: "#11111b", // Darker Background
			Base11: "#0a0a0f", // Darkest Background
			Base12: "#f38ba8", // Bright Red
			Base13: "#f9e2af", // Bright Yellow
			Base14: "#a6e3a1", // Bright Green
			Base15: "#94e2d5", // Bright Cyan
			Base16: "#89b4fa", // Bright Blue
			Base17: "#cba6f7", // Bright Magenta
		},
		Keybar: KeybarSettings{
			Buttons: []string{"C-c", "C-d", "C-z", "C-\\", "C-l", "C-r", "C-u", "C-w"},
		},
	}
}

// xdgConfigHome returns XDG_CONFIG_HOME or ~/.config
func xdgConfigHome() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

// xdgDataHome returns XDG_DATA_HOME or ~/.local/share
func xdgDataHome() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

func instanceIDForPort(port string) string {
	port = strings.TrimSpace(port)
	if port == "" {
		port = "unknown"
	}

	var b strings.Builder
	b.Grow(len("port-") + len(port))
	b.WriteString("port-")
	for _, r := range port {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// xdgStateHome returns XDG_STATE_HOME or ~/.local/state
// _ for now to silence unused function warning
func _() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state")
}

// settingsFilePath returns the path to the settings file
func settingsFilePath() string {
	return filepath.Join(xdgConfigHome(), "webmux", "settings.json")
}

// LoadSettings loads settings from disk or returns defaults
func LoadSettings() *Settings {
	path := settingsFilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return DefaultSettings()
	}

	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return DefaultSettings()
	}

	// Merge with defaults to fill in any missing values
	mergeWithDefaults(&settings)
	return &settings
}

// mergeWithDefaults fills in empty string values with defaults
func mergeWithDefaults(s *Settings) {
	d := DefaultSettings()

	// UI colors
	if s.UI.BgPrimary == "" {
		s.UI.BgPrimary = d.UI.BgPrimary
	}
	if s.UI.BgSecondary == "" {
		s.UI.BgSecondary = d.UI.BgSecondary
	}
	if s.UI.BgTertiary == "" {
		s.UI.BgTertiary = d.UI.BgTertiary
	}
	if s.UI.TextPrimary == "" {
		s.UI.TextPrimary = d.UI.TextPrimary
	}
	if s.UI.TextSecondary == "" {
		s.UI.TextSecondary = d.UI.TextSecondary
	}
	if s.UI.TextMuted == "" {
		s.UI.TextMuted = d.UI.TextMuted
	}
	if s.UI.Accent == "" {
		s.UI.Accent = d.UI.Accent
	}
	if s.UI.AccentHover == "" {
		s.UI.AccentHover = d.UI.AccentHover
	}
	if s.UI.Border == "" {
		s.UI.Border = d.UI.Border
	}

	// Terminal colors
	if s.Terminal.Base00 == "" {
		s.Terminal.Base00 = d.Terminal.Base00
	}
	if s.Terminal.Base01 == "" {
		s.Terminal.Base01 = d.Terminal.Base01
	}
	if s.Terminal.Base02 == "" {
		s.Terminal.Base02 = d.Terminal.Base02
	}
	if s.Terminal.Base03 == "" {
		s.Terminal.Base03 = d.Terminal.Base03
	}
	if s.Terminal.Base04 == "" {
		s.Terminal.Base04 = d.Terminal.Base04
	}
	if s.Terminal.Base05 == "" {
		s.Terminal.Base05 = d.Terminal.Base05
	}
	if s.Terminal.Base06 == "" {
		s.Terminal.Base06 = d.Terminal.Base06
	}
	if s.Terminal.Base07 == "" {
		s.Terminal.Base07 = d.Terminal.Base07
	}
	if s.Terminal.Base08 == "" {
		s.Terminal.Base08 = d.Terminal.Base08
	}
	if s.Terminal.Base09 == "" {
		s.Terminal.Base09 = d.Terminal.Base09
	}
	if s.Terminal.Base0A == "" {
		s.Terminal.Base0A = d.Terminal.Base0A
	}
	if s.Terminal.Base0B == "" {
		s.Terminal.Base0B = d.Terminal.Base0B
	}
	if s.Terminal.Base0C == "" {
		s.Terminal.Base0C = d.Terminal.Base0C
	}
	if s.Terminal.Base0D == "" {
		s.Terminal.Base0D = d.Terminal.Base0D
	}
	if s.Terminal.Base0E == "" {
		s.Terminal.Base0E = d.Terminal.Base0E
	}
	if s.Terminal.Base0F == "" {
		s.Terminal.Base0F = d.Terminal.Base0F
	}
	if s.Terminal.Base10 == "" {
		s.Terminal.Base10 = d.Terminal.Base10
	}
	if s.Terminal.Base11 == "" {
		s.Terminal.Base11 = d.Terminal.Base11
	}
	if s.Terminal.Base12 == "" {
		s.Terminal.Base12 = d.Terminal.Base12
	}
	if s.Terminal.Base13 == "" {
		s.Terminal.Base13 = d.Terminal.Base13
	}
	if s.Terminal.Base14 == "" {
		s.Terminal.Base14 = d.Terminal.Base14
	}
	if s.Terminal.Base15 == "" {
		s.Terminal.Base15 = d.Terminal.Base15
	}
	if s.Terminal.Base16 == "" {
		s.Terminal.Base16 = d.Terminal.Base16
	}
	if s.Terminal.Base17 == "" {
		s.Terminal.Base17 = d.Terminal.Base17
	}

	// Keybar settings - use defaults if empty
	if len(s.Keybar.Buttons) == 0 {
		s.Keybar.Buttons = d.Keybar.Buttons
	}
}

// SaveSettings saves settings to disk
func SaveSettings(settings *Settings) error {
	path := settingsFilePath()

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// SECTION: SERVER

// Server holds the HTTP server and pane manager
type Server struct {
	manager          *PaneManager
	uploadDir        string
	settings         *Settings
	settingsMu       sync.RWMutex
	scratchText      string
	scratchMu        sync.RWMutex
	scratchSubs      map[chan string]struct{} // SSE subscribers
	scratchSubMu     sync.Mutex
	markedFiles      []MarkedFile // Files marked for download
	markedMu         sync.RWMutex
	markedSubs       map[chan string]struct{} // SSE subscribers for marked files
	markedSubMu      sync.Mutex
	uiState          *UIState // UI layout state (groups, order, etc.)
	uiStateMu        sync.RWMutex
	paneStorage      map[string]*PaneStorageState // Browser storage mirrored by shared pane backend
	paneStorageMu    sync.RWMutex
	clipboard        string       // Server-side clipboard for wm CLI
	clipboardVersion uint64       // Increments on each clipboard change
	clipboardMu      sync.RWMutex // Protects clipboard and clipboardVersion
}

// NewServer creates a new server instance
func NewServer(manager *PaneManager, uploadDir string) *Server {
	s := &Server{
		manager:     manager,
		uploadDir:   uploadDir,
		settings:    LoadSettings(),
		scratchSubs: make(map[chan string]struct{}),
		markedFiles: make([]MarkedFile, 0),
		markedSubs:  make(map[chan string]struct{}),
		paneStorage: LoadPaneStorage(),
		uiState: &UIState{
			Groups:     make([]UIGroup, 0),
			GroupOrder: make([]string, 0),
		},
	}
	// Wire up settings getter for pane manager
	manager.getSettings = func() *Settings {
		s.settingsMu.RLock()
		defer s.settingsMu.RUnlock()
		return s.settings
	}
	// Wire up pane cleanup callback
	manager.onPaneClosed = func(paneID string) {
		s.removePaneFromUIState(paneID)
	}
	return s
}

// SECTION: API

// handleInfo returns server configuration info
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	panes := s.manager.ListPanes()

	json.NewEncoder(w).Encode(map[string]any{
		"workDir":    s.manager.workDir,
		"uploadDir":  s.uploadDir,
		"shell":      s.manager.shell,
		"port":       s.manager.serverPort,
		"instanceID": s.manager.instanceID,
		"paneCount":  len(panes),
		"paneTypes":  s.manager.PaneTypes(),
		"tmuxSocket": s.manager.tmuxSocketPath(),
	})
}

// Maximum lines that can be requested from logs endpoint
const maxLogLines = 10000

// handleLogs returns the server logs
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Prevent caching of logs
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")

	if logWriter == nil {
		http.Error(w, "Log file not available", http.StatusServiceUnavailable)
		return
	}

	// Get optional lines parameter (default to max, capped for safety)
	maxLines := maxLogLines
	if linesParam := r.URL.Query().Get("lines"); linesParam != "" {
		if n, err := strconv.Atoi(linesParam); err == nil && n > 0 {
			if n > maxLogLines {
				n = maxLogLines
			}
			maxLines = n
		}
	}

	logs, err := logWriter.ReadLogs(maxLines)
	if err != nil {
		// Don't expose internal error details
		log.Printf("Failed to read logs: %v", err)
		http.Error(w, "Failed to read logs", http.StatusInternalServerError)
		return
	}

	w.Write([]byte(logs))
}

// handleScratch handles scratch pad GET/POST/DELETE
func (s *Server) handleScratch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		s.scratchMu.RLock()
		text := s.scratchText
		s.scratchMu.RUnlock()
		json.NewEncoder(w).Encode(map[string]string{"text": text})

	case http.MethodPost:
		var req struct {
			Text   string `json:"text"`
			Toggle string `json:"toggle"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Toggle mode: just signal to show/hide without changing text
		if req.Toggle == "true" {
			s.scratchMu.RLock()
			text := s.scratchText
			s.scratchMu.RUnlock()
			// Send toggle event with current text
			s.notifyScratchSubscribers("toggle:" + text)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "toggled", "text": text})
			return
		}

		s.scratchMu.Lock()
		s.scratchText = req.Text
		s.scratchMu.Unlock()

		// Notify SSE subscribers
		s.notifyScratchSubscribers("text:" + req.Text)

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	case http.MethodDelete:
		s.scratchMu.Lock()
		s.scratchText = ""
		s.scratchMu.Unlock()

		// Notify SSE subscribers to close
		s.notifyScratchSubscribers("clear:")

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "cleared"})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleScratchEvents provides SSE stream for scratch pad updates
func (s *Server) handleScratchEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable reverse proxy buffering (nginx)

	// Create channel for this subscriber
	ch := make(chan string, 10)
	s.scratchSubMu.Lock()
	s.scratchSubs[ch] = struct{}{}
	s.scratchSubMu.Unlock()

	defer func() {
		s.scratchSubMu.Lock()
		delete(s.scratchSubs, ch)
		s.scratchSubMu.Unlock()
		close(ch)
	}()

	// Send current text immediately (as init event)
	s.scratchMu.RLock()
	currentText := s.scratchText
	s.scratchMu.RUnlock()

	data, _ := json.Marshal(map[string]any{"type": "init", "text": currentText})
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()

	// Stream updates
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			// Parse message type: "type:content"
			var eventType, content string
			if idx := strings.Index(msg, ":"); idx != -1 {
				eventType = msg[:idx]
				content = msg[idx+1:]
			} else {
				eventType = "text"
				content = msg
			}
			data, _ := json.Marshal(map[string]any{"type": eventType, "text": content})
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// notifyScratchSubscribers sends text to all SSE subscribers
func (s *Server) notifyScratchSubscribers(text string) {
	s.scratchSubMu.Lock()
	defer s.scratchSubMu.Unlock()

	for ch := range s.scratchSubs {
		select {
		case ch <- text:
		default:
			// Skip if channel is full
		}
	}
}

// handleSettings handles settings GET/POST
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		s.settingsMu.RLock()
		json.NewEncoder(w).Encode(s.settings)
		s.settingsMu.RUnlock()

	case http.MethodPost:
		var settings Settings
		if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
			http.Error(w, "Invalid settings: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Merge with defaults to fill in any missing values
		mergeWithDefaults(&settings)

		s.settingsMu.Lock()
		s.settings = &settings
		s.settingsMu.Unlock()

		if err := SaveSettings(&settings); err != nil {
			http.Error(w, "Failed to save settings: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "saved"})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// SECTION: CLIPBOARD

// Maximum clipboard size (10MB - same as OSC 52 payload limit in xterm.js)
const maxClipboardSize = 10 * 1024 * 1024

// handleClipboard provides a server-side clipboard that the wm CLI can use
// This enables clipboard integration without relying on OSC 52 escape sequences
// POST: set clipboard content (body is the text, max 10MB)
// GET: get clipboard content (returns the text)
func (s *Server) handleClipboard(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.clipboardMu.RLock()
		content := s.clipboard
		s.clipboardMu.RUnlock()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(content))

	case http.MethodPost:
		// Limit request body size to prevent memory exhaustion
		r.Body = http.MaxBytesReader(w, r.Body, maxClipboardSize)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			if err.Error() == "http: request body too large" {
				http.Error(w, "Clipboard content too large (max 10MB)", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "Failed to read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		s.clipboardMu.Lock()
		s.clipboard = string(body)
		s.clipboardVersion++
		s.clipboardMu.Unlock()
		w.WriteHeader(http.StatusOK)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleClipboardVersion returns the current clipboard version number.
// The browser polls this to detect clipboard changes without SSE buffering issues.
func (s *Server) handleClipboardVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.clipboardMu.RLock()
	v := s.clipboardVersion
	s.clipboardMu.RUnlock()
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	fmt.Fprintf(w, "%d", v)
}

// handleUIState handles GET/POST for UI layout state
func (s *Server) handleUIState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		s.uiStateMu.RLock()
		state := s.uiState
		s.uiStateMu.RUnlock()

		// Validate state against current panes before returning
		validState := s.validateUIState(state)
		json.NewEncoder(w).Encode(validState)

	case http.MethodPost:
		var state UIState
		if err := json.NewDecoder(r.Body).Decode(&state); err != nil {
			http.Error(w, "Invalid state: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Validate against current panes
		validState := s.validateUIState(&state)

		s.uiStateMu.Lock()
		s.uiState = validState
		s.uiStateMu.Unlock()

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(validState)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// validateUIState removes references to panes that no longer exist
// and resets counters if all panes are gone
func (s *Server) validateUIState(state *UIState) *UIState {
	if state == nil {
		return &UIState{
			Groups:     make([]UIGroup, 0),
			GroupOrder: make([]string, 0),
		}
	}

	// Get current valid pane IDs
	panes := s.manager.ListPanes()
	validPaneIDs := make(map[string]bool)
	for _, pane := range panes {
		validPaneIDs[pane.ID] = true
	}

	// Filter groups to only include valid panes
	validGroups := make([]UIGroup, 0)
	validGroupIDs := make(map[string]bool)

	for _, group := range state.Groups {
		validPaneIDsInGroup := make([]string, 0)
		for _, paneID := range group.PaneIDs {
			if validPaneIDs[paneID] {
				validPaneIDsInGroup = append(validPaneIDsInGroup, paneID)
			}
		}

		if len(validPaneIDsInGroup) > 0 {
			// Keep group with only valid panes
			newGroup := UIGroup{
				ID:               group.ID,
				Name:             group.Name,
				PaneIDs:          validPaneIDsInGroup,
				Layout:           group.Layout,
				ExpandedQuadrant: group.ExpandedQuadrant,
				SplitRatio:       group.SplitRatio,
				CellMapping:      group.CellMapping,
			}

			// If pane count changed, reset layout to defaults
			if len(validPaneIDsInGroup) != len(group.PaneIDs) {
				newGroup.Layout = getDefaultLayout(len(validPaneIDsInGroup))
				newGroup.SplitRatio = getDefaultSplitRatio(len(validPaneIDsInGroup))
				newGroup.CellMapping = nil
			}

			validGroups = append(validGroups, newGroup)
			validGroupIDs[group.ID] = true
		}
	}

	// Filter group order
	validOrder := make([]string, 0)
	for _, gid := range state.GroupOrder {
		if validGroupIDs[gid] {
			validOrder = append(validOrder, gid)
		}
	}

	// Add any groups not in order
	for _, g := range validGroups {
		if !slices.Contains(validOrder, g.ID) {
			validOrder = append(validOrder, g.ID)
		}
	}

	// Filter custom names
	validCustomNames := make([]string, 0)
	for _, paneID := range state.CustomNames {
		if validPaneIDs[paneID] {
			validCustomNames = append(validCustomNames, paneID)
		}
	}

	// Validate active group
	activeGroupID := state.ActiveGroupID
	if !validGroupIDs[activeGroupID] && len(validOrder) > 0 {
		activeGroupID = validOrder[0]
	} else if len(validOrder) == 0 {
		activeGroupID = ""
	}

	// Reset counter if no groups remain
	groupCounter := state.GroupCounter
	if len(validGroups) == 0 {
		groupCounter = 0
	}

	return &UIState{
		Groups:           validGroups,
		GroupOrder:       validOrder,
		ActiveGroupID:    activeGroupID,
		GroupCounter:     groupCounter,
		SidebarCollapsed: state.SidebarCollapsed,
		CustomNames:      validCustomNames,
	}
}

// removePaneFromUIState removes a pane from UI state when it dies
func (s *Server) removePaneFromUIState(paneID string) {
	s.uiStateMu.Lock()
	defer s.uiStateMu.Unlock()

	if s.uiState == nil {
		return
	}

	// Remove from groups
	newGroups := make([]UIGroup, 0)
	removedGroupIDs := make(map[string]bool)

	for _, group := range s.uiState.Groups {
		originalCount := len(group.PaneIDs)
		newPaneIDs := make([]string, 0)
		for _, id := range group.PaneIDs {
			if id != paneID {
				newPaneIDs = append(newPaneIDs, id)
			}
		}

		if len(newPaneIDs) > 0 {
			group.PaneIDs = newPaneIDs
			// Reset layout if count changed
			if len(newPaneIDs) != originalCount {
				group.Layout = getDefaultLayout(len(newPaneIDs))
				group.SplitRatio = getDefaultSplitRatio(len(newPaneIDs))
				group.CellMapping = nil
			}
			newGroups = append(newGroups, group)
		} else {
			removedGroupIDs[group.ID] = true
		}
	}

	// Update group order
	newOrder := make([]string, 0)
	for _, gid := range s.uiState.GroupOrder {
		if !removedGroupIDs[gid] {
			newOrder = append(newOrder, gid)
		}
	}

	// Update active group if it was removed
	if removedGroupIDs[s.uiState.ActiveGroupID] {
		if len(newOrder) > 0 {
			s.uiState.ActiveGroupID = newOrder[0]
		} else {
			s.uiState.ActiveGroupID = ""
		}
	}

	// Remove from custom names
	newCustomNames := make([]string, 0)
	for _, id := range s.uiState.CustomNames {
		if id != paneID {
			newCustomNames = append(newCustomNames, id)
		}
	}

	s.uiState.Groups = newGroups
	s.uiState.GroupOrder = newOrder
	s.uiState.CustomNames = newCustomNames

	// Reset counter if no groups remain
	if len(newGroups) == 0 {
		s.uiState.GroupCounter = 0
	}
}

// getDefaultLayout returns the default layout for a given pane count
func getDefaultLayout(count int) string {
	switch count {
	case 1:
		return "single"
	case 2:
		return "horizontal"
	default:
		return "grid"
	}
}

// getDefaultSplitRatio returns the default split ratio for a given pane count
func getDefaultSplitRatio(count int) []float64 {
	switch count {
	case 1:
		return nil
	case 2:
		return []float64{0.5}
	default:
		return []float64{0.5, 0.5}
	}
}

// handlePanes handles pane CRUD operations.
func (s *Server) handlePanes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		// List all panes
		panes := s.manager.ListPanes()
		json.NewEncoder(w).Encode(panes)

	case http.MethodPost:
		// Create new pane
		var req struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Type == "" {
			req.Type = "terminal"
		}
		if !s.manager.isSupportedPaneType(req.Type) {
			http.Error(w, "unsupported pane type", http.StatusBadRequest)
			return
		}
		if available, reason := s.manager.paneTypeAvailability(req.Type); !available {
			http.Error(w, reason, http.StatusBadRequest)
			return
		}

		// Log pane creation with origin info for debugging
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = r.Header.Get("Referer")
		}
		remoteAddr := r.RemoteAddr
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			remoteAddr = fwd
		}
		log.Printf("Pane create request from %s (origin: %s)", remoteAddr, origin)

		pane, err := s.manager.CreatePane(req.Type, req.Name)
		if err != nil {
			log.Printf("Pane create failed: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("Pane %s created successfully", pane.ID)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(pane)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePane handles operations on a specific pane.
func (s *Server) handlePane(w http.ResponseWriter, r *http.Request) {
	// Extract pane ID from path: /api/panes/{id} or /api/panes/{id}/input
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	paneID := parts[3]

	// Check for sub-resource paths like /api/panes/{id}/input
	if len(parts) >= 5 && parts[4] == "input" {
		s.handlePaneInput(w, r)
		return
	}

	switch r.Method {
	case http.MethodDelete:
		// Log who is requesting the pane close
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = r.Header.Get("Referer")
		}
		remoteAddr := r.RemoteAddr
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			remoteAddr = fwd
		}
		log.Printf("Pane DELETE request for %s from %s (origin: %s)", paneID, remoteAddr, origin)

		if err := s.manager.ClosePane(paneID); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodPatch:
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.manager.RenamePane(paneID, req.Name); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// Maximum request body size for input endpoint (32KB should be plenty).
const maxInputRequestSize = 32 * 1024

// handlePaneInput handles sending key/text input to a pane.
// POST /api/panes/{id}/input
func (s *Server) handlePaneInput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract pane ID from path: /api/panes/{id}/input
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 || parts[4] != "input" {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	paneID := parts[3]

	// Validate pane ID format (should be "pane-NNNN")
	if !strings.HasPrefix(paneID, "pane-") || len(paneID) > 20 {
		http.Error(w, "Invalid pane ID format", http.StatusBadRequest)
		return
	}

	// Limit request body size to prevent abuse
	r.Body = http.MaxBytesReader(w, r.Body, maxInputRequestSize)

	var req PaneInputRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields() // Reject requests with unknown fields
	if err := decoder.Decode(&req); err != nil {
		if strings.Contains(err.Error(), "http: request body too large") {
			http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		}
		return
	}

	if err := s.manager.SendInput(paneID, &req); err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "pane not found") {
			http.Error(w, errMsg, http.StatusNotFound)
		} else if strings.Contains(errMsg, "invalid") || strings.Contains(errMsg, "too many") || strings.Contains(errMsg, "too long") || strings.Contains(errMsg, "unsupported") {
			http.Error(w, errMsg, http.StatusBadRequest)
		} else {
			// Log unexpected errors but return generic message
			log.Printf("SendInput error for pane %s: %v", paneID, err)
			http.Error(w, "Failed to send input", http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// SECTION: FILES

// MarkedFile represents a file or directory marked for download
type MarkedFile struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime"`
	IsDir   bool   `json:"isDir"`
}

// handleUpload handles file uploads to the server
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse multipart form (max 1GB)
	if err := r.ParseMultipartForm(1 << 30); err != nil {
		http.Error(w, "Failed to parse form: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Get target directory from form or use default
	targetDir := r.FormValue("directory")
	if targetDir == "" {
		targetDir = s.uploadDir
	}

	// Ensure target directory exists
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		http.Error(w, "Failed to create directory: "+err.Error(), http.StatusInternalServerError)
		return
	}

	files := r.MultipartForm.File["files"]
	uploaded := make([]string, 0, len(files))

	for _, fileHeader := range files {
		file, err := fileHeader.Open()
		if err != nil {
			http.Error(w, "Failed to open uploaded file: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer file.Close()

		// Sanitize filename to prevent path traversal
		filename := filepath.Base(fileHeader.Filename)
		destPath := filepath.Join(targetDir, filename)

		// Avoid overwriting existing files by appending a number suffix
		if _, err := os.Stat(destPath); err == nil {
			ext := filepath.Ext(filename)
			base := filename[:len(filename)-len(ext)]
			for i := 1; ; i++ {
				destPath = filepath.Join(targetDir, fmt.Sprintf("%s (%d)%s", base, i, ext))
				if _, err := os.Stat(destPath); os.IsNotExist(err) {
					break
				}
			}
		}

		dest, err := os.Create(destPath)
		if err != nil {
			http.Error(w, "Failed to create file: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer dest.Close()

		if _, err := io.Copy(dest, file); err != nil {
			http.Error(w, "Failed to write file: "+err.Error(), http.StatusInternalServerError)
			return
		}

		uploaded = append(uploaded, destPath)
		log.Printf("Uploaded file: %s", destPath)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"uploaded": uploaded,
		"count":    len(uploaded),
	})
}

// handleDownload serves files for download (directories are zipped)
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		http.Error(w, "path parameter required", http.StatusBadRequest)
		return
	}

	// Decode URL-encoded path
	filePath, err := url.QueryUnescape(filePath)
	if err != nil {
		http.Error(w, "Invalid path encoding", http.StatusBadRequest)
		return
	}

	// Clean the path to prevent directory traversal
	filePath = filepath.Clean(filePath)

	info, err := os.Stat(filePath)
	if os.IsNotExist(err) {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "Failed to stat file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if info.IsDir() {
		// Download directory as zip
		s.downloadDirAsZip(w, filePath)
		return
	}

	// Regular file - direct download
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(filePath)))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))

	http.ServeFile(w, r, filePath)
}

// downloadDirAsZip streams a directory as a zip file
func (s *Server) downloadDirAsZip(w http.ResponseWriter, dirPath string) {
	zipName := filepath.Base(dirPath) + ".zip"
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", zipName))
	w.Header().Set("Content-Type", "application/zip")

	zw := zip.NewWriter(w)
	defer zw.Close()

	filepath.Walk(dirPath, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil // Continue on errors
		}

		relPath, err := filepath.Rel(dirPath, path)
		if err != nil {
			return nil
		}

		if fi.IsDir() {
			if relPath != "." {
				header := &zip.FileHeader{
					Name:   relPath + "/",
					Method: zip.Store,
				}
				header.Modified = fi.ModTime()
				zw.CreateHeader(header)
			}
			return nil
		}

		// Skip non-regular files
		if !fi.Mode().IsRegular() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		header := &zip.FileHeader{
			Name:   relPath,
			Method: zip.Deflate,
		}
		header.Modified = fi.ModTime()

		zf, err := zw.CreateHeader(header)
		if err != nil {
			return nil
		}

		io.Copy(zf, f)
		return nil
	})
}

// handleBrowse lists files in a directory for the download UI
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	dirPath := r.URL.Query().Get("path")
	if dirPath == "" {
		dirPath, _ = os.UserHomeDir()
	}

	// Decode URL-encoded path
	dirPath, err := url.QueryUnescape(dirPath)
	if err != nil {
		http.Error(w, "Invalid path encoding", http.StatusBadRequest)
		return
	}

	dirPath = filepath.Clean(dirPath)

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		http.Error(w, "Failed to read directory: "+err.Error(), http.StatusInternalServerError)
		return
	}

	type FileInfo struct {
		Name      string `json:"name"`
		Path      string `json:"path"`
		IsDir     bool   `json:"isDir"`
		IsRegular bool   `json:"isRegular"` // true for regular files (not symlinks, sockets, etc.)
		Size      int64  `json:"size"`      // file size in bytes, or item count for directories
		ModTime   int64  `json:"modTime"`   // Unix timestamp
	}

	files := make([]FileInfo, 0, len(entries))

	// Add parent directory entry if not at root
	if dirPath != "/" {
		files = append(files, FileInfo{
			Name:  "..",
			Path:  filepath.Dir(dirPath),
			IsDir: true,
		})
	}

	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		fi := FileInfo{
			Name:      entry.Name(),
			Path:      filepath.Join(dirPath, entry.Name()),
			IsDir:     entry.IsDir(),
			IsRegular: info.Mode().IsRegular(),
			Size:      info.Size(),
			ModTime:   info.ModTime().Unix(),
		}

		// For directories, get item count instead of size
		if entry.IsDir() {
			if items, err := os.ReadDir(fi.Path); err == nil {
				fi.Size = int64(len(items))
			} else {
				fi.Size = 0
			}
		}

		files = append(files, fi)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"path":  dirPath,
		"files": files,
	})
}

// handleMarked handles marked files GET/POST/DELETE
func (s *Server) handleMarked(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		s.markedMu.RLock()
		files := s.markedFiles
		s.markedMu.RUnlock()
		json.NewEncoder(w).Encode(map[string]any{"files": files})

	case http.MethodPost:
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Clean and validate path
		filePath := filepath.Clean(req.Path)
		info, err := os.Stat(filePath)
		if err != nil {
			http.Error(w, "File not found: "+err.Error(), http.StatusNotFound)
			return
		}

		// Only allow regular files and directories
		if !info.IsDir() && !info.Mode().IsRegular() {
			http.Error(w, "Cannot mark this file type", http.StatusBadRequest)
			return
		}

		marked := MarkedFile{
			Path:    filePath,
			Name:    filepath.Base(filePath),
			Size:    info.Size(),
			ModTime: info.ModTime().Unix(),
			IsDir:   info.IsDir(),
		}

		s.markedMu.Lock()
		// Check if already marked (exact path)
		for _, f := range s.markedFiles {
			if f.Path == filePath {
				s.markedMu.Unlock()
				json.NewEncoder(w).Encode(map[string]any{"files": s.markedFiles, "added": false})
				return
			}
		}

		// Check for overlap: can't mark if a parent is already marked
		for _, f := range s.markedFiles {
			if f.IsDir && strings.HasPrefix(filePath, f.Path+string(filepath.Separator)) {
				s.markedMu.Unlock()
				http.Error(w, fmt.Sprintf("Parent directory %q is already marked", f.Name), http.StatusConflict)
				return
			}
		}

		// Check for overlap: can't mark directory if any children are already marked
		if info.IsDir() {
			for _, f := range s.markedFiles {
				if strings.HasPrefix(f.Path, filePath+string(filepath.Separator)) {
					s.markedMu.Unlock()
					http.Error(w, fmt.Sprintf("Child %q is already marked; unmark it first", f.Name), http.StatusConflict)
					return
				}
			}
		}

		s.markedFiles = append(s.markedFiles, marked)
		files := s.markedFiles
		s.markedMu.Unlock()

		// Notify subscribers
		s.notifyMarkedSubscribers()

		json.NewEncoder(w).Encode(map[string]any{"files": files, "added": true})

	case http.MethodDelete:
		// Check for specific file to unmark or clear all
		path := r.URL.Query().Get("path")

		s.markedMu.Lock()
		if path != "" {
			// Remove specific file
			path = filepath.Clean(path)
			newFiles := make([]MarkedFile, 0, len(s.markedFiles))
			for _, f := range s.markedFiles {
				if f.Path != path {
					newFiles = append(newFiles, f)
				}
			}
			s.markedFiles = newFiles
		} else {
			// Clear all
			s.markedFiles = make([]MarkedFile, 0)
		}
		files := s.markedFiles
		s.markedMu.Unlock()

		// Notify subscribers
		s.notifyMarkedSubscribers()

		json.NewEncoder(w).Encode(map[string]any{"files": files})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleMarkedEvents provides SSE stream for marked files updates
func (s *Server) handleMarkedEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable reverse proxy buffering (nginx)

	// Create channel for this subscriber
	ch := make(chan string, 10)
	s.markedSubMu.Lock()
	s.markedSubs[ch] = struct{}{}
	s.markedSubMu.Unlock()

	defer func() {
		s.markedSubMu.Lock()
		delete(s.markedSubs, ch)
		s.markedSubMu.Unlock()
		close(ch)
	}()

	// Send current state immediately
	s.markedMu.RLock()
	files := s.markedFiles
	s.markedMu.RUnlock()

	data, _ := json.Marshal(map[string]any{"type": "init", "files": files})
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()

	// Stream updates
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
			s.markedMu.RLock()
			files := s.markedFiles
			s.markedMu.RUnlock()
			data, _ := json.Marshal(map[string]any{"type": "update", "files": files})
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// notifyMarkedSubscribers notifies all SSE subscribers of marked files changes
func (s *Server) notifyMarkedSubscribers() {
	s.markedSubMu.Lock()
	defer s.markedSubMu.Unlock()

	for ch := range s.markedSubs {
		select {
		case ch <- "update":
		default:
			// Skip if channel is full
		}
	}
}

// handleMarkedDownload handles downloading marked files (single or zipped)
func (s *Server) handleMarkedDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check for specific path to download (single item from marked list)
	specificPath := r.URL.Query().Get("path")
	if specificPath != "" {
		specificPath = filepath.Clean(specificPath)
	}

	s.markedMu.RLock()
	var files []MarkedFile
	if specificPath != "" {
		// Find the specific marked file
		for _, f := range s.markedFiles {
			if f.Path == specificPath {
				files = []MarkedFile{f}
				break
			}
		}
	} else {
		files = make([]MarkedFile, len(s.markedFiles))
		copy(files, s.markedFiles)
	}
	s.markedMu.RUnlock()

	if len(files) == 0 {
		if specificPath != "" {
			http.Error(w, "File not in marked list", http.StatusNotFound)
		} else {
			http.Error(w, "No files marked", http.StatusBadRequest)
		}
		return
	}

	// Single regular file - direct download (no zip needed)
	if len(files) == 1 && !files[0].IsDir {
		file := files[0]

		// Remove from marked list
		s.markedMu.Lock()
		newFiles := make([]MarkedFile, 0, len(s.markedFiles)-1)
		for _, f := range s.markedFiles {
			if f.Path != file.Path {
				newFiles = append(newFiles, f)
			}
		}
		s.markedFiles = newFiles
		s.markedMu.Unlock()
		s.notifyMarkedSubscribers()

		// Serve file
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", file.Name))
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeFile(w, r, file.Path)
		return
	}

	// Multiple files or directory - create zip
	var zipName string
	if len(files) == 1 && files[0].IsDir {
		// Single directory: name.zip
		zipName = files[0].Name + ".zip"
	} else {
		// Multiple items: generate hash-based name
		h := sha256.New()
		for _, f := range files {
			h.Write([]byte(f.Path))
		}
		hashStr := hex.EncodeToString(h.Sum(nil))[:8]
		zipName = fmt.Sprintf("download-%s.zip", hashStr)
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", zipName))
	w.Header().Set("Content-Type", "application/zip")

	// Create zip writer directly to response
	zw := zip.NewWriter(w)
	defer zw.Close()

	// Track which marked items we successfully added
	addedPaths := make([]string, 0, len(files))

	// Helper to add a single file to the zip
	addFileToZip := func(filePath, zipPath string) error {
		info, err := os.Stat(filePath)
		if err != nil {
			return err
		}
		// Skip non-regular files (symlinks, etc.)
		if !info.Mode().IsRegular() {
			return nil
		}

		f, err := os.Open(filePath)
		if err != nil {
			return err
		}
		defer f.Close()

		header := &zip.FileHeader{
			Name:   zipPath,
			Method: zip.Deflate,
		}
		header.Modified = info.ModTime()

		zf, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}

		_, err = io.Copy(zf, f)
		return err
	}

	// Helper to recursively add a directory to the zip
	addDirToZip := func(dirPath, baseInZip string) error {
		return filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				log.Printf("Error walking %s: %v", path, err)
				return nil // Continue walking
			}

			// Get relative path from dirPath
			relPath, err := filepath.Rel(dirPath, path)
			if err != nil {
				return nil
			}
			zipPath := filepath.Join(baseInZip, relPath)

			if info.IsDir() {
				// Add directory entry (with trailing slash)
				if relPath != "." {
					header := &zip.FileHeader{
						Name:   zipPath + "/",
						Method: zip.Store,
					}
					header.Modified = info.ModTime()
					_, err := zw.CreateHeader(header)
					if err != nil {
						log.Printf("Failed to create dir entry %s: %v", zipPath, err)
					}
				}
				return nil
			}

			// Skip non-regular files
			if !info.Mode().IsRegular() {
				return nil
			}

			if err := addFileToZip(path, zipPath); err != nil {
				log.Printf("Failed to add %s to zip: %v", path, err)
			}
			return nil
		})
	}

	// Build unique zip paths for each marked item to avoid collisions
	// If names collide, prepend parent directory names until unique
	zipPaths := make(map[string]string) // file.Path -> zipPath
	usedNames := make(map[string]bool)

	for _, file := range files {
		zipPath := file.Name
		fullPath := file.Path

		// Keep prepending parent dirs until unique
		for usedNames[zipPath] {
			parent := filepath.Dir(fullPath)
			if parent == "/" || parent == "." || parent == fullPath {
				// Can't go further up, add numeric suffix
				base := file.Name
				ext := filepath.Ext(base)
				name := strings.TrimSuffix(base, ext)
				for i := 2; ; i++ {
					zipPath = fmt.Sprintf("%s (%d)%s", name, i, ext)
					if !usedNames[zipPath] {
						break
					}
				}
				break
			}
			zipPath = filepath.Join(filepath.Base(parent), zipPath)
			fullPath = parent
		}
		usedNames[zipPath] = true
		zipPaths[file.Path] = zipPath
	}

	for _, file := range files {
		zipPath := zipPaths[file.Path]
		if file.IsDir {
			// Add directory contents
			if err := addDirToZip(file.Path, zipPath); err != nil {
				log.Printf("Failed to add directory %s to zip: %v", file.Path, err)
				continue
			}
		} else {
			// Add single file
			if err := addFileToZip(file.Path, zipPath); err != nil {
				log.Printf("Failed to add file %s to zip: %v", file.Path, err)
				continue
			}
		}
		addedPaths = append(addedPaths, file.Path)
	}

	// Remove successfully downloaded items from marked list
	s.markedMu.Lock()
	newFiles := make([]MarkedFile, 0)
	for _, f := range s.markedFiles {
		if !slices.Contains(addedPaths, f.Path) {
			newFiles = append(newFiles, f)
		}
	}
	s.markedFiles = newFiles
	s.markedMu.Unlock()
	s.notifyMarkedSubscribers()
}

func main() {
	// Set up rotating log writer (9999 lines max, stored in system temp dir)
	var err error
	logWriter, err = NewRotatingLogWriter(9999)
	if err != nil {
		// Fall back to stdout only if we can't create log file
		fmt.Fprintf(os.Stderr, "Warning: could not create log file: %v\n", err)
	} else {
		log.SetOutput(logWriter)
		log.SetFlags(log.Ldate | log.Ltime)
		defer logWriter.Close()
	}

	// Configuration via flags
	defaultUploadDir := filepath.Join(xdgDataHome(), "webmux", "uploads")

	// Default shell: flag > $SHELL > /bin/bash
	defaultShell := os.Getenv("SHELL")
	if defaultShell == "" {
		defaultShell = "/bin/bash"
	}

	port := flag.String("port", "8080", "HTTP server port")
	panePortStart := flag.Int("pane-port-start", 7700, "Starting port for managed pane backends")
	shell := flag.String("shell", defaultShell, "Shell to spawn in terminals")
	uploadDir := flag.String("upload-dir", defaultUploadDir, "Directory for uploaded files")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: webmux [options] [directory]\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nSee 'man webmux' for more details.\n")
	}
	flag.Parse()

	// Get starting directory from first positional argument, default to current dir
	workDir, _ := os.Getwd()
	if flag.NArg() > 0 {
		argDir := flag.Arg(0)
		// Resolve to absolute path
		if !filepath.IsAbs(argDir) {
			argDir = filepath.Join(workDir, argDir)
		}
		// Verify it exists and is a directory
		if info, err := os.Stat(argDir); err != nil {
			log.Fatalf("Invalid directory: %s: %v", argDir, err)
		} else if !info.IsDir() {
			log.Fatalf("Not a directory: %s", argDir)
		}
		workDir = argDir
	}

	// Check for required dependencies
	if _, err := exec.LookPath("ttyd"); err != nil {
		log.Fatal("ttyd not found in PATH. Please install ttyd: https://github.com/tsl0922/ttyd")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		log.Fatal("tmux not found in PATH. Please install tmux: https://github.com/tmux/tmux")
	}

	// Create upload directory
	os.MkdirAll(*uploadDir, 0755)

	// Initialize pane manager
	manager := NewPaneManager(*panePortStart, *shell, workDir, *port)
	server := NewServer(manager, *uploadDir)

	// Cleanup on exit
	defer manager.Cleanup()

	// Handle signals for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("Shutting down...")
		manager.Cleanup()
		os.Exit(0)
	}()

	// Set up routes
	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("/api/info", server.handleInfo)
	mux.HandleFunc("/api/logs", server.handleLogs)
	mux.HandleFunc("/api/panes", server.handlePanes)
	mux.HandleFunc("/api/panes/", server.handlePane)
	mux.HandleFunc("/api/upload", server.handleUpload)
	mux.HandleFunc("/api/download", server.handleDownload)
	mux.HandleFunc("/api/browse", server.handleBrowse)
	mux.HandleFunc("/api/settings", server.handleSettings)
	mux.HandleFunc("/api/ui-state", server.handleUIState)
	mux.HandleFunc("/api/pane-storage/", server.handlePaneStorage)
	mux.HandleFunc("/api/storage/", server.handleStorageAdmin)
	mux.HandleFunc("/api/scratch", server.handleScratch)
	mux.HandleFunc("/api/scratch/events", server.handleScratchEvents)
	mux.HandleFunc("/api/marked", server.handleMarked)
	mux.HandleFunc("/api/marked/events", server.handleMarkedEvents)
	mux.HandleFunc("/api/marked/download", server.handleMarkedDownload)
	mux.HandleFunc("/api/clipboard", server.handleClipboard)
	mux.HandleFunc("/api/clipboard/version", server.handleClipboardVersion)

	// Pane proxy - forwards requests to pane backends
	mux.HandleFunc("/p/", server.handlePaneProxy)

	// Static files (dev mode handled by build tag)
	mux.Handle("/", InitDevMode(mux, server))

	log.Printf("Starting server on http://localhost:%s", *port)
	log.Printf("Instance ID: %s", manager.instanceID)
	log.Printf("Working directory: %s", workDir)
	log.Printf("Upload directory: %s", *uploadDir)
	log.Printf("Default shell: %s", *shell)

	if err := http.ListenAndServe(":"+*port, mountPathHandler(mux)); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func mountPathHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/p/") {
			next.ServeHTTP(w, r)
			return
		}

		for _, marker := range []string{"/api/", "/p/"} {
			if idx := strings.Index(path, marker); idx > 0 {
				r2 := r.Clone(r.Context())
				r2.URL.Path = path[idx:]
				r2.URL.RawPath = ""
				next.ServeHTTP(w, r2)
				return
			}
		}

		if path != "/" && !strings.HasSuffix(path, "/") && (filepath.Ext(path) == "" || strings.HasSuffix(filepath.Base(path), ".http")) {
			target := *r.URL
			target.Path = path + "/"
			http.Redirect(w, r, target.String(), http.StatusMovedPermanently)
			return
		}

		if path != "/" && strings.HasSuffix(path, "/") {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			r2.URL.RawPath = ""
			next.ServeHTTP(w, r2)
			return
		}

		if base := filepath.Base(path); base != "." && base != "/" && filepath.Ext(base) != "" && path != "/"+base {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/" + base
			r2.URL.RawPath = ""
			next.ServeHTTP(w, r2)
			return
		}

		if path != "/" && filepath.Ext(path) == "" {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			r2.URL.RawPath = ""
			next.ServeHTTP(w, r2)
			return
		}

		next.ServeHTTP(w, r)
	})
}
