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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
)

// SECTION: OPENCODE RUNTIME

// OpenCodePaneState stores runtime-only state for OpenCode panes.
type OpenCodePaneState struct {
	cmd         *exec.Cmd
	pid         int
	port        int
	exitCh      chan error
	done        chan struct{}
	logPath     string
	monitorOnce sync.Once
	identity    PersistedBackend
}

// OpenCodeRuntime owns managed opencode server processes.
type OpenCodeRuntime struct {
	manager       *PaneManager
	states        map[string]*OpenCodePaneState
	mu            sync.RWMutex
	warningReason string
	warningDetail string
}

func (or *OpenCodeRuntime) Start(pane *Pane) error {
	if pane.BackendID == "" {
		pane.BackendID = pane.ID
	}
	if or.IsRunning(pane.BackendID) {
		return nil
	}

	args := []string{
		"serve",
		"--hostname", "127.0.0.1",
		"--port", strconv.Itoa(pane.Port),
	}
	cmd := exec.Command("opencode", args...)
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return fmt.Errorf("failed to generate backend identity: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)
	cmd.Env = append(cleanOpenCodeEnv(os.Environ()), "WEBMUX_BACKEND_TOKEN="+token)
	if or.manager.workDir != "" {
		cmd.Dir = or.manager.workDir
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	logPath := filepath.Join(webmuxInstanceDir(or.manager.instanceID), "opencode.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
		return fmt.Errorf("failed to create opencode log directory: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("failed to open opencode log: %w", err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("failed to start opencode: %w", err)
	}
	logFile.Close()
	log.Printf("Starting OpenCode backend %s on 127.0.0.1:%d", pane.BackendID, pane.Port)

	state := &OpenCodePaneState{
		cmd:     cmd,
		pid:     cmd.Process.Pid,
		port:    pane.Port,
		exitCh:  make(chan error, 1),
		done:    make(chan struct{}),
		logPath: logPath,
	}
	var identity PersistedBackend
	identityDeadline := time.Now().Add(time.Second)
	for {
		identity, err = readOpenCodeProcessIdentity(cmd.Process.Pid, pane.Port, token)
		if err == nil || time.Now().After(identityDeadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return fmt.Errorf("failed to record opencode process identity: %w", err)
	}
	identity.ID = pane.BackendID
	identity.Type = "opencode"
	state.identity = identity
	go func() {
		state.exitCh <- cmd.Wait()
		close(state.done)
	}()

	or.mu.Lock()
	or.states[pane.BackendID] = state
	or.mu.Unlock()
	or.setWarningReason("")

	addr := fmt.Sprintf("127.0.0.1:%d", pane.Port)
	baseURL := "http://" + addr
	deadline := time.After(15 * time.Second)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		conn, err := net.DialTimeout("tcp", addr, 20*time.Millisecond)
		if err == nil {
			conn.Close()
			if processOwnsListeningPort(state.pid, pane.Port) {
				go or.probeBackendCompatibilityInBackground(baseURL, pane.BackendID)
				log.Printf("OpenCode backend %s ready on %s", pane.BackendID, addr)
				return nil
			}
		}

		select {
		case err := <-state.exitCh:
			or.removeState(pane.BackendID, state)
			return fmt.Errorf("opencode exited before becoming ready on %s: %v (see %s)", addr, err, logPath)
		case <-deadline:
			or.Stop(pane)
			return fmt.Errorf("opencode did not become ready on %s within 15s (see %s)", addr, logPath)
		case <-tick.C:
		}
	}
}

func (or *OpenCodeRuntime) Adopt(backend PersistedBackend) error {
	if backend.PID <= 0 || backend.Port <= 0 {
		return fmt.Errorf("invalid persisted process")
	}
	if !openCodeProcessMatchesWithRetry(backend) {
		return fmt.Errorf("process %d is not a live opencode server", backend.PID)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", backend.Port)
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return fmt.Errorf("backend is not listening on %s: %w", addr, err)
	}
	conn.Close()
	if !processOwnsListeningPort(backend.PID, backend.Port) {
		return fmt.Errorf("process %d does not own the listener on %s", backend.PID, addr)
	}

	state := &OpenCodePaneState{
		pid:      backend.PID,
		port:     backend.Port,
		exitCh:   make(chan error, 1),
		done:     make(chan struct{}),
		logPath:  filepath.Join(webmuxInstanceDir(or.manager.instanceID), "opencode.log"),
		identity: backend,
	}
	or.mu.Lock()
	or.states[backend.ID] = state
	or.mu.Unlock()
	go func() {
		failedChecks := 0
		for {
			if openCodeProcessMatches(backend) {
				failedChecks = 0
				time.Sleep(time.Second)
				continue
			}
			if syscall.Kill(backend.PID, 0) != nil {
				break
			}
			failedChecks++
			if failedChecks >= 3 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		state.exitCh <- fmt.Errorf("adopted process %d exited", backend.PID)
		close(state.done)
	}()
	log.Printf("Adopted OpenCode backend %s on %s (PID %d)", backend.ID, addr, backend.PID)
	go or.Monitor(&Pane{BackendID: backend.ID})
	return nil
}

func readOpenCodeProcessIdentity(pid, port int, token string) (PersistedBackend, error) {
	if pid <= 0 || syscall.Kill(pid, 0) != nil {
		return PersistedBackend{}, fmt.Errorf("process is not alive")
	}
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return PersistedBackend{}, err
	}
	closeParen := strings.LastIndexByte(string(stat), ')')
	if closeParen < 0 {
		return PersistedBackend{}, fmt.Errorf("invalid process stat")
	}
	fields := strings.Fields(string(stat)[closeParen+1:])
	if len(fields) <= 19 {
		return PersistedBackend{}, fmt.Errorf("incomplete process stat")
	}
	processGroup, err := strconv.Atoi(fields[2])
	if err != nil {
		return PersistedBackend{}, err
	}
	startTime, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return PersistedBackend{}, err
	}
	bootIDBytes, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return PersistedBackend{}, err
	}
	cmdline, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return PersistedBackend{}, err
	}
	args := strings.Split(strings.TrimSuffix(string(cmdline), "\x00"), "\x00")
	if !containsOpenCodeServeArgs(args, port) {
		return PersistedBackend{}, fmt.Errorf("process command does not match opencode serve port %d: %q", port, args)
	}
	environ, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil {
		return PersistedBackend{}, err
	}
	if !strings.Contains("\x00"+string(environ), "\x00WEBMUX_BACKEND_TOKEN="+token+"\x00") {
		return PersistedBackend{}, fmt.Errorf("process token does not match")
	}
	if processGroup != pid {
		return PersistedBackend{}, fmt.Errorf("process is not its process-group leader")
	}
	return PersistedBackend{
		Port: port, PID: pid, ProcessGroup: processGroup, StartTime: startTime,
		BootID: strings.TrimSpace(string(bootIDBytes)), Token: token,
	}, nil
}

