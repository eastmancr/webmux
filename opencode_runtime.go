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
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// SECTION: OPENCODE RUNTIME

// OpenCodePaneState stores runtime-only state for OpenCode panes.
type OpenCodePaneState struct {
	cmd    *exec.Cmd
	exitCh chan error
	done   chan struct{}
	output *processOutputBuffer
}

type processOutputBuffer struct {
	mu    sync.Mutex
	lines []string
	limit int
}

func newProcessOutputBuffer(limit int) *processOutputBuffer {
	return &processOutputBuffer{limit: limit}
}

func (b *processOutputBuffer) Add(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit <= 0 {
		return
	}
	b.lines = append(b.lines, line)
	if len(b.lines) > b.limit {
		copy(b.lines, b.lines[len(b.lines)-b.limit:])
		b.lines = b.lines[:b.limit]
	}
}

func (b *processOutputBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.lines, "\n")
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
	cmd.Env = cleanOpenCodeEnv(os.Environ())
	if or.manager.workDir != "" {
		cmd.Dir = or.manager.workDir
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to capture opencode stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to capture opencode stderr: %w", err)
	}
	output := newProcessOutputBuffer(80)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start opencode: %w", err)
	}
	log.Printf("Starting OpenCode backend %s on 127.0.0.1:%d", pane.BackendID, pane.Port)
	go captureProcessOutput("opencode", pane.BackendID, "stdout", stdout, output)
	go captureProcessOutput("opencode", pane.BackendID, "stderr", stderr, output)

	state := &OpenCodePaneState{
		cmd:    cmd,
		exitCh: make(chan error, 1),
		done:   make(chan struct{}),
		output: output,
	}
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
			go or.probeBackendCompatibilityInBackground(baseURL, pane.BackendID)
			log.Printf("OpenCode backend %s ready on %s", pane.BackendID, addr)
			return nil
		}

		select {
		case err := <-state.exitCh:
			or.removeState(pane.BackendID, state)
			return fmt.Errorf("opencode exited before becoming ready on %s: %v%s", addr, err, formatProcessOutput(output))
		case <-deadline:
			or.Stop(pane)
			return fmt.Errorf("opencode did not become ready on %s within 15s%s", addr, formatProcessOutput(output))
		case <-tick.C:
		}
	}
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

func captureProcessOutput(processName, backendID, stream string, pipe interface{ Read([]byte) (int, error) }, output *processOutputBuffer) {
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	suppressWarningDetails := false
	for scanner.Scan() {
		line := scanner.Text()
		output.Add(stream + ": " + line)
		if suppressWarningDetails {
			if line == "" || line[0] == ' ' || line[0] == '\t' {
				continue
			}
			suppressWarningDetails = false
		}
		log.Printf("%s backend %s %s: %s", processName, backendID, stream, line)
		if processName == "opencode" && strings.HasPrefix(line, "MaxListenersExceededWarning:") {
			suppressWarningDetails = true
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("%s backend %s %s read error: %v", processName, backendID, stream, err)
	}
}

func formatProcessOutput(output *processOutputBuffer) string {
	text := strings.TrimSpace(output.String())
	if text == "" {
		return ""
	}
	return "\nRecent opencode output:\n" + text
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
	if !ok || state.cmd == nil {
		return
	}

	err := <-state.exitCh
	currentState, stateOK := or.getState(backendID)
	if !stateOK || currentState.cmd != state.cmd {
		return
	}
	log.Printf("OpenCode backend %s exited with: %v%s", backendID, err, formatProcessOutput(state.output))

	or.manager.mu.Lock()
	defer or.manager.mu.Unlock()

	if !or.manager.hasPaneForBackend(backendID) {
		return
	}

	or.mu.Lock()
	delete(or.states, backendID)
	or.mu.Unlock()
	or.manager.deletePanesForBackend(backendID)
	if len(or.manager.panes) == 0 {
		or.manager.resetCounters()
	}
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
	if state.cmd != nil && state.cmd.Process != nil {
		killProcessGroup(state.cmd.Process)
	}
	or.removeState(backendID, state)
}

func killProcessGroup(process *os.Process) {
	if process == nil {
		return
	}
	if process.Pid > 0 {
		if err := syscall.Kill(-process.Pid, syscall.SIGKILL); err == nil {
			return
		}
	}
	if err := process.Kill(); err == nil {
		return
	}
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
		return true
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
	or.mu.Lock()
	defer or.mu.Unlock()
	for id, state := range or.states {
		if state.cmd != nil && state.cmd.Process != nil {
			killProcessGroup(state.cmd.Process)
		}
		delete(or.states, id)
	}
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
