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
	ID              string    `json:"id"`
	Type            string    `json:"type"`
	BackendID       string    `json:"backendId"`
	BackendScope    string    `json:"backendScope"`
	BackendLifetime string    `json:"backendLifetime"`
	Name            string    `json:"name"`
	Port            int       `json:"port"`
	CreatedAt       time.Time `json:"createdAt"`
	CurrentProcess  string    `json:"currentProcess,omitempty"`
}

// PaneTypeInfo describes a pane type that can be shown in the UI.
type PaneTypeInfo struct {
	Type              string `json:"type"`
	Label             string `json:"label"`
	BackendScope      string `json:"backendScope"`
	BackendLifetime   string `json:"backendLifetime"`
	SupportsKeybar    bool   `json:"supportsKeybar"`
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailableReason,omitempty"`
	WarningReason     string `json:"warningReason,omitempty"`
	Version           string `json:"version,omitempty"`
}

type paneTypeDefinition struct {
	Type            string
	Label           string
	BackendScope    string
	BackendLifetime string
	SupportsKeybar  bool
}

type RestoreSummary struct {
	BackendCandidates  int
	BackendsAdopted    int
	TerminalCandidates int
	TerminalsAdopted   int
	OpenCodeCandidates int
	OpenCodeRestored   int
}

func definitionForPaneType(paneType string) (paneTypeDefinition, bool) {
	switch paneType {
	case "terminal":
		return paneTypeDefinition{
			Type: "terminal", Label: "Terminal", BackendScope: PaneBackendDedicated,
			BackendLifetime: PaneBackendLifetimePane, SupportsKeybar: true,
		}, true
	case "opencode":
		return paneTypeDefinition{
			Type: "opencode", Label: "OpenCode", BackendScope: PaneBackendShared,
			BackendLifetime: PaneBackendLifetimeInstance,
		}, true
	default:
		return paneTypeDefinition{}, false
	}
}

const (
	PaneBackendDedicated        = "dedicated"
	PaneBackendShared           = "shared"
	PaneBackendLifetimePane     = "pane"
	PaneBackendLifetimeInstance = "instance"
	paneTypeVersionTTL          = 30 * time.Second
)

// SECTION: SESSIONS

// PaneManager handles generic pane lifecycle and dispatches type-specific work.
type PaneManager struct {
	panes                    map[string]*Pane
	mu                       sync.RWMutex
	createMu                 sync.Mutex
	nextPaneID               int32
	startPort                int32
	shell                    string
	workDir                  string // Starting directory for new panes
	getSettings              func() *Settings
	serverPort               string // HTTP server port for WEBMUX_PORT env var
	instanceID               string // Runtime namespace derived from the HTTP server port
	onPaneClosed             func(string)
	onPaneChanged            func(*Pane)
	onStateChanged           func()
	terminal                 *TerminalRuntime
	opencode                 *OpenCodeRuntime
	versionMu                sync.Mutex
	opencodeVersion          string
	opencodeVersionCheckedAt time.Time
}

// NewPaneManager creates a new pane manager
func NewPaneManager(startPort int, shell, workDir, serverPort string) *PaneManager {
	sm := &PaneManager{
		panes:      make(map[string]*Pane),
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
		} else if paneType == "opencode" {
			if existingPort, ok := sm.opencode.RunningBackend(paneType); ok {
				port = existingPort
				backendID = paneType
			}
		} else {
			startedBackend = true
		}
		if backendID == "" {
			var err error
			port, err = sm.allocatePanePort()
			if err != nil {
				return nil, err
			}
			backendID = paneType
			startedBackend = true
		}
	} else if paneType == "terminal" {
		// Terminal panes attach through the main Webmux server and do not need
		// a dedicated TCP listener. Their ID still comes from nextPaneID.
		startedBackend = true
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
		ID:              id,
		Type:            paneType,
		BackendID:       backendID,
		BackendScope:    scope,
		BackendLifetime: sm.backendLifetime(paneType),
		Name:            name,
		Port:            port,
		CreatedAt:       time.Now(),
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
	} else if port == 0 {
		log.Printf("Created pane %s", id)
	} else {
		log.Printf("Created pane %s on port %d", id, port)
	}

	sm.mu.Lock()
	sm.panes[id] = pane
	sm.mu.Unlock()
	sm.stateChanged()

	if startedBackend {
		sm.monitorPaneRuntime(pane)
	}

	return pane, nil
}

