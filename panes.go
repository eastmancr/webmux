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
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SECTION: PANES

// Pane represents a managed endpoint shown in the web UI.
type Pane struct {
	ID             string    `json:"id"`
	Type           string    `json:"type"`
	BackendID      string    `json:"backendId"`
	BackendScope   string    `json:"backendScope"`
	Name           string    `json:"name"`
	Port           int       `json:"port"`
	CreatedAt      time.Time `json:"createdAt"`
	CurrentProcess string    `json:"currentProcess,omitempty"`
}

// PaneTypeInfo describes a pane type that can be shown in the UI.
type PaneTypeInfo struct {
	Type              string `json:"type"`
	Label             string `json:"label"`
	BackendScope      string `json:"backendScope"`
	SupportsKeybar    bool   `json:"supportsKeybar"`
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailableReason,omitempty"`
	WarningReason     string `json:"warningReason,omitempty"`
	Version           string `json:"version,omitempty"`
}

const (
	PaneBackendDedicated = "dedicated"
	PaneBackendShared    = "shared"
)

// SECTION: SESSIONS

// PaneManager handles generic pane lifecycle and dispatches type-specific work.
type PaneManager struct {
	panes         map[string]*Pane
	mu            sync.RWMutex
	createMu      sync.Mutex
	nextPort      int32
	nextPaneID    int32
	startPort     int32 // Initial port to reset to when all panes close
	shell         string
	workDir       string // Starting directory for new panes
	getSettings   func() *Settings
	serverPort    string // HTTP server port for WEBMUX_PORT env var
	instanceID    string // Runtime namespace derived from the HTTP server port
	onPaneClosed  func(string)
	onPaneChanged func(*Pane)
	terminal      *TerminalRuntime
	opencode      *OpenCodeRuntime
}

// NewPaneManager creates a new pane manager
func NewPaneManager(startPort int, shell, workDir, serverPort string) *PaneManager {
	sm := &PaneManager{
		panes:      make(map[string]*Pane),
		nextPort:   int32(startPort),
		nextPaneID: int32(startPort),
		startPort:  int32(startPort),
		shell:      shell,
		workDir:    workDir,
		serverPort: serverPort,
		instanceID: instanceIDForPort(serverPort),
	}
	sm.terminal = &TerminalRuntime{
		manager: sm,
		states:  make(map[string]*TerminalPaneState),
	}
	sm.opencode = &OpenCodeRuntime{
		manager: sm,
		states:  make(map[string]*OpenCodePaneState),
	}
	sm.terminal.SetupResources()

	return sm
}

// tmuxSocketPath returns the path to our dedicated tmux socket
func (sm *PaneManager) tmuxSocketPath() string {
	return sm.terminal.tmuxSocketPath()
}

// CreatePane creates a pane and starts its type-specific runtime.
func (sm *PaneManager) CreatePane(paneType, name string) (*Pane, error) {
	// Serialize creation so shared backends cannot be observed as absent by
	// multiple concurrent requests and started more than once.
	sm.createMu.Lock()
	defer sm.createMu.Unlock()

	if paneType == "" {
		paneType = "terminal"
	}
	if !sm.isSupportedPaneType(paneType) {
		return nil, fmt.Errorf("unsupported pane type: %s", paneType)
	}
	if available, reason := sm.paneTypeAvailability(paneType); !available {
		return nil, fmt.Errorf("pane type %s unavailable: %s", paneType, reason)
	}

	scope := sm.backendScope(paneType)
	port := 0
	backendID := ""
	startedBackend := false

	if scope == PaneBackendShared {
		if existing := sm.findSharedBackendPane(paneType); existing != nil {
			port = existing.Port
			backendID = existing.BackendID
		} else {
			var err error
			port, err = sm.allocatePanePort()
			if err != nil {
				return nil, err
			}
			backendID = paneType
			startedBackend = true
		}
	} else {
		var err error
		port, err = sm.allocatePanePort()
		if err != nil {
			return nil, err
		}
		startedBackend = true
	}

	id := sm.allocatePaneID(port)
	if backendID == "" {
		backendID = id
	}

	if name == "" {
		// Find the highest numeric name among active panes and increment from there
		sm.mu.RLock()
		maxNum := 0
		for _, s := range sm.panes {
			if num, err := strconv.Atoi(s.Name); err == nil && num > maxNum {
				maxNum = num
			}
		}
		sm.mu.RUnlock()
		name = strconv.Itoa(maxNum + 1)
	}

	pane := &Pane{
		ID:           id,
		Type:         paneType,
		BackendID:    backendID,
		BackendScope: scope,
		Name:         name,
		Port:         port,
		CreatedAt:    time.Now(),
	}

	if startedBackend {
		if err := sm.startPaneRuntime(pane); err != nil {
			return nil, err
		}
	}

	if !startedBackend && scope == PaneBackendShared && !sm.sharedBackendRunning(pane) {
		return nil, fmt.Errorf("shared backend not running for pane type: %s", paneType)
	}

	if !startedBackend {
		log.Printf("Created pane %s using shared %s backend %s on port %d", id, paneType, backendID, port)
	} else {
		log.Printf("Created pane %s on port %d", id, port)
	}

	sm.mu.Lock()
	sm.panes[id] = pane
	sm.mu.Unlock()

	if startedBackend {
		sm.monitorPaneRuntime(pane)
	}

	return pane, nil
}

