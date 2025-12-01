/* *
 * Webmux - a browser-based terminal multiplexer
 * Copyright (C) 2025  Webmux contributors
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
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

//go:embed static/*
var staticFiles embed.FS

// Session represents a terminal session backed by tmux + ttyd
type Session struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Port           int       `json:"port"`
	CreatedAt      time.Time `json:"createdAt"`
	CurrentProcess string    `json:"currentProcess,omitempty"`
	tmuxSession    string    // tmux session name (e.g., "mux-7701")
	ttydCmd        *exec.Cmd // current ttyd process (restarts if it exits while tmux persists)
}

// Settings represents user-configurable settings
type Settings struct {
	// Multiplexer UI colors
	UI UIColors `json:"ui"`
	// Terminal colors
	Terminal TerminalColors `json:"terminal"`
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

// xdgStateHome returns XDG_STATE_HOME or ~/.local/state
func xdgStateHome() string {
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

// SessionManager handles multiple ttyd sessions
type SessionManager struct {
	sessions       map[string]*Session
	mu             sync.RWMutex
	nextPort       int32
	nextNameNum    int32 // Atomic counter for default session names
	shell          string
	workDir        string // Starting directory for new sessions
	tmuxConfigPath string
	wmBinDir       string           // Directory containing wm binary (added to PATH)
	getSettings    func() *Settings // Function to get current settings
	serverPort     string           // HTTP server port for WEBMUX_PORT env var
}

// NewSessionManager creates a new session manager
func NewSessionManager(startPort int, shell, workDir, serverPort string) *SessionManager {
	sm := &SessionManager{
		sessions:   make(map[string]*Session),
		nextPort:   int32(startPort),
		shell:      shell,
		workDir:    workDir,
		serverPort: serverPort,
	}

	// Extract tmux config to temp file
	tmuxConf, err := staticFiles.ReadFile("static/tmux.conf")
	if err != nil {
		log.Printf("Warning: could not read tmux.conf: %v", err)
	} else {
		tmpFile, err := os.CreateTemp("", "mux-tmux-*.conf")
		if err != nil {
			log.Printf("Warning: could not create temp file for tmux config: %v", err)
		} else {
			tmpFile.Write(tmuxConf)
			tmpFile.Close()
			sm.tmuxConfigPath = tmpFile.Name()
			log.Printf("Using custom tmux config: %s", sm.tmuxConfigPath)
		}
	}

	// Extract wm binary to temp directory (makes it available in terminal PATH)
	wmBin, err := staticFiles.ReadFile("static/wm")
	if err != nil {
		log.Printf("Warning: could not read embedded wm binary: %v", err)
	} else {
		tmpDir, err := os.MkdirTemp("", "webmux-bin-*")
		if err != nil {
			log.Printf("Warning: could not create temp dir for wm: %v", err)
		} else {
			wmPath := filepath.Join(tmpDir, "wm")
			if err := os.WriteFile(wmPath, wmBin, 0755); err != nil {
				log.Printf("Warning: could not write wm binary: %v", err)
				os.RemoveAll(tmpDir)
			} else {
				sm.wmBinDir = tmpDir
				log.Printf("Extracted wm binary to: %s", wmPath)
			}
		}
	}

	return sm
}

// tmuxSocketPath returns the path to our dedicated tmux socket
func (sm *SessionManager) tmuxSocketPath() string {
	// Use XDG_RUNTIME_DIR if available (per-user tmp), otherwise /tmp with uid
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "webmux-tmux.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("webmux-tmux-%d.sock", os.Getuid()))
}

// sessionEnvArgs returns tmux -e arguments for setting session environment variables
func (sm *SessionManager) sessionEnvArgs() []string {
	var args []string

	// Add WEBMUX_PORT so wm CLI knows which server to talk to
	args = append(args, "-e", "WEBMUX_PORT="+sm.serverPort)

	// Set $wm to the path of the wm binary - users can run "$wm info", "$wm ls", etc.
	if sm.wmBinDir != "" {
		args = append(args, "-e", "wm="+filepath.Join(sm.wmBinDir, "wm"))
	}

	return args
}

// CreateSession spawns a new tmux session with ttyd attached
func (sm *SessionManager) CreateSession(name string) (*Session, error) {
	port := int(atomic.AddInt32(&sm.nextPort, 1))
	id := fmt.Sprintf("session-%d", port)
	tmuxSession := fmt.Sprintf("mux-%d", port)

	if name == "" {
		nameNum := atomic.AddInt32(&sm.nextNameNum, 1)
		name = fmt.Sprintf("%d", nameNum)
	}

	tmuxSocket := sm.tmuxSocketPath()

	// Build tmux command with our custom config
	// -S: socket path, -f: config file, -d: detached, -s: session name, -x/-y: initial size, -c: start dir
	// -e: environment variables for the session
	tmuxArgs := []string{"-S", tmuxSocket}
	if sm.tmuxConfigPath != "" {
		tmuxArgs = append(tmuxArgs, "-f", sm.tmuxConfigPath)
	}
	tmuxArgs = append(tmuxArgs, "new-session", "-d", "-s", tmuxSession, "-x", "200", "-y", "50")
	// Add environment variables (-e must come after new-session)
	tmuxArgs = append(tmuxArgs, sm.sessionEnvArgs()...)
	if sm.workDir != "" {
		tmuxArgs = append(tmuxArgs, "-c", sm.workDir)
	}
	tmuxArgs = append(tmuxArgs, sm.shell)

	tmuxCmd := exec.Command("tmux", tmuxArgs...)
	tmuxCmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if out, err := tmuxCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to create tmux session: %w: %s", err, string(out))
	}

	// Wait for tmux session to be ready
	for range 50 {
		checkCmd := exec.Command("tmux", "-S", tmuxSocket, "has-session", "-t", tmuxSession)
		if checkCmd.Run() == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	session := &Session{
		ID:          id,
		Name:        name,
		Port:        port,
		CreatedAt:   time.Now(),
		tmuxSession: tmuxSession,
	}

	// Start ttyd attached to the tmux session (must be called without lock)
	if err := sm.startTtyd(session); err != nil {
		// Clean up tmux session
		exec.Command("tmux", "-S", tmuxSocket, "kill-session", "-t", tmuxSession).Run()
		return nil, err
	}

	// Add to sessions map
	sm.mu.Lock()
	sm.sessions[id] = session
	sm.mu.Unlock()

	// Monitor tmux session to detect when shell exits
	go sm.monitorSession(session)

	log.Printf("Created session %s on port %d", id, port)
	return session, nil
}

// startTtyd starts a ttyd process attached to the session's tmux session
// NOTE: This must be called WITHOUT holding sm.mu lock
func (sm *SessionManager) startTtyd(session *Session) error {
	tmuxSocket := sm.tmuxSocketPath()
	tmuxSession := session.tmuxSession

	// Get terminal colors from settings
	var termColors TerminalColors
	if sm.getSettings != nil {
		termColors = sm.getSettings().Terminal
	} else {
		termColors = DefaultSettings().Terminal
	}

	// Build theme JSON for ttyd using Base24 mapping
	// ttyd xterm.js theme format -> Base24 mapping:
	// background=base00, foreground=base05, cursor=base06, cursorAccent=base00
	// selection=base02, black=base03, red=base08, green=base0B, yellow=base0A
	// blue=base0D, magenta=base0E, cyan=base0C, white=base06
	// brightBlack=base04, brightRed=base12, brightGreen=base14, brightYellow=base13
	// brightBlue=base16, brightMagenta=base17, brightCyan=base15, brightWhite=base07
	themeJSON := fmt.Sprintf(`{"background":"%s","foreground":"%s","cursor":"%s","cursorAccent":"%s","selection":"%s","black":"%s","red":"%s","green":"%s","yellow":"%s","blue":"%s","magenta":"%s","cyan":"%s","white":"%s","brightBlack":"%s","brightRed":"%s","brightGreen":"%s","brightYellow":"%s","brightBlue":"%s","brightMagenta":"%s","brightCyan":"%s","brightWhite":"%s"}`,
		termColors.Base00, termColors.Base05, termColors.Base06, termColors.Base00,
		termColors.Base02, termColors.Base03, termColors.Base08, termColors.Base0B, termColors.Base0A,
		termColors.Base0D, termColors.Base0E, termColors.Base0C, termColors.Base06,
		termColors.Base04, termColors.Base12, termColors.Base14, termColors.Base13,
		termColors.Base16, termColors.Base17, termColors.Base15, termColors.Base07)

	// No --once: ttyd stays running and each client connection runs tmux attach
	// Multiple tmux attach calls to the same session share the view
	args := []string{
		"--port", strconv.Itoa(session.Port),
		"--writable",
		"--client-option", "fontSize=14",
		"--client-option", "fontFamily=JetBrains Mono,Fira Code,SF Mono,Menlo,Monaco,Courier New,monospace",
		"--client-option", "theme=" + themeJSON,
		"--client-option", "disableLeaveAlert=true",
		"--client-option", "scrollback=50000",
		"--client-option", "allowProposedApi=true",
		"--client-option", "rightClickSelectsWord=true",
	}

	// Build tmux attach command with our config
	tmuxArgs := []string{"-S", tmuxSocket}
	if sm.tmuxConfigPath != "" {
		tmuxArgs = append(tmuxArgs, "-f", sm.tmuxConfigPath)
	}
	tmuxArgs = append(tmuxArgs, "attach-session", "-t", tmuxSession)

	args = append(args, "tmux")
	args = append(args, tmuxArgs...)

	cmd := exec.Command("ttyd", args...)
	// Don't inherit stdout/stderr to avoid echoing to parent terminal
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start ttyd: %w", err)
	}

	session.ttydCmd = cmd

	// Monitor ttyd process and restart when client disconnects
	go sm.handleTtydExit(session, cmd)

	// Wait for ttyd to be ready (port accepting connections)
	addr := fmt.Sprintf("127.0.0.1:%d", session.Port)
	for range 50 {
		conn, err := net.DialTimeout("tcp", addr, 10*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// handleTtydExit handles ttyd process exit and restarts for reconnection
func (sm *SessionManager) handleTtydExit(session *Session, cmd *exec.Cmd) {
	cmd.Wait()

	sm.mu.Lock()
	// Check if session still exists
	s, ok := sm.sessions[session.ID]
	if !ok {
		sm.mu.Unlock()
		return
	}

	// Check if tmux session still exists
	tmuxSocket := sm.tmuxSocketPath()
	checkCmd := exec.Command("tmux", "-S", tmuxSocket, "has-session", "-t", session.tmuxSession)
	if err := checkCmd.Run(); err != nil {
		// tmux session is gone, clean up
		log.Printf("Session %s: tmux session exited, cleaning up", session.ID)
		delete(sm.sessions, session.ID)
		sm.mu.Unlock()
		return
	}

	log.Printf("Session %s: ttyd exited, restarting for reconnection...", session.ID)
	sm.mu.Unlock()

	// Restart ttyd (outside of lock)
	if err := sm.startTtyd(s); err != nil {
		log.Printf("Session %s: failed to restart ttyd: %v", session.ID, err)
		sm.mu.Lock()
		delete(sm.sessions, session.ID)
		sm.mu.Unlock()
	}
}

// monitorSession watches the tmux session to detect when the shell exits
// and updates the current foreground process
func (sm *SessionManager) monitorSession(session *Session) {
	tmuxSocket := sm.tmuxSocketPath()

	for {
		sm.mu.RLock()
		s, ok := sm.sessions[session.ID]
		if !ok {
			sm.mu.RUnlock()
			return
		}
		tmuxSession := s.tmuxSession
		sm.mu.RUnlock()

		// Check if tmux session still exists
		checkCmd := exec.Command("tmux", "-S", tmuxSocket, "has-session", "-t", tmuxSession)
		if err := checkCmd.Run(); err != nil {
			log.Printf("Session %s: tmux session exited, cleaning up", session.ID)
			// Kill ttyd process if running
			sm.mu.Lock()
			if s, ok := sm.sessions[session.ID]; ok {
				if s.ttydCmd != nil && s.ttydCmd.Process != nil {
					s.ttydCmd.Process.Kill()
				}
				delete(sm.sessions, session.ID)
			}
			sm.mu.Unlock()
			return
		}

		// Update current foreground process
		proc := sm.getForegroundProcess(tmuxSession)
		sm.mu.Lock()
		if s, ok := sm.sessions[session.ID]; ok {
			s.CurrentProcess = proc
		}
		sm.mu.Unlock()

		time.Sleep(2 * time.Second)
	}
}

// getForegroundProcess returns the name of the foreground process in the terminal
func (sm *SessionManager) getForegroundProcess(tmuxSession string) string {
	tmuxSocket := sm.tmuxSocketPath()

	// Use tmux to get the current command in the pane
	out, err := exec.Command("tmux", "-S", tmuxSocket, "display-message", "-p", "-t", tmuxSession, "#{pane_current_command}").Output()
	if err != nil {
		return ""
	}

	procName := strings.TrimSpace(string(out))

	return procName
}

// GetSession returns a session by ID
func (sm *SessionManager) GetSession(id string) (*Session, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	s, ok := sm.sessions[id]
	return s, ok
}

// ListSessions returns all active sessions
func (sm *SessionManager) ListSessions() []*Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	sessions := make([]*Session, 0, len(sm.sessions))
	for _, s := range sm.sessions {
		sessions = append(sessions, s)
	}
	return sessions
}

// CloseSession terminates a ttyd session
func (sm *SessionManager) CloseSession(id string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[id]
	if !ok {
		return fmt.Errorf("session not found: %s", id)
	}

	// Kill ttyd process
	if session.ttydCmd != nil && session.ttydCmd.Process != nil {
		session.ttydCmd.Process.Kill()
	}

	// Kill tmux session
	if session.tmuxSession != "" {
		tmuxSocket := sm.tmuxSocketPath()
		exec.Command("tmux", "-S", tmuxSocket, "kill-session", "-t", session.tmuxSession).Run()
	}

	delete(sm.sessions, id)
	log.Printf("Closed session %s", id)
	return nil
}

// RenameSession changes the display name of a session
func (sm *SessionManager) RenameSession(id, name string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[id]
	if !ok {
		return fmt.Errorf("session not found: %s", id)
	}

	session.Name = name
	return nil
}

// Cleanup terminates all sessions
func (sm *SessionManager) Cleanup() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	tmuxSocket := sm.tmuxSocketPath()

	for id, session := range sm.sessions {
		if session.ttydCmd != nil && session.ttydCmd.Process != nil {
			session.ttydCmd.Process.Kill()
		}
		if session.tmuxSession != "" {
			exec.Command("tmux", "-S", tmuxSocket, "kill-session", "-t", session.tmuxSession).Run()
		}
		log.Printf("Cleaned up session %s", id)
	}
	sm.sessions = make(map[string]*Session)

	// Kill the entire tmux server on our socket
	exec.Command("tmux", "-S", tmuxSocket, "kill-server").Run()

	// Clean up temp files
	if sm.tmuxConfigPath != "" {
		os.Remove(sm.tmuxConfigPath)
	}
	if sm.wmBinDir != "" {
		os.RemoveAll(sm.wmBinDir)
	}
}

// MarkedFile represents a file or directory marked for download
type MarkedFile struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime"`
	IsDir   bool   `json:"isDir"`
}

// Server holds the HTTP server and session manager
type Server struct {
	manager      *SessionManager
	uploadDir    string
	settings     *Settings
	settingsMu   sync.RWMutex
	scratchText  string
	scratchMu    sync.RWMutex
	scratchSubs  map[chan string]struct{} // SSE subscribers
	scratchSubMu sync.Mutex
	markedFiles  []MarkedFile // Files marked for download
	markedMu     sync.RWMutex
	markedSubs   map[chan string]struct{} // SSE subscribers for marked files
	markedSubMu  sync.Mutex
}

// NewServer creates a new server instance
func NewServer(manager *SessionManager, uploadDir string) *Server {
	s := &Server{
		manager:     manager,
		uploadDir:   uploadDir,
		settings:    LoadSettings(),
		scratchSubs: make(map[chan string]struct{}),
		markedFiles: make([]MarkedFile, 0),
		markedSubs:  make(map[chan string]struct{}),
	}
	// Wire up settings getter for session manager
	manager.getSettings = func() *Settings {
		s.settingsMu.RLock()
		defer s.settingsMu.RUnlock()
		return s.settings
	}
	return s
}

// handleInfo returns server configuration info
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	sessions := s.manager.ListSessions()

	json.NewEncoder(w).Encode(map[string]any{
		"workDir":      s.manager.workDir,
		"uploadDir":    s.uploadDir,
		"shell":        s.manager.shell,
		"port":         s.manager.serverPort,
		"sessionCount": len(sessions),
		"tmuxSocket":   s.manager.tmuxSocketPath(),
	})
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

// handleSessions handles session CRUD operations
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		// List all sessions
		sessions := s.manager.ListSessions()
		json.NewEncoder(w).Encode(sessions)

	case http.MethodPost:
		// Create new session
		var req struct {
			Name string `json:"name"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		session, err := s.manager.CreateSession(req.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(session)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleSession handles operations on a specific session
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	// Extract session ID from path: /api/sessions/{id}
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	sessionID := parts[3]

	switch r.Method {
	case http.MethodDelete:
		if err := s.manager.CloseSession(sessionID); err != nil {
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
		if err := s.manager.RenameSession(sessionID, req.Name); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
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
		s.downloadDirAsZip(w, filePath, info)
		return
	}

	// Regular file - direct download
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(filePath)))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))

	http.ServeFile(w, r, filePath)
}

// downloadDirAsZip streams a directory as a zip file
func (s *Server) downloadDirAsZip(w http.ResponseWriter, dirPath string, info os.FileInfo) {
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
				header.SetModTime(fi.ModTime())
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
		header.SetModTime(fi.ModTime())

		zf, err := zw.CreateHeader(header)
		if err != nil {
			return nil
		}

		io.Copy(zf, f)
		return nil
	})
}

// clipboardScript is injected into ttyd's HTML to enable OSC 52 clipboard copy
// Paste is handled natively by xterm.js via Ctrl+Shift+V
const clipboardScript = `<script>
(function() {
    // Wait for terminal to initialize, then set up OSC 52 handler for copy
    var checkTerm = setInterval(function() {
        if (window.term && window.term.terminal) {
            clearInterval(checkTerm);
            var terminal = window.term.terminal;
            
            // Register OSC 52 handler for copy (used by tmux set-clipboard)
            if (terminal.parser && terminal.parser.registerOscHandler) {
                terminal.parser.registerOscHandler(52, function(data) {
                    // OSC 52 format: "<selection>;<base64-text>"
                    var parts = data.split(';');
                    if (parts.length >= 2) {
                        var base64Text = parts.slice(1).join(';');
                        if (base64Text && base64Text !== '?') {
                            try {
                                var text = atob(base64Text);
                                navigator.clipboard.writeText(text);
                            } catch (e) {}
                        }
                    }
                    return true;
                });
            }
        }
    }, 100);
})();
</script></body>`

// handleTerminalProxy proxies all HTTP requests to the appropriate ttyd instance
// Path format: /t/{sessionID}/...
func (s *Server) handleTerminalProxy(w http.ResponseWriter, r *http.Request) {
	// Extract session ID from path: /t/{sessionID}/...
	path := strings.TrimPrefix(r.URL.Path, "/t/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "Invalid terminal path", http.StatusBadRequest)
		return
	}
	sessionID := parts[0]

	session, ok := s.manager.GetSession(sessionID)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	// Build the target URL
	targetURL := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("127.0.0.1:%d", session.Port),
	}

	// Create reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// Modify the request
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		// Strip the /t/{sessionID} prefix from the path
		if len(parts) > 1 {
			req.URL.Path = "/" + parts[1]
		} else {
			req.URL.Path = "/"
		}
		req.URL.RawPath = ""
		req.Host = targetURL.Host
	}

	// For HTML responses (the ttyd index), inject our clipboard script
	isIndexRequest := len(parts) == 1 || parts[1] == "" || parts[1] == "index.html"
	if isIndexRequest {
		proxy.ModifyResponse = func(resp *http.Response) error {
			if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
				return nil
			}

			// Read the body
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				return err
			}

			// Inject our script before </body>
			content := strings.Replace(string(body), "</body>", clipboardScript, 1)

			// Update the response
			resp.Body = io.NopCloser(strings.NewReader(content))
			resp.ContentLength = int64(len(content))
			resp.Header.Set("Content-Length", strconv.Itoa(len(content)))

			return nil
		}
	}

	proxy.ServeHTTP(w, r)
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
		header.SetModTime(info.ModTime())

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
					header.SetModTime(info.ModTime())
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
					zipPath = fmt.Sprintf("%s_%d%s", name, i, ext)
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
		found := false
		for _, p := range addedPaths {
			if f.Path == p {
				found = true
				break
			}
		}
		if !found {
			newFiles = append(newFiles, f)
		}
	}
	s.markedFiles = newFiles
	s.markedMu.Unlock()
	s.notifyMarkedSubscribers()
}

func main() {
	// Configuration via flags
	defaultUploadDir := filepath.Join(xdgDataHome(), "webmux", "uploads")

	// Default shell: flag > $SHELL > /bin/bash
	defaultShell := os.Getenv("SHELL")
	if defaultShell == "" {
		defaultShell = "/bin/bash"
	}

	port := flag.String("port", "8080", "HTTP server port")
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

	// Initialize session manager (ttyd sessions start at port 7700)
	manager := NewSessionManager(7700, *shell, workDir, *port)
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
	mux.HandleFunc("/api/sessions", server.handleSessions)
	mux.HandleFunc("/api/sessions/", server.handleSession)
	mux.HandleFunc("/api/upload", server.handleUpload)
	mux.HandleFunc("/api/download", server.handleDownload)
	mux.HandleFunc("/api/browse", server.handleBrowse)
	mux.HandleFunc("/api/settings", server.handleSettings)
	mux.HandleFunc("/api/scratch", server.handleScratch)
	mux.HandleFunc("/api/scratch/events", server.handleScratchEvents)
	mux.HandleFunc("/api/marked", server.handleMarked)
	mux.HandleFunc("/api/marked/events", server.handleMarkedEvents)
	mux.HandleFunc("/api/marked/download", server.handleMarkedDownload)

	// Terminal proxy - forwards requests to ttyd instances
	mux.HandleFunc("/t/", server.handleTerminalProxy)

	// Static files (dev mode handled by build tag)
	mux.Handle("/", InitDevMode(mux, server))

	log.Printf("Starting server on :%s", *port)
	log.Printf("Working directory: %s", workDir)
	log.Printf("Upload directory: %s", *uploadDir)
	log.Printf("Default shell: %s", *shell)

	if err := http.ListenAndServe(":"+*port, mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
