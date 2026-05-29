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
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	shellinit "webmux/internal/shell"
)

// SECTION: TERMINAL RUNTIME

// TerminalPaneState stores runtime-only state for terminal panes.
type TerminalPaneState struct {
	tmuxSession string
	ttydCmd     *exec.Cmd
}

// TerminalRuntime owns tmux/ttyd lifecycle for terminal panes.
type TerminalRuntime struct {
	manager        *PaneManager
	states         map[string]*TerminalPaneState
	mu             sync.RWMutex
	tmuxConfigPath string
	wmBinDir       string // Directory containing wm binary (added to PATH)
}

// Display-related environment variables that can be forwarded to panes.
// These are connection variables that allow GUI apps to connect to the display server.
var displayEnvVars = []string{
	"DISPLAY",
	"WAYLAND_DISPLAY",
}

func (tr *TerminalRuntime) SetupResources() {
	tr.extractTmuxConfig()
	tr.extractWMBinary()
	tr.installShellScripts()
}

func (tr *TerminalRuntime) extractTmuxConfig() {
	tmuxConf, err := staticFiles.ReadFile("static/tmux.conf")
	if err != nil {
		log.Printf("Warning: could not read tmux.conf: %v", err)
		return
	}

	tmpFile, err := os.CreateTemp("", "mux-tmux-*.conf")
	if err != nil {
		log.Printf("Warning: could not create temp file for tmux config: %v", err)
		return
	}
	defer tmpFile.Close()

	if _, err := tmpFile.Write(tmuxConf); err != nil {
		log.Printf("Warning: could not write tmux config temp file: %v", err)
		return
	}
	tr.tmuxConfigPath = tmpFile.Name()
	log.Printf("Using custom tmux config: %s", tr.tmuxConfigPath)
}

func (tr *TerminalRuntime) extractWMBinary() {
	wmBin, err := staticFiles.ReadFile("static/wm")
	if err != nil {
		log.Printf("Warning: could not read embedded wm binary: %v", err)
		return
	}

	tmpDir, err := os.MkdirTemp("", "webmux-bin-*")
	if err != nil {
		log.Printf("Warning: could not create temp dir for wm: %v", err)
		return
	}

	wmPath := filepath.Join(tmpDir, "wm")
	if err := os.WriteFile(wmPath, wmBin, 0755); err != nil {
		log.Printf("Warning: could not write wm binary: %v", err)
		os.RemoveAll(tmpDir)
		return
	}

	tr.wmBinDir = tmpDir
	log.Printf("Extracted wm binary to: %s", wmPath)
}

func (tr *TerminalRuntime) installShellScripts() {
	if tr.wmBinDir == "" {
		return
	}

	wmPath := filepath.Join(tr.wmBinDir, "wm")
	initPath := filepath.Join(tr.wmBinDir, "init.sh")
	initContent := shellinit.InitScript(wmPath, tr.wmBinDir)
	if err := os.WriteFile(initPath, []byte(initContent), 0644); err != nil {
		log.Printf("Warning: could not write init script: %v", err)
	}

	for _, script := range shellinit.ClipboardWrapperScripts(wmPath) {
		scriptPath := filepath.Join(tr.wmBinDir, script.Name)
		if err := os.WriteFile(scriptPath, []byte(script.Content), 0755); err != nil {
			log.Printf("Warning: could not write %s wrapper: %v", script.Name, err)
		}
	}
}

func (tr *TerminalRuntime) tmuxSocketPathCreate() string {
	// Use XDG_DATA_HOME (~/.local/share) for the socket to avoid issues with
	// XDG_RUNTIME_DIR being cleaned up by systemd when user has no active panes
	// (which happens when accessing webmux only via web/VPN without a local login)
	socketDir := webmuxInstanceDir(tr.manager.instanceID)
	os.MkdirAll(socketDir, 0700)
	return filepath.Join(socketDir, "tmux.sock")
}

func (tr *TerminalRuntime) tmuxSocketPath() string {
	return filepath.Join(webmuxInstanceDir(tr.manager.instanceID), "tmux.sock")
}

// paneEnvArgs returns tmux -e arguments for setting pane environment variables
func (tr *TerminalRuntime) paneEnvArgs() []string {
	var args []string

	// Add WEBMUX_PORT so wm CLI knows which server to talk to
	args = append(args, "-e", "WEBMUX_PORT="+tr.manager.serverPort)

	// Set _wm_bin env var to the path of the wm binary (used by shell wrapper)
	if tr.wmBinDir != "" {
		args = append(args, "-e", "_wm_bin="+filepath.Join(tr.wmBinDir, "wm"))
	}

	return args
}