func containsOpenCodeServeArgs(args []string, port int) bool {
	hasServe := false
	hasPort := false
	for i, arg := range args {
		if arg == "serve" || arg == "opencode" && i+1 < len(args) && args[i+1] == "serve" {
			hasServe = true
		}
		if arg == "--port" && i+1 < len(args) && args[i+1] == strconv.Itoa(port) {
			hasPort = true
		}
	}
	return hasServe && hasPort
}

func openCodeProcessMatches(expected PersistedBackend) bool {
	actual, err := readOpenCodeProcessIdentity(expected.PID, expected.Port, expected.Token)
	return err == nil && actual.ProcessGroup == expected.ProcessGroup &&
		actual.StartTime == expected.StartTime && actual.BootID == expected.BootID
}

func openCodeProcessMatchesWithRetry(expected PersistedBackend) bool {
	for attempt := 0; attempt < 3; attempt++ {
		if openCodeProcessMatches(expected) {
			return true
		}
		if syscall.Kill(expected.PID, 0) != nil {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func processOwnsListeningPort(pid, port int) bool {
	inodes := make(map[string]bool)
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 10 || fields[3] != "0A" {
				continue
			}
			_, portHex, ok := strings.Cut(fields[1], ":")
			if !ok {
				continue
			}
			value, err := strconv.ParseUint(portHex, 16, 16)
			if err == nil && int(value) == port {
				inodes[fields[9]] = true
			}
		}
	}
	if len(inodes) == 0 {
		return false
	}
	fds, err := os.ReadDir(filepath.Join("/proc", strconv.Itoa(pid), "fd"))
	if err != nil {
		return false
	}
	for _, fd := range fds {
		target, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "fd", fd.Name()))
		if err != nil || !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
			continue
		}
		if inodes[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] {
			return true
		}
	}
	return false
}

func (or *OpenCodeRuntime) RunningBackend(backendID string) (int, bool) {
	state, ok := or.getState(backendID)
	if !ok || !or.IsRunning(backendID) {
		return 0, false
	}
	return state.port, true
}