func (sm *PaneManager) isSupportedPaneType(paneType string) bool {
	switch paneType {
	case "terminal", "opencode":
		return true
	default:
		return false
	}
}

func (sm *PaneManager) paneTypeAvailability(paneType string) (bool, string) {
	switch paneType {
	case "terminal":
		return true, ""
	case "opencode":
		if _, err := exec.LookPath("opencode"); err != nil {
			return false, "opencode not found in PATH"
		}
		return true, ""
	default:
		return false, "unsupported pane type"
	}
}

func (sm *PaneManager) PaneTypes() []PaneTypeInfo {
	opencodeAvailable, opencodeReason := sm.paneTypeAvailability("opencode")
	opencodeVersion := ""
	if opencodeAvailable {
		opencodeVersion = paneTypeCommandVersion("opencode")
	}
	opencodeWarning := ""
	if sm.opencode != nil {
		opencodeWarning = sm.opencode.WarningReason()
	}
	return []PaneTypeInfo{
		{
			Type:           "terminal",
			Label:          "Terminal",
			BackendScope:   PaneBackendDedicated,
			SupportsKeybar: true,
			Available:      true,
		},
		{
			Type:              "opencode",
			Label:             "OpenCode",
			BackendScope:      PaneBackendShared,
			SupportsKeybar:    false,
			Available:         opencodeAvailable,
			UnavailableReason: opencodeReason,
			WarningReason:     opencodeWarning,
			Version:           opencodeVersion,
		},
	}
}

func paneTypeCommandVersion(command string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	output, err := exec.CommandContext(ctx, command, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func (sm *PaneManager) backendScope(paneType string) string {
	switch paneType {
	case "opencode":
		return PaneBackendShared
	default:
		return PaneBackendDedicated
	}
}

func (sm *PaneManager) findSharedBackendPane(paneType string) *Pane {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	for _, pane := range sm.panes {
		if pane.Type == paneType && pane.BackendScope == PaneBackendShared {
			return pane
		}
	}
	return nil
}

func (sm *PaneManager) sharedBackendRunning(pane *Pane) bool {
	switch pane.Type {
	case "opencode":
		return sm.opencode.IsRunning(pane.BackendID)
	default:
		return false
	}
}

func (sm *PaneManager) allocatePaneID(preferredPort int) string {
	if preferredPort > 0 {
		id := fmt.Sprintf("pane-%d", preferredPort)
		if !sm.paneExists(id) {
			return id
		}
	}

	for {
		id := fmt.Sprintf("pane-%d", atomic.AddInt32(&sm.nextPaneID, 1))
		if !sm.paneExists(id) {
			return id
		}
	}
}

func (sm *PaneManager) paneExists(id string) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	_, ok := sm.panes[id]
	return ok
}

func (sm *PaneManager) deletePanesForBackend(backendID string) {
	for id, pane := range sm.panes {
		if pane.BackendID == backendID {
			sm.deletePane(id)
		}
	}
}

func (sm *PaneManager) hasPaneForBackend(backendID string) bool {
	for _, pane := range sm.panes {
		if pane.BackendID == backendID {
			return true
		}
	}
	return false
}

func (sm *PaneManager) BackendExists(backendID string) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.hasPaneForBackend(backendID)
}

func (sm *PaneManager) startPaneRuntime(pane *Pane) error {
	switch pane.Type {
	case "terminal":
		return sm.terminal.Start(pane)
	case "opencode":
		return sm.opencode.Start(pane)
	default:
		return fmt.Errorf("unsupported pane type: %s", pane.Type)
	}
}

func (sm *PaneManager) monitorPaneRuntime(pane *Pane) {
	switch pane.Type {
	case "terminal":
		go sm.terminal.Monitor(pane)
	case "opencode":
		go sm.opencode.Monitor(pane)
	}
}

func (sm *PaneManager) allocatePanePort() (int, error) {
	for range 100 {
		port := int(atomic.AddInt32(&sm.nextPort, 1))
		if isTCPPortAvailable(port) {
			return port, nil
		}
		log.Printf("Pane backend port %d is already in use, trying next port", port)
	}
	return 0, fmt.Errorf("no available pane backend ports")
}

func isTCPPortAvailable(port int) bool {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	listener.Close()
	return true
}

// GetPane returns a pane by ID
func (sm *PaneManager) GetPane(id string) (*Pane, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	s, ok := sm.panes[id]
	if !ok {
		return nil, false
	}
	pane := *s
	return &pane, true
}

// ListPanes returns all active panes
func (sm *PaneManager) ListPanes() []*Pane {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	panes := make([]*Pane, 0, len(sm.panes))
	for _, s := range sm.panes {
		pane := *s
		panes = append(panes, &pane)
	}
	return panes
}