// Start creates the terminal backend for a pane.
func (tr *TerminalRuntime) Start(pane *Pane) error {
	tmuxSocket := tr.tmuxSocketPathCreate()
	tmuxSession := strings.Replace(pane.ID, "pane-", "mux-", 1)

	// Build tmux command with our custom config.
	// Each webmux terminal pane is backed by one tmux session.
	// -S: socket path, -f: config file, -d: detached, -s: session name, -x/-y: initial size, -c: start dir
	// -e: environment variables for the pane
	tmuxArgs := []string{"-S", tmuxSocket}
	if tr.tmuxConfigPath != "" {
		tmuxArgs = append(tmuxArgs, "-f", tr.tmuxConfigPath)
	}
	tmuxArgs = append(tmuxArgs, "new-session", "-d", "-s", tmuxSession, "-x", "200", "-y", "50")
	// Add environment variables (-e must come after new-session)
	tmuxArgs = append(tmuxArgs, tr.paneEnvArgs()...)
	// Add pane ID so wm CLI knows which pane it's in
	tmuxArgs = append(tmuxArgs, "-e", "WEBMUX_SESSION="+pane.ID)
	// Signal that OSC 52 clipboard is supported (webmux intercepts and handles it)
	// Apps can check this to enable OSC 52 clipboard integration
	tmuxArgs = append(tmuxArgs, "-e", "WEBMUX_CLIPBOARD=osc52")
	// Set COLORTERM to help apps detect modern terminal features
	tmuxArgs = append(tmuxArgs, "-e", "COLORTERM=truecolor")
	// Clear display environment variables by default (clean terminal pane)
	// We set them to a dummy value rather than empty, because some shell init
	// scripts check `[ -z "$DISPLAY" ]` to detect headless panes and may
	// try to start a display server if DISPLAY is empty
	for _, key := range displayEnvVars {
		tmuxArgs = append(tmuxArgs, "-e", key+"=none")
	}
	// Set WEBMUX_INIT to our init script path (defines wm function)
	if tr.wmBinDir != "" {
		initPath := filepath.Join(tr.wmBinDir, "init.sh")
		tmuxArgs = append(tmuxArgs, "-e", "WEBMUX_INIT="+initPath)
	}
	if tr.manager.workDir != "" {
		tmuxArgs = append(tmuxArgs, "-c", tr.manager.workDir)
	}
	// Determine how to inject our init based on shell type
	shellBase := filepath.Base(tr.manager.shell)
	if tr.wmBinDir != "" {
		initPath := filepath.Join(tr.wmBinDir, "init.sh")
		switch shellBase {
		case "bash":
			// bash: use --rcfile to source our init, which also sources user's .bashrc
			rcPath := filepath.Join(tr.wmBinDir, "bashrc")
			rcContent := fmt.Sprintf(`[ -f ~/.bashrc ] && . ~/.bashrc
. %s
`, initPath)
			os.WriteFile(rcPath, []byte(rcContent), 0644)
			tmuxArgs = append(tmuxArgs, tr.manager.shell, "--rcfile", rcPath)
		case "zsh":
			// zsh: use ZDOTDIR with custom rc files that source user's config then our init
			zdotdir := filepath.Join(tr.wmBinDir, "zsh")
			os.MkdirAll(zdotdir, 0755)
			// Create .zshenv that sources user's .zshenv (but keeps our ZDOTDIR)
			zshenvContent := `[ -f "$HOME/.zshenv" ] && . "$HOME/.zshenv"
`
			os.WriteFile(filepath.Join(zdotdir, ".zshenv"), []byte(zshenvContent), 0644)
			// Create .zprofile that sources user's .zprofile
			zprofileContent := `[ -f "$HOME/.zprofile" ] && . "$HOME/.zprofile"
`
			os.WriteFile(filepath.Join(zdotdir, ".zprofile"), []byte(zprofileContent), 0644)
			// Create .zshrc that sources user's .zshrc then our init
			zshrcContent := fmt.Sprintf(`[ -f "$HOME/.zshrc" ] && . "$HOME/.zshrc"
. %s
`, initPath)
			os.WriteFile(filepath.Join(zdotdir, ".zshrc"), []byte(zshrcContent), 0644)
			tmuxArgs = append(tmuxArgs, "-e", "ZDOTDIR="+zdotdir)
			tmuxArgs = append(tmuxArgs, tr.manager.shell)
		default:
			// Other shells: set ENV for POSIX compliance
			tmuxArgs = append(tmuxArgs, "-e", "ENV="+initPath)
			tmuxArgs = append(tmuxArgs, tr.manager.shell)
		}
	} else {
		tmuxArgs = append(tmuxArgs, tr.manager.shell)
	}

	tmuxCmd := exec.Command("tmux", tmuxArgs...)
	tmuxCmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if out, err := tmuxCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create tmux session: %w: %s", err, string(out))
	}

	// Wait for tmux session to be ready
	for range 50 {
		checkCmd := exec.Command("tmux", "-S", tmuxSocket, "has-session", "-t", tmuxSession)
		if checkCmd.Run() == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	state := &TerminalPaneState{tmuxSession: tmuxSession}
	tr.mu.Lock()
	tr.states[pane.ID] = state
	tr.mu.Unlock()

	// Start ttyd attached to the tmux session (must be called without lock)
	if err := tr.startTtyd(pane); err != nil {
		// Clean up tmux session
		exec.Command("tmux", "-S", tmuxSocket, "kill-session", "-t", tmuxSession).Run()
		tr.mu.Lock()
		delete(tr.states, pane.ID)
		tr.mu.Unlock()
		return err
	}

	return nil
}