func (sm *PaneManager) isSupportedPaneType(paneType string) bool {
	_, ok := definitionForPaneType(paneType)
	return ok
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
	terminal, _ := definitionForPaneType("terminal")
	opencode, _ := definitionForPaneType("opencode")
	opencodeAvailable, opencodeReason := sm.paneTypeAvailability("opencode")
	opencodeVersion := ""
	if opencodeAvailable {
		opencodeVersion = sm.cachedOpenCodeVersion()
	}
	opencodeWarning := ""
	if sm.opencode != nil {
		opencodeWarning = sm.opencode.WarningReason()
	}
	return []PaneTypeInfo{
		{
			Type:            terminal.Type,
			Label:           terminal.Label,
			BackendScope:    terminal.BackendScope,
			BackendLifetime: terminal.BackendLifetime,
			SupportsKeybar:  terminal.SupportsKeybar,
			Available:       true,
		},
		{
			Type:              opencode.Type,
			Label:             opencode.Label,
			BackendScope:      opencode.BackendScope,
			BackendLifetime:   opencode.BackendLifetime,
			SupportsKeybar:    opencode.SupportsKeybar,
			Available:         opencodeAvailable,
			UnavailableReason: opencodeReason,
			WarningReason:     opencodeWarning,
			Version:           opencodeVersion,
		},
	}
}

func (sm *PaneManager) cachedOpenCodeVersion() string {
	sm.versionMu.Lock()
	defer sm.versionMu.Unlock()

	if time.Since(sm.opencodeVersionCheckedAt) < paneTypeVersionTTL {
		return sm.opencodeVersion
	}
	sm.opencodeVersion = paneTypeCommandVersion("opencode")
	sm.opencodeVersionCheckedAt = time.Now()
	return sm.opencodeVersion
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
	definition, ok := definitionForPaneType(paneType)
	if !ok {
		return ""
	}
	return definition.BackendScope
}