// ClosePane terminates a pane and its runtime backend.
func (sm *PaneManager) ClosePane(id string) error {
	sm.createMu.Lock()
	defer sm.createMu.Unlock()

	sm.mu.Lock()
	pane, ok := sm.panes[id]
	if !ok {
		sm.mu.Unlock()
		return fmt.Errorf("pane not found: %s", id)
	}

	paneCopy := *pane
	sm.deletePane(id)
	stopOpenCode := paneCopy.Type == "opencode" && !sm.hasPaneForBackend(paneCopy.BackendID)
	sm.mu.Unlock()

	switch paneCopy.Type {
	case "terminal":
		sm.terminal.Stop(&paneCopy)
	case "opencode":
		if stopOpenCode {
			sm.opencode.Stop(&paneCopy)
		}
	}
	log.Printf("Closed pane %s", id)

	// Reset counters when all panes are closed (ports are now free to reuse)
	sm.mu.Lock()
	if len(sm.panes) == 0 {
		sm.resetCounters()
	}
	sm.mu.Unlock()

	return nil
}

// resetCounters resets port counter to initial value
// Called when all panes have been closed to allow port reuse
func (sm *PaneManager) resetCounters() {
	atomic.StoreInt32(&sm.nextPort, sm.startPort)
	atomic.StoreInt32(&sm.nextPaneID, sm.startPort)
	log.Printf("All panes closed, reset pane and port counters to %d", sm.startPort)
}

// deletePane removes a pane from the map and notifies the callback
// Must be called with sm.mu held
func (sm *PaneManager) deletePane(id string) {
	delete(sm.panes, id)
	if sm.onPaneClosed != nil {
		// Call outside of lock to avoid deadlock
		go sm.onPaneClosed(id)
	}
}

// RenamePane changes the display name of a pane
func (sm *PaneManager) RenamePane(id, name string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	pane, ok := sm.panes[id]
	if !ok {
		return fmt.Errorf("pane not found: %s", id)
	}

	pane.Name = name
	return nil
}

// ProxyConfig returns proxy behavior for a pane backend.
func (sm *PaneManager) ProxyConfig(id string) (*PaneProxyConfig, bool) {
	sm.mu.RLock()
	pane, ok := sm.panes[id]
	if !ok {
		sm.mu.RUnlock()
		return nil, false
	}
	paneType := pane.Type
	sm.mu.RUnlock()

	switch paneType {
	case "terminal":
		return nil, false
	case "opencode":
		return sm.opencode.ProxyConfig(id)
	default:
		return nil, false
	}
}

// PaneInputStep represents a single step in a pane input sequence.
type PaneInputStep struct {
	Type  string `json:"type"`  // "key" or "text"
	Value string `json:"value"` // key name (e.g. "C-c") or literal text
}

// PaneInputRequest represents a request to send input to a pane.
type PaneInputRequest struct {
	Keys     []string        `json:"keys,omitempty"`     // Simple form: list of key names
	Sequence []PaneInputStep `json:"sequence,omitempty"` // Extended form: sequence of steps
}

// SendInput sends input to the pane runtime.
func (sm *PaneManager) SendInput(id string, req *PaneInputRequest) error {
	sm.mu.RLock()
	pane, ok := sm.panes[id]
	if !ok {
		sm.mu.RUnlock()
		return fmt.Errorf("pane not found: %s", id)
	}
	paneType := pane.Type
	sm.mu.RUnlock()

	switch paneType {
	case "terminal":
		return sm.terminal.SendInput(id, req)
	case "opencode":
		return fmt.Errorf("input unsupported for pane type: %s", paneType)
	default:
		return fmt.Errorf("unsupported pane type: %s", paneType)
	}
}

// Cleanup terminates all panes
func (sm *PaneManager) Cleanup() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	for id, pane := range sm.panes {
		switch pane.Type {
		case "terminal":
			sm.terminal.Stop(pane)
		}
		log.Printf("Cleaned up pane %s", id)
	}
	sm.panes = make(map[string]*Pane)
	sm.terminal.Cleanup()
	sm.opencode.Cleanup()
}

// UIGroup represents a visual grouping of panes in the sidebar
type UIGroup struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	PaneIDs          []string  `json:"paneIds"`
	Layout           string    `json:"layout"`           // single, horizontal, vertical, grid
	ExpandedQuadrant string    `json:"expandedQuadrant"` // for 3-pane: top, bottom, left, right
	SplitRatio       []float64 `json:"splitRatio"`
	CellMapping      []int     `json:"cellMapping"` // maps pane positions to pane indices
}

// UIState represents the UI layout state (groups, order, etc.)
type UIState struct {
	Groups           []UIGroup `json:"groups"`
	GroupOrder       []string  `json:"groupOrder"`
	ActiveGroupID    string    `json:"activeGroupId"`
	GroupCounter     int       `json:"groupCounter"`
	SidebarCollapsed bool      `json:"sidebarCollapsed"`
	CustomNames      []string  `json:"customNames"` // pane IDs with custom names
}