// startTtyd starts a ttyd process attached to the pane's tmux session
// NOTE: This must be called WITHOUT holding manager.mu lock.
func (tr *TerminalRuntime) startTtyd(pane *Pane) error {
	tmuxSocket := tr.tmuxSocketPath()
	state, ok := tr.getState(pane.ID)
	if !ok {
		return fmt.Errorf("terminal state not found: %s", pane.ID)
	}
	tmuxSession := state.tmuxSession

	// Get terminal colors from settings
	var termColors TerminalColors
	if tr.manager.getSettings != nil {
		termColors = tr.manager.getSettings().Terminal
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
		"--port", strconv.Itoa(pane.Port),
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
	if tr.tmuxConfigPath != "" {
		tmuxArgs = append(tmuxArgs, "-f", tr.tmuxConfigPath)
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

	tr.mu.Lock()
	if currentState, ok := tr.states[pane.ID]; ok {
		currentState.ttydCmd = cmd
	}
	tr.mu.Unlock()

	// Monitor ttyd process and restart when client disconnects
	go tr.handleTtydExit(pane, cmd)

	// Wait for ttyd to be ready (port accepting connections)
	addr := fmt.Sprintf("127.0.0.1:%d", pane.Port)
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

func (tr *TerminalRuntime) getState(paneID string) (*TerminalPaneState, bool) {
	tr.mu.RLock()
	defer tr.mu.RUnlock()
	state, ok := tr.states[paneID]
	return state, ok
}

// handleTtydExit handles ttyd process exit and restarts for reconnection.
func (tr *TerminalRuntime) handleTtydExit(pane *Pane, cmd *exec.Cmd) {
	exitState := cmd.Wait()
	log.Printf("Pane %s: ttyd process exited with: %v", pane.ID, exitState)

	sm := tr.manager
	sm.mu.Lock()
	// Check if pane still exists
	s, ok := sm.panes[pane.ID]
	if !ok {
		log.Printf("Pane %s: already removed from panes map", pane.ID)
		sm.mu.Unlock()
		return
	}

	state, stateOK := tr.getState(pane.ID)
	if !stateOK {
		log.Printf("Pane %s: terminal state missing, cleaning up", pane.ID)
		sm.deletePane(pane.ID)
		if len(sm.panes) == 0 {
			sm.resetCounters()
		}
		sm.mu.Unlock()
		return
	}

	// Check if tmux session still exists
	tmuxSocket := tr.tmuxSocketPath()
	checkCmd := exec.Command("tmux", "-S", tmuxSocket, "has-session", "-t", state.tmuxSession)
	if err := checkCmd.Run(); err != nil {
		// tmux session is gone, clean up
		log.Printf("Pane %s: tmux session %s no longer exists, cleaning up", pane.ID, state.tmuxSession)
		tr.mu.Lock()
		delete(tr.states, pane.ID)
		tr.mu.Unlock()
		sm.deletePane(pane.ID)
		if len(sm.panes) == 0 {
			sm.resetCounters()
		}
		sm.mu.Unlock()
		return
	}

	log.Printf("Pane %s: ttyd exited but tmux session %s still exists, restarting ttyd...", pane.ID, state.tmuxSession)
	sm.mu.Unlock()

	// Restart ttyd (outside of lock)
	if err := tr.startTtyd(s); err != nil {
		log.Printf("Pane %s: failed to restart ttyd: %v", pane.ID, err)
		sm.mu.Lock()
		tr.mu.Lock()
		delete(tr.states, pane.ID)
		tr.mu.Unlock()
		sm.deletePane(pane.ID)
		if len(sm.panes) == 0 {
			sm.resetCounters()
		}
		sm.mu.Unlock()
	} else {
		log.Printf("Pane %s: ttyd restarted successfully", pane.ID)
	}
}

// monitorPane watches the tmux session to detect when the shell exits
// and updates the current foreground process
func (tr *TerminalRuntime) Monitor(pane *Pane) {
	sm := tr.manager
	tmuxSocket := tr.tmuxSocketPath()
	startTime := time.Now()
	checkCount := 0

	for {
		sm.mu.RLock()
		s, ok := sm.panes[pane.ID]
		if !ok {
			sm.mu.RUnlock()
			log.Printf("Pane %s: removed from panes map after %d checks (%v)", pane.ID, checkCount, time.Since(startTime))
			return
		}
		state, stateOK := tr.getState(s.ID)
		if !stateOK {
			sm.mu.RUnlock()
			log.Printf("Pane %s: terminal state missing after %d checks (%v)", pane.ID, checkCount, time.Since(startTime))
			return
		}
		tmuxSession := state.tmuxSession
		sm.mu.RUnlock()

		checkCount++

		// Check if tmux session still exists
		checkCmd := exec.Command("tmux", "-S", tmuxSocket, "has-session", "-t", tmuxSession)
		if err := checkCmd.Run(); err != nil {
			log.Printf("Pane %s: tmux session %s exited after %d checks (%v), cleaning up", pane.ID, tmuxSession, checkCount, time.Since(startTime))
			// Kill ttyd process if running
			sm.mu.Lock()
			if s, ok := sm.panes[pane.ID]; ok {
				if state, ok := tr.getState(s.ID); ok && state.ttydCmd != nil && state.ttydCmd.Process != nil {
					state.ttydCmd.Process.Kill()
				}
				tr.mu.Lock()
				delete(tr.states, pane.ID)
				tr.mu.Unlock()
				sm.deletePane(pane.ID)
			}
			if len(sm.panes) == 0 {
				sm.resetCounters()
			}
			sm.mu.Unlock()
			return
		}

		// Update current foreground process
		proc := tr.getForegroundProcess(tmuxSession)
		var paneCopy *Pane
		sm.mu.Lock()
		if s, ok := sm.panes[pane.ID]; ok {
			if s.CurrentProcess != proc {
				s.CurrentProcess = proc
				copy := *s
				paneCopy = &copy
			}
		}
		sm.mu.Unlock()
		if paneCopy != nil && sm.onPaneChanged != nil {
			sm.onPaneChanged(paneCopy)
		}

		time.Sleep(2 * time.Second)
	}
}

// getForegroundProcess returns the name of the foreground process in the terminal
func (tr *TerminalRuntime) getForegroundProcess(tmuxSession string) string {
	tmuxSocket := tr.tmuxSocketPath()

	// Use tmux to get the current command in the pane
	out, err := exec.Command("tmux", "-S", tmuxSocket, "display-message", "-p", "-t", tmuxSession, "#{pane_current_command}").Output()
	if err != nil {
		return ""
	}

	procName := strings.TrimSpace(string(out))

	return procName
}

// Stop terminates a terminal pane backend.
func (tr *TerminalRuntime) Stop(pane *Pane) {
	state, ok := tr.getState(pane.ID)
	if !ok {
		return
	}
	if state.ttydCmd != nil && state.ttydCmd.Process != nil {
		state.ttydCmd.Process.Kill()
	}
	if state.tmuxSession != "" {
		exec.Command("tmux", "-S", tr.tmuxSocketPath(), "kill-session", "-t", state.tmuxSession).Run()
	}
	tr.mu.Lock()
	delete(tr.states, pane.ID)
	tr.mu.Unlock()
}

// ProxyConfig returns the ttyd proxy behavior for a terminal pane.
func (tr *TerminalRuntime) ProxyConfig(id string) (*PaneProxyConfig, bool) {
	tr.manager.mu.RLock()
	pane, ok := tr.manager.panes[id]
	port := 0
	if ok {
		port = pane.Port
	}
	tr.manager.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if _, ok := tr.getState(id); !ok {
		return nil, false
	}
	return &PaneProxyConfig{
		TargetHost:  fmt.Sprintf("127.0.0.1:%d", port),
		BackendName: "terminal",
		ModifyIndexResponse: func(_ *Server, resp *http.Response) error {
			return tr.modifyTtydIndexResponse(resp)
		},
		NewWebSocketObserver: func(s *Server) WebSocketTrafficObserver {
			return newOSC52Scanner(s)
		},
	}, true
}

// Cleanup releases terminal runtime resources.
func (tr *TerminalRuntime) Cleanup() {
	tmuxSocket := tr.tmuxSocketPath()

	// Kill the entire tmux server on our socket
	exec.Command("tmux", "-S", tmuxSocket, "kill-server").Run()
	os.Remove(tmuxSocket)
	cleanupEmptyInstanceDirs()

	// Clean up temp files
	if tr.tmuxConfigPath != "" {
		os.Remove(tr.tmuxConfigPath)
	}
	if tr.wmBinDir != "" {
		os.RemoveAll(tr.wmBinDir)
	}
	tr.mu.Lock()
	tr.states = make(map[string]*TerminalPaneState)
	tr.mu.Unlock()
}