func (sm *PaneManager) backendLifetime(paneType string) string {
	definition, ok := definitionForPaneType(paneType)
	if !ok {
		return ""
	}
	return definition.BackendLifetime
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

func (sm *PaneManager) deletePanesForBackend(backendID string) []string {
	deleted := []string{}
	for id, pane := range sm.panes {
		if pane.BackendID == backendID {
			sm.deletePane(id)
			deleted = append(deleted, id)
		}
	}
	return deleted
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
	for offset := int32(1); offset <= 100; offset++ {
		port := int(sm.startPort + offset)
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

func (sm *PaneManager) PersistedBackends() []PersistedBackend {
	backends := make([]PersistedBackend, 0, 1)
	if backend, ok := sm.opencode.PersistedBackend("opencode"); ok {
		backends = append(backends, backend)
	}
	return backends
}

// Restore adopts only backends that survived the previous webmux process.
// Missing runtimes are discarded rather than recreated after a machine reboot.
func (sm *PaneManager) Restore(panes []PersistedPane, backends []PersistedBackend) RestoreSummary {
	summary := RestoreSummary{BackendCandidates: len(backends)}
	seenBackends := make(map[string]bool)
	for _, backend := range backends {
		if backend.Type != "opencode" || backend.ID != "opencode" || seenBackends[backend.ID] {
			continue
		}
		seenBackends[backend.ID] = true
		if err := sm.opencode.Adopt(backend); err != nil {
			log.Printf("Could not restore OpenCode backend %s: %v", backend.ID, err)
		} else {
			summary.BackendsAdopted++
		}
	}

	restoredOpenCode := false
	seenPanes := make(map[string]bool)
	for _, saved := range panes {
		if !validPersistedPaneID(saved.ID) || !sm.isSupportedPaneType(saved.Type) || seenPanes[saved.ID] {
			continue
		}
		seenPanes[saved.ID] = true
		pane := Pane{
			ID: saved.ID, Type: saved.Type, BackendID: saved.BackendID,
			BackendScope: sm.backendScope(saved.Type), BackendLifetime: sm.backendLifetime(saved.Type),
			Name: saved.Name, CreatedAt: saved.CreatedAt,
		}
		if pane.BackendID == "" {
			pane.BackendID = pane.ID
		}
		switch pane.Type {
		case "terminal":
			summary.TerminalCandidates++
			if !sm.terminal.Adopt(&pane) {
				log.Printf("Discarding terminal pane %s: tmux session did not survive", pane.ID)
				continue
			}
			summary.TerminalsAdopted++
		case "opencode":
			summary.OpenCodeCandidates++
			if !sm.opencode.IsRunning(pane.BackendID) {
				log.Printf("Discarding OpenCode pane %s: backend did not survive", pane.ID)
				continue
			}
			if port, ok := sm.opencode.RunningBackend(pane.BackendID); ok {
				pane.Port = port
			}
			restoredOpenCode = true
			summary.OpenCodeRestored++
		}

		sm.mu.Lock()
		if _, exists := sm.panes[pane.ID]; !exists {
			copy := pane
			sm.panes[pane.ID] = &copy
			sm.advancePaneID(pane.ID)
		}
		sm.mu.Unlock()
		if pane.Type == "terminal" {
			sm.monitorPaneRuntime(&pane)
		}
	}
	if restoredOpenCode {
		if pane := sm.findSharedBackendPane("opencode"); pane != nil {
			sm.monitorPaneRuntime(pane)
		}
	}
	return summary
}

func validPersistedPaneID(id string) bool {
	if !strings.HasPrefix(id, "pane-") {
		return false
	}
	value, err := strconv.Atoi(strings.TrimPrefix(id, "pane-"))
	return err == nil && value > 0
}

func (sm *PaneManager) advancePaneID(id string) {
	value, err := strconv.Atoi(strings.TrimPrefix(id, "pane-"))
	if err != nil {
		return
	}
	for {
		current := atomic.LoadInt32(&sm.nextPaneID)
		if int32(value) <= current || atomic.CompareAndSwapInt32(&sm.nextPaneID, current, int32(value)) {
			return
		}
	}
}

// ClosePane terminates a pane and its runtime backend.
func (sm *PaneManager) ClosePane(id string) error {
	sm.createMu.Lock()
	defer sm.createMu.Unlock()

	sm.mu.RLock()
	pane, ok := sm.panes[id]
	if !ok {
		sm.mu.RUnlock()
		return fmt.Errorf("pane not found: %s", id)
	}
	paneCopy := *pane
	sm.mu.RUnlock()

	if paneCopy.Type == "terminal" {
		if err := sm.terminal.Stop(&paneCopy); err != nil {
			return err
		}
	}

	sm.mu.Lock()
	if _, ok := sm.panes[id]; !ok {
		sm.mu.Unlock()
		return nil
	}
	sm.deletePane(id)
	stopBackend := sm.backendLifetime(paneCopy.Type) == PaneBackendLifetimePane &&
		!sm.hasPaneForBackend(paneCopy.BackendID)
	sm.mu.Unlock()
	sm.notifyPaneClosed(id)

	switch paneCopy.Type {
	case "opencode":
		if stopBackend {
			sm.opencode.Stop(&paneCopy)
		}
	}
	log.Printf("Closed pane %s", id)

	sm.stateChanged()

	return nil
}

func (sm *PaneManager) resetCounters() {
	atomic.StoreInt32(&sm.nextPaneID, sm.startPort)
	log.Printf("All panes closed, reset pane ID counter to %d", sm.startPort)
}

// deletePane removes a pane from the map.
// Must be called with sm.mu held
func (sm *PaneManager) deletePane(id string) {
	delete(sm.panes, id)
}

func (sm *PaneManager) notifyPaneClosed(id string) {
	if sm.onPaneClosed != nil {
		sm.onPaneClosed(id)
	}
}

// RenamePane changes the display name of a pane
func (sm *PaneManager) RenamePane(id, name string) error {
	sm.mu.Lock()
	pane, ok := sm.panes[id]
	if !ok {
		sm.mu.Unlock()
		return fmt.Errorf("pane not found: %s", id)
	}

	pane.Name = name
	sm.mu.Unlock()
	sm.stateChanged()
	return nil
}

func (sm *PaneManager) stateChanged() {
	if sm.onStateChanged != nil {
		sm.onStateChanged()
	}
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

// ForceCleanup asks all backends to exit and kills any that exceed timeout.
func (sm *PaneManager) ForceCleanup(timeout time.Duration) bool {
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		results <- sm.terminal.Shutdown(timeout)
	}()
	go func() {
		defer wg.Done()
		results <- sm.opencode.Shutdown(timeout)
	}()
	wg.Wait()
	close(results)
	allStopped := true
	for stopped := range results {
		allStopped = allStopped && stopped
	}
	if !allStopped {
		sm.stateChanged()
		return false
	}

	sm.mu.Lock()
	ids := make([]string, 0, len(sm.panes))
	for id := range sm.panes {
		ids = append(ids, id)
	}
	sm.panes = make(map[string]*Pane)
	sm.resetCounters()
	sm.mu.Unlock()
	for _, id := range ids {
		if sm.onPaneClosed != nil {
			sm.onPaneClosed(id)
		}
	}
	sm.stateChanged()
	return true
}

func (sm *PaneManager) Cleanup() {
	sm.ForceCleanup(3 * time.Second)
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
	Revision         uint64    `json:"revision"`
	Groups           []UIGroup `json:"groups"`
	GroupOrder       []string  `json:"groupOrder"`
	ActiveGroupID    string    `json:"activeGroupId"`
	FocusedPaneID    string    `json:"focusedPaneId"`
	GroupCounter     int       `json:"groupCounter"`
	SidebarCollapsed bool      `json:"sidebarCollapsed"`
	CustomNames      []string  `json:"customNames"` // pane IDs with custom names
}
