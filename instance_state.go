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
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const instanceStateVersion = 1

type PersistedBackend struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	Port         int    `json:"port"`
	PID          int    `json:"pid,omitempty"`
	ProcessGroup int    `json:"processGroup,omitempty"`
	StartTime    uint64 `json:"startTime,omitempty"`
	BootID       string `json:"bootId,omitempty"`
	Token        string `json:"token,omitempty"`
}

type PersistedPane struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	BackendID string    `json:"backendId"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

type InstanceState struct {
	Version  int                `json:"version"`
	Panes    []PersistedPane    `json:"panes"`
	Backends []PersistedBackend `json:"backends,omitempty"`
	UIState  *UIState           `json:"uiState"`
}

type InstanceLock struct {
	file *os.File
}

func AcquireInstanceLock(instanceID string) (*InstanceLock, error) {
	path := filepath.Join(webmuxInstanceDir(instanceID), "webmux.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("another webmux process owns instance %s", instanceID)
	}
	return &InstanceLock{file: file}, nil
}

func (lock *InstanceLock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	_ = syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
	return lock.file.Close()
}

func instanceStateFilePath(instanceID string) string {
	return filepath.Join(webmuxInstanceDir(instanceID), "state.json")
}

func scratchFilePath(instanceID string) string {
	return filepath.Join(webmuxInstanceDir(instanceID), "scratch.txt")
}

func LoadScratch(instanceID string) (string, error) {
	data, err := os.ReadFile(scratchFilePath(instanceID))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func SaveScratch(instanceID, text string) error {
	path := scratchFilePath(instanceID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".scratch.txt-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0600); err != nil {
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
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func LoadInstanceState(instanceID string) (*InstanceState, error) {
	data, err := os.ReadFile(instanceStateFilePath(instanceID))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var state InstanceState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse instance state: %w", err)
	}
	if state.Version != instanceStateVersion {
		return nil, fmt.Errorf("unsupported instance state version %d", state.Version)
	}
	if err := validateInstanceStateStructure(&state); err != nil {
		return nil, fmt.Errorf("invalid instance state: %w", err)
	}
	return &state, nil
}

func validateInstanceStateStructure(state *InstanceState) error {
	backendIDs := make(map[string]bool)
	for _, backend := range state.Backends {
		if backend.ID == "" || backendIDs[backend.ID] {
			return fmt.Errorf("duplicate or empty backend id %q", backend.ID)
		}
		backendIDs[backend.ID] = true
		definition, ok := definitionForPaneType(backend.Type)
		if !ok || definition.BackendLifetime != PaneBackendLifetimeInstance {
			return fmt.Errorf("unsupported persisted backend type %q", backend.Type)
		}
		if backend.Type != "opencode" || backend.ID != "opencode" || backend.Port <= 0 ||
			backend.PID <= 0 || backend.ProcessGroup <= 0 || backend.StartTime == 0 ||
			backend.BootID == "" || backend.Token == "" {
			return fmt.Errorf("incomplete persisted backend %q", backend.ID)
		}
	}

	paneIDs := make(map[string]bool)
	for _, pane := range state.Panes {
		if !validPersistedPaneID(pane.ID) || paneIDs[pane.ID] {
			return fmt.Errorf("duplicate or invalid pane id %q", pane.ID)
		}
		paneIDs[pane.ID] = true
		if _, ok := definitionForPaneType(pane.Type); !ok {
			return fmt.Errorf("unsupported pane type %q", pane.Type)
		}
		if pane.Type == "terminal" && pane.BackendID != pane.ID {
			return fmt.Errorf("terminal pane %q has invalid backend id", pane.ID)
		}
		if pane.Type == "opencode" && pane.BackendID != "opencode" {
			return fmt.Errorf("OpenCode pane %q has invalid backend id", pane.ID)
		}
	}
	return nil
}

func SaveInstanceState(instanceID string, state InstanceState) error {
	path := instanceStateFilePath(instanceID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	state.Version = instanceStateVersion
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state.json-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0600); err != nil {
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
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *Server) saveInstanceState() {
	if s.stateLoadErr != nil {
		return
	}
	s.stateSaveMu.Lock()
	defer s.stateSaveMu.Unlock()
	started := time.Now()
	s.uiStateMu.RLock()
	uiState := cloneUIState(s.uiState)
	s.uiStateMu.RUnlock()
	panes := s.manager.ListPanes()
	persistedPanes := make([]PersistedPane, 0, len(panes))
	for _, pane := range panes {
		persistedPanes = append(persistedPanes, PersistedPane{
			ID: pane.ID, Type: pane.Type, BackendID: pane.BackendID,
			Name: pane.Name, CreatedAt: pane.CreatedAt,
		})
	}
	state := InstanceState{
		Panes:    persistedPanes,
		Backends: s.manager.PersistedBackends(),
		UIState:  uiState,
	}
	if err := SaveInstanceState(s.manager.instanceID, state); err != nil {
		log.Printf("Failed to save instance state: %v", err)
		s.diagnosticf("persistence", "event=state-save status=error panes=%d backends=%d groups=%d revision=%d durationMs=%d",
			len(persistedPanes), len(state.Backends), len(uiState.Groups), uiState.Revision, time.Since(started).Milliseconds())
		return
	}
	s.diagnosticf("persistence", "event=state-save status=ok panes=%d backends=%d groups=%d revision=%d durationMs=%d",
		len(persistedPanes), len(state.Backends), len(uiState.Groups), uiState.Revision, time.Since(started).Milliseconds())
}

func cloneUIState(state *UIState) *UIState {
	if state == nil {
		return &UIState{Groups: []UIGroup{}, GroupOrder: []string{}}
	}
	clone := *state
	clone.Groups = append([]UIGroup(nil), state.Groups...)
	for i := range clone.Groups {
		clone.Groups[i].PaneIDs = append([]string(nil), state.Groups[i].PaneIDs...)
		clone.Groups[i].SplitRatio = append([]float64(nil), state.Groups[i].SplitRatio...)
		clone.Groups[i].CellMapping = append([]int(nil), state.Groups[i].CellMapping...)
	}
	clone.GroupOrder = append([]string(nil), state.GroupOrder...)
	clone.CustomNames = append([]string(nil), state.CustomNames...)
	return &clone
}
