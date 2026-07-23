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
	"context"
	"fmt"
	"log"
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
}

// TerminalRuntime owns tmux lifecycle for terminal panes.
type TerminalRuntime struct {
	manager        *PaneManager
	states         map[string]*TerminalPaneState
	mu             sync.RWMutex
	tmuxConfigPath string
	wmBinDir       string // Directory containing wm binary (added to PATH)
	sixelSupported bool
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
	tr.sixelSupported = detectTmuxSixelSupport()
}

func detectTmuxSixelSupport() bool {
	socketName := fmt.Sprintf("webmux-capability-%d", os.Getpid())
	defer exec.Command("tmux", "-L", socketName, "kill-server").Run()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "-L", socketName, "-f", "/dev/null",
		"start-server", ";", "display-message", "-p", "#{sixel_support}", ";", "kill-server").Output()
	if err != nil || strings.TrimSpace(string(out)) != "1" {
		log.Printf("tmux SIXEL support unavailable; terminal images are disabled")
		return false
	}
	return true
}

func (tr *TerminalRuntime) extractTmuxConfig() {
	tmuxConf, err := staticFiles.ReadFile("static/tmux.conf")
	if err != nil {
		log.Printf("Warning: could not read tmux.conf: %v", err)
		return
	}

	path := filepath.Join(webmuxInstanceDir(tr.manager.instanceID), "runtime", "tmux.conf")
	if err := writeRuntimeFile(path, tmuxConf, 0600); err != nil {
		log.Printf("Warning: could not write tmux config: %v", err)
		return
	}
	tr.tmuxConfigPath = path
	log.Printf("Using custom tmux config: %s", tr.tmuxConfigPath)
}

func (tr *TerminalRuntime) extractWMBinary() {
	wmBin, err := staticFiles.ReadFile("static/wm")
	if err != nil {
		log.Printf("Warning: could not read embedded wm binary: %v", err)
		return
	}

	binDir := filepath.Join(webmuxInstanceDir(tr.manager.instanceID), "runtime", "bin")
	wmPath := filepath.Join(binDir, "wm")
	if err := writeRuntimeFile(wmPath, wmBin, 0755); err != nil {
		log.Printf("Warning: could not write wm binary: %v", err)
		return
	}

	tr.wmBinDir = binDir
	log.Printf("Extracted wm binary to: %s", wmPath)
}

func writeRuntimeFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".runtime-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (tr *TerminalRuntime) installShellScripts() {
	if tr.wmBinDir == "" {
		return
	}

	wmPath := filepath.Join(tr.wmBinDir, "wm")
	initPath := filepath.Join(tr.wmBinDir, "init.sh")
	initContent := shellinit.InitScript(wmPath, tr.wmBinDir)
	if err := writeRuntimeFile(initPath, []byte(initContent), 0644); err != nil {
		log.Printf("Warning: could not write init script: %v", err)
	}

	for _, script := range shellinit.ClipboardWrapperScripts(wmPath) {
		scriptPath := filepath.Join(tr.wmBinDir, script.Name)
		if err := writeRuntimeFile(scriptPath, []byte(script.Content), 0755); err != nil {
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
	if tr.sixelSupported {
		args = append(args, "-e", "WEBMUX_IMAGE_PROTOCOL=sixel")
	}

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
			if err := writeRuntimeFile(rcPath, []byte(rcContent), 0644); err != nil {
				return fmt.Errorf("failed to write bash runtime config: %w", err)
			}
			tmuxArgs = append(tmuxArgs, tr.manager.shell, "--rcfile", rcPath)
		case "zsh":
			// zsh: use ZDOTDIR with custom rc files that source user's config then our init
			zdotdir := filepath.Join(tr.wmBinDir, "zsh")
			os.MkdirAll(zdotdir, 0755)
			// Create .zshenv that sources user's .zshenv (but keeps our ZDOTDIR)
			zshenvContent := `[ -f "$HOME/.zshenv" ] && . "$HOME/.zshenv"
`
			if err := writeRuntimeFile(filepath.Join(zdotdir, ".zshenv"), []byte(zshenvContent), 0644); err != nil {
				return fmt.Errorf("failed to write zsh environment config: %w", err)
			}
			// Create .zprofile that sources user's .zprofile
			zprofileContent := `[ -f "$HOME/.zprofile" ] && . "$HOME/.zprofile"
`
			if err := writeRuntimeFile(filepath.Join(zdotdir, ".zprofile"), []byte(zprofileContent), 0644); err != nil {
				return fmt.Errorf("failed to write zsh profile config: %w", err)
			}
			// Create .zshrc that sources user's .zshrc then our init
			zshrcContent := fmt.Sprintf(`[ -f "$HOME/.zshrc" ] && . "$HOME/.zshrc"
. %s
`, initPath)
			if err := writeRuntimeFile(filepath.Join(zdotdir, ".zshrc"), []byte(zshrcContent), 0644); err != nil {
				return fmt.Errorf("failed to write zsh runtime config: %w", err)
			}
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
	if err := configureTmuxAttention(tmuxSocket); err != nil {
		log.Printf("Warning: could not enable terminal bell forwarding: %v", err)
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

	return nil
}

func (tr *TerminalRuntime) getState(paneID string) (*TerminalPaneState, bool) {
	tr.mu.RLock()
	defer tr.mu.RUnlock()
	state, ok := tr.states[paneID]
	return state, ok
}

func (tr *TerminalRuntime) Adopt(pane *Pane) bool {
	tmuxSession := strings.Replace(pane.ID, "pane-", "mux-", 1)
	if !strings.HasPrefix(tmuxSession, "mux-") {
		return false
	}
	for attempt := 0; attempt < 3; attempt++ {
		if exec.Command("tmux", "-S", tr.tmuxSocketPath(), "has-session", "-t", tmuxSession).Run() == nil {
			if err := configureTmuxAttention(tr.tmuxSocketPath()); err != nil {
				log.Printf("Warning: could not enable terminal bell forwarding: %v", err)
			}
			tr.mu.Lock()
			tr.states[pane.ID] = &TerminalPaneState{tmuxSession: tmuxSession}
			tr.mu.Unlock()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func configureTmuxAttention(tmuxSocket string) error {
	if out, err := exec.Command("tmux", "-S", tmuxSocket, "set-option", "-g", "bell-action", "current").CombinedOutput(); err != nil {
		return fmt.Errorf("tmux set-option: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// monitorPane watches the tmux session to detect when the shell exits
// and updates the current foreground process
func (tr *TerminalRuntime) Monitor(pane *Pane) {
	sm := tr.manager
	tmuxSocket := tr.tmuxSocketPath()
	startTime := time.Now()
	checkCount := 0
	missingChecks := 0

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
			missingChecks++
			if missingChecks < 3 {
				time.Sleep(200 * time.Millisecond)
				continue
			}
			log.Printf("Pane %s: tmux session %s exited after %d checks (%v), cleaning up", pane.ID, tmuxSession, checkCount, time.Since(startTime))
			sm.mu.Lock()
			deleted := false
			if _, ok := sm.panes[pane.ID]; ok {
				tr.mu.Lock()
				delete(tr.states, pane.ID)
				tr.mu.Unlock()
				sm.deletePane(pane.ID)
				deleted = true
			}
			if len(sm.panes) == 0 {
				sm.resetCounters()
			}
			sm.mu.Unlock()
			if deleted {
				sm.notifyPaneClosed(pane.ID)
			}
			return
		}
		missingChecks = 0

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
func (tr *TerminalRuntime) Stop(pane *Pane) error {
	state, ok := tr.getState(pane.ID)
	if !ok {
		return fmt.Errorf("terminal state not found: %s", pane.ID)
	}
	if state.tmuxSession != "" {
		_ = exec.Command("tmux", "-S", tr.tmuxSocketPath(), "kill-session", "-t", state.tmuxSession).Run()
		for attempt := 0; attempt < 5; attempt++ {
			if exec.Command("tmux", "-S", tr.tmuxSocketPath(), "has-session", "-t", state.tmuxSession).Run() != nil {
				tr.mu.Lock()
				delete(tr.states, pane.ID)
				tr.mu.Unlock()
				return nil
			}
			time.Sleep(50 * time.Millisecond)
		}
		return fmt.Errorf("terminal session %s did not close", state.tmuxSession)
	}
	tr.mu.Lock()
	delete(tr.states, pane.ID)
	tr.mu.Unlock()
	return nil
}

// Shutdown asks tmux to close its sessions, then kills a stuck server.
func (tr *TerminalRuntime) Shutdown(timeout time.Duration) bool {
	tmuxSocket := tr.tmuxSocketPath()
	pid := 0
	if out, err := exec.Command("tmux", "-S", tmuxSocket, "display-message", "-p", "#{pid}").Output(); err == nil {
		pid, _ = strconv.Atoi(strings.TrimSpace(string(out)))
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	err := exec.CommandContext(ctx, "tmux", "-S", tmuxSocket, "kill-server").Run()
	cancel()
	if ctx.Err() != nil && pid > 0 {
		log.Printf("Tmux server did not exit within %v; killing PID %d", timeout, pid)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	} else if err != nil && !os.IsNotExist(err) {
		if _, statErr := os.Stat(tmuxSocket); statErr == nil {
			log.Printf("Tmux shutdown returned: %v", err)
			return false
		}
	}
	if pid > 0 {
		deadline := time.Now().Add(time.Second)
		for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if syscall.Kill(pid, 0) == nil {
			log.Printf("Tmux server PID %d is still alive after forced shutdown", pid)
			return false
		}
	}
	os.Remove(tmuxSocket)
	tr.mu.Lock()
	tr.states = make(map[string]*TerminalPaneState)
	tr.mu.Unlock()
	return true
}

func (tr *TerminalRuntime) Cleanup() {
	tr.Shutdown(3 * time.Second)
}