func (or *OpenCodeRuntime) PersistedBackend(backendID string) (PersistedBackend, bool) {
	state, ok := or.getState(backendID)
	if !ok || !or.IsRunning(backendID) {
		return PersistedBackend{}, false
	}
	backend := state.identity
	backend.ID = backendID
	backend.Type = "opencode"
	return backend, true
}

func (or *OpenCodeRuntime) probeBackendCompatibilityInBackground(baseURL, backendID string) {
	deadline := time.After(15 * time.Second)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var lastErr error
	for {
		warning, err := or.probeBackendCompatibility(baseURL)
		if err == nil {
			if warning != "" {
				if or.updateWarningReason(warning) {
					log.Printf("OpenCode compatibility warning for backend %s: %s", backendID, warning)
				}
			}
			return
		}
		lastErr = err

		select {
		case <-deadline:
			warning := "OpenCode index compatibility check failed; pane may not render correctly."
			if or.updateWarningReason(warning) {
				log.Printf("OpenCode compatibility warning for backend %s: %s detail=%q", backendID, warning, lastErr.Error())
			}
			return
		case <-tick.C:
		}
	}
}

func (or *OpenCodeRuntime) probeBackendCompatibility(baseURL string) (string, error) {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(baseURL + "/")
	if err != nil {
		return "", fmt.Errorf("failed to fetch OpenCode index: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("OpenCode index returned HTTP %d", resp.StatusCode)
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType != "" && !strings.Contains(strings.ToLower(contentType), "text/html") {
		return "", fmt.Errorf("OpenCode index returned %s instead of HTML", contentType)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return "", fmt.Errorf("failed to read OpenCode index: %w", err)
	}
	result := analyzeOpenCodeIndexCompatibility(string(body))
	if result.Error != "" {
		return "", errors.New(result.Error)
	}
	return result.Warning, nil
}

func (or *OpenCodeRuntime) WarningReason() string {
	or.mu.RLock()
	defer or.mu.RUnlock()
	return or.warningReason
}

func (or *OpenCodeRuntime) setWarningReason(reason string) {
	or.mu.Lock()
	defer or.mu.Unlock()
	or.warningReason = reason
	or.warningDetail = ""
}

func (or *OpenCodeRuntime) updateWarningReason(reason string) bool {
	return or.updateWarning(reason, "")
}

func (or *OpenCodeRuntime) updateWarning(reason, detail string) bool {
	or.mu.Lock()
	defer or.mu.Unlock()
	if or.warningReason == reason && or.warningDetail == detail {
		return false
	}
	or.warningReason = reason
	or.warningDetail = detail
	return true
}

func cleanOpenCodeEnv(env []string) []string {
	blocked := map[string]bool{
		"OPENCODE":              true,
		"OPENCODE_CONFIG":       true,
		"OPENCODE_CONFIG_DIR":   true,
		"OPENCODE_PID":          true,
		"OPENCODE_PROCESS_ROLE": true,
		"OPENCODE_RUN_ID":       true,
	}

	clean := env[:0]
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if ok && blocked[key] {
			continue
		}
		clean = append(clean, entry)
	}
	return clean
}

func (or *OpenCodeRuntime) Monitor(pane *Pane) {
	backendID := pane.BackendID
	state, ok := or.getState(backendID)
	if !ok {
		return
	}
	state.monitorOnce.Do(func() {
		err := <-state.exitCh
		currentState, stateOK := or.getState(backendID)
		if !stateOK || currentState != state {
			return
		}
		log.Printf("OpenCode backend %s exited with: %v (see %s)", backendID, err, state.logPath)

		or.mu.Lock()
		delete(or.states, backendID)
		or.mu.Unlock()
		or.manager.mu.Lock()
		closedPaneIDs := []string{}
		if or.manager.hasPaneForBackend(backendID) {
			closedPaneIDs = or.manager.deletePanesForBackend(backendID)
			if len(or.manager.panes) == 0 {
				or.manager.resetCounters()
			}
		}
		or.manager.mu.Unlock()
		for _, paneID := range closedPaneIDs {
			or.manager.notifyPaneClosed(paneID)
		}
		or.manager.stateChanged()
	})
}

// Restart stops the backend process and starts a fresh one on the same port
// without closing the panes bound to it. The state is detached from the map
// before the process is signaled so Monitor's exit handling (which deletes
// all panes for the backend) sees a stale state and returns early.
func (or *OpenCodeRuntime) Restart(pane *Pane) error {
	backendID := pane.BackendID
	state, ok := or.getState(backendID)
	if !ok {
		return fmt.Errorf("backend %s is not running", backendID)
	}
	or.removeState(backendID, state)
	if !or.stopState(backendID, state, 3*time.Second) {
		// The old process is still alive; reattach its state so it stays
		// managed and Monitor semantics are restored.
		or.mu.Lock()
		if _, exists := or.states[backendID]; !exists {
			or.states[backendID] = state
		}
		or.mu.Unlock()
		return fmt.Errorf("failed to stop backend %s for restart", backendID)
	}
	return or.Start(pane)
}

func (or *OpenCodeRuntime) Stop(pane *Pane) {
	backendID := pane.BackendID
	if backendID == "" {
		backendID = pane.ID
	}
	state, ok := or.getState(backendID)
	if !ok {
		return
	}
	or.stopState(backendID, state, 3*time.Second)
}

func (or *OpenCodeRuntime) stopState(backendID string, state *OpenCodePaneState, timeout time.Duration) bool {
	if !openCodeProcessMatches(state.identity) {
		if syscall.Kill(state.pid, 0) != nil {
			or.removeState(backendID, state)
			return true
		}
		log.Printf("Refusing to signal OpenCode backend %s: process identity changed", backendID)
		return false
	}
	if state.identity.ProcessGroup > 0 {
		_ = syscall.Kill(-state.identity.ProcessGroup, syscall.SIGTERM)
	}
	select {
	case <-state.done:
	case <-time.After(timeout):
		if openCodeProcessMatches(state.identity) {
			log.Printf("OpenCode backend %s did not exit within %v; killing PID %d", backendID, timeout, state.pid)
			_ = syscall.Kill(-state.identity.ProcessGroup, syscall.SIGKILL)
		}
	}
	deadline := time.Now().Add(time.Second)
	for openCodeProcessMatches(state.identity) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if openCodeProcessMatches(state.identity) {
		log.Printf("OpenCode backend %s is still alive after forced shutdown", backendID)
		return false
	}
	or.removeState(backendID, state)
	return true
}

func (or *OpenCodeRuntime) IsRunning(backendID string) bool {
	state, ok := or.getState(backendID)
	if !ok {
		return false
	}
	select {
	case <-state.done:
		return false
	default:
		return openCodeProcessMatchesWithRetry(state.identity)
	}
}

func (or *OpenCodeRuntime) ProxyConfig(id string) (*PaneProxyConfig, bool) {
	or.manager.mu.RLock()
	pane, ok := or.manager.panes[id]
	port := 0
	backendID := ""
	if ok {
		port = pane.Port
		backendID = pane.BackendID
	}
	or.manager.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if _, ok := or.getState(backendID); !ok {
		return nil, false
	}

	targetHost := fmt.Sprintf("127.0.0.1:%d", port)
	return &PaneProxyConfig{
		TargetHost:  targetHost,
		BackendName: "opencode",
		ModifyRequest: func(req *http.Request) {
			rewriteOpenCodeRequestOrigin(targetHost, req)
		},
		ModifyResponse: func(s *Server, resp *http.Response) error {
			if strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
				return or.modifyOpenCodeIndexResponse(s, id, backendID, s.getPaneStorageSnapshot(backendID), s.diagnosticsSettings(), resp)
			}
			return or.modifyOpenCodeAssetResponse(id, resp)
		},
	}, true
}

func (or *OpenCodeRuntime) Cleanup() {
	or.Shutdown(3 * time.Second)
}

func (or *OpenCodeRuntime) Shutdown(timeout time.Duration) bool {
	or.mu.RLock()
	states := make(map[string]*OpenCodePaneState, len(or.states))
	for id, state := range or.states {
		states[id] = state
	}
	or.mu.RUnlock()
	allStopped := true
	for id, state := range states {
		if !or.stopState(id, state, timeout) {
			allStopped = false
		}
	}
	return allStopped
}

func (or *OpenCodeRuntime) getState(paneID string) (*OpenCodePaneState, bool) {
	or.mu.RLock()
	defer or.mu.RUnlock()
	state, ok := or.states[paneID]
	return state, ok
}

func (or *OpenCodeRuntime) removeState(backendID string, state *OpenCodePaneState) {
	or.mu.Lock()
	defer or.mu.Unlock()
	if current, ok := or.states[backendID]; ok && current == state {
		delete(or.states, backendID)
	}
}
