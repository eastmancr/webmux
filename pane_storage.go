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
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gorilla/websocket"
)

// SECTION: PANE STORAGE

// PaneStorageState stores browser localStorage-style state for a shared backend.
type PaneStorageState struct {
	Items     map[string]string `json:"items"`
	Version   uint64            `json:"version"`
	UpdatedBy string            `json:"updatedBy,omitempty"`
}

type paneStorageEvent struct {
	Type      string `json:"type"`
	Version   uint64 `json:"version"`
	UpdatedBy string `json:"updatedBy,omitempty"`
}

type paneStorageAdminResponse struct {
	Namespace string            `json:"namespace"`
	Items     map[string]string `json:"items"`
	Version   uint64            `json:"version"`
	UpdatedBy string            `json:"updatedBy,omitempty"`
	KeyCount  int               `json:"keyCount"`
	SizeBytes int               `json:"sizeBytes"`
}

const maxPaneStorageRequestSize = 10 * 1024 * 1024

var paneStorageUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func (s *Server) notifyPaneStorageSubscribers(backendID string, event paneStorageEvent) {
	s.paneStorageSubMu.Lock()
	defer s.paneStorageSubMu.Unlock()

	for ch := range s.paneStorageSubs[backendID] {
		select {
		case ch <- event:
		default:
			s.diagnosticf("storage-events", "event=drop backend=%s type=%s version=%d", diagSanitize(backendID, 48), diagSanitize(event.Type, 32), event.Version)
		}
	}
}

func (s *Server) handlePaneStorageEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	backendID := strings.TrimPrefix(r.URL.Path, "/api/pane-storage-events/")
	backendID = strings.Trim(backendID, "/")
	if backendID == "" {
		http.Error(w, "missing backend id", http.StatusBadRequest)
		return
	}
	if !s.manager.BackendExists(backendID) {
		http.Error(w, "backend not found", http.StatusNotFound)
		return
	}

	conn, err := paneStorageUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	s.diagnosticf("storage-events", "event=connect backend=%s remote=%s", diagSanitize(backendID, 48), diagSanitize(r.RemoteAddr, 80))

	ch := make(chan paneStorageEvent, 20)
	s.paneStorageSubMu.Lock()
	if s.paneStorageSubs[backendID] == nil {
		s.paneStorageSubs[backendID] = make(map[chan paneStorageEvent]struct{})
	}
	s.paneStorageSubs[backendID][ch] = struct{}{}
	s.paneStorageSubMu.Unlock()
	defer func() {
		s.paneStorageSubMu.Lock()
		delete(s.paneStorageSubs[backendID], ch)
		if len(s.paneStorageSubs[backendID]) == 0 {
			delete(s.paneStorageSubs, backendID)
		}
		s.paneStorageSubMu.Unlock()
		close(ch)
	}()

	snapshot := s.getPaneStorageSnapshot(backendID)
	if err := conn.WriteJSON(paneStorageEvent{Type: "storage", Version: snapshot.Version, UpdatedBy: snapshot.UpdatedBy}); err != nil {
		s.diagnosticf("storage-events", "event=write-error backend=%s version=%d err=%q", diagSanitize(backendID, 48), snapshot.Version, err.Error())
		return
	}
	s.diagnosticf("storage-events", "event=initial backend=%s version=%d updatedBy=%s", diagSanitize(backendID, 48), snapshot.Version, diagSanitize(snapshot.UpdatedBy, 80))

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.NextReader(); err != nil {
				s.diagnosticf("storage-events", "event=read-close backend=%s err=%q", diagSanitize(backendID, 48), err.Error())
				return
			}
		}
	}()

	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return
			}
			if err := conn.WriteJSON(event); err != nil {
				s.diagnosticf("storage-events", "event=write-error backend=%s version=%d err=%q", diagSanitize(backendID, 48), event.Version, err.Error())
				return
			}
			s.diagnosticf("storage-events", "event=sent backend=%s version=%d updatedBy=%s", diagSanitize(backendID, 48), event.Version, diagSanitize(event.UpdatedBy, 80))
		case <-done:
			s.diagnosticf("storage-events", "event=disconnect backend=%s reason=reader", diagSanitize(backendID, 48))
			return
		case <-r.Context().Done():
			s.diagnosticf("storage-events", "event=disconnect backend=%s reason=context", diagSanitize(backendID, 48))
			return
		}
	}
}

func paneStorageDir() string {
	return filepath.Join(xdgDataHome(), "webmux", "pane-storage")
}

func paneStorageFilePath(namespace string) (string, error) {
	name := sanitizePaneStorageNamespace(namespace)
	if name == "" {
		return "", fmt.Errorf("invalid storage namespace")
	}
	return filepath.Join(paneStorageDir(), name+".json"), nil
}

func sanitizePaneStorageNamespace(namespace string) string {
	namespace = strings.TrimSpace(namespace)
	var b strings.Builder
	b.Grow(len(namespace))
	for _, r := range namespace {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "._-")
}

func LoadPaneStorage() map[string]*PaneStorageState {
	states := make(map[string]*PaneStorageState)
	entries, err := os.ReadDir(paneStorageDir())
	if err != nil {
		return states
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		namespace := strings.TrimSuffix(entry.Name(), ".json")
		path := filepath.Join(paneStorageDir(), entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			log.Printf("Failed to load pane storage %s: %v", namespace, err)
			continue
		}
		var state PaneStorageState
		if err := json.Unmarshal(data, &state); err != nil {
			log.Printf("Failed to parse pane storage %s: %v", namespace, err)
			continue
		}
		if state.Items == nil {
			state.Items = make(map[string]string)
		}
		if state.Version == 0 && len(state.Items) > 0 {
			state.Version = 1
		}
		states[namespace] = &state
	}
	return states
}

func SavePaneStorage(namespace string, state PaneStorageState) error {
	path, err := paneStorageFilePath(namespace)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
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
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

type paneStorageRequest struct {
	Operation  string               `json:"operation"`
	Key        string               `json:"key"`
	Value      string               `json:"value"`
	Items      map[string]string    `json:"items"`
	ClientID   string               `json:"clientId"`
	Operations []paneStorageRequest `json:"operations"`
}

func (s *Server) handlePaneStorage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	backendID := strings.TrimPrefix(r.URL.Path, "/api/pane-storage/")
	backendID = strings.Trim(backendID, "/")
	if backendID == "" {
		http.Error(w, "missing backend id", http.StatusBadRequest)
		return
	}
	if !s.manager.BackendExists(backendID) {
		http.Error(w, "backend not found", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		json.NewEncoder(w).Encode(s.getPaneStorageSnapshot(backendID))

	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, maxPaneStorageRequestSize)
		var req paneStorageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid storage request: "+err.Error(), http.StatusBadRequest)
			return
		}
		snapshot := s.applyPaneStorageRequest(backendID, req)
		json.NewEncoder(w).Encode(snapshot)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) getPaneStorageSnapshot(backendID string) PaneStorageState {
	s.paneStorageMu.RLock()
	state := s.paneStorage[backendID]
	if state == nil {
		s.paneStorageMu.RUnlock()
		return PaneStorageState{Items: map[string]string{}}
	}
	items := copyPaneStorageItems(state.Items)
	version := state.Version
	updatedBy := state.UpdatedBy
	s.paneStorageMu.RUnlock()

	return PaneStorageState{Items: items, Version: version, UpdatedBy: updatedBy}
}

func (s *Server) applyPaneStorageRequest(backendID string, req paneStorageRequest) PaneStorageState {
	s.paneStorageMu.Lock()
	state := s.paneStorage[backendID]
	if state == nil {
		state = &PaneStorageState{Items: make(map[string]string)}
		s.paneStorage[backendID] = state
	}
	changed := false

	operations := []paneStorageRequest{req}
	if req.Operation == "batch" {
		operations = req.Operations
	}
	for _, op := range operations {
		clientID := op.ClientID
		if clientID == "" {
			clientID = req.ClientID
		}

		switch op.Operation {
		case "seed":
			if len(state.Items) == 0 && state.Version == 0 {
				for key, value := range op.Items {
					state.Items[key] = value
				}
				if len(op.Items) > 0 {
					state.Version++
					state.UpdatedBy = clientID
					changed = true
				}
			}
		case "set":
			if state.Items[op.Key] != op.Value {
				state.Items[op.Key] = op.Value
				state.Version++
				state.UpdatedBy = clientID
				changed = true
			}
		case "remove":
			if _, ok := state.Items[op.Key]; ok {
				delete(state.Items, op.Key)
				state.Version++
				state.UpdatedBy = clientID
				changed = true
			}
		case "clear":
			if len(state.Items) > 0 {
				state.Items = make(map[string]string)
				state.Version++
				state.UpdatedBy = clientID
				changed = true
			}
		default:
			// Unknown operations are ignored so older pages do not break if a newer
			// page posts an operation after the server has changed versions.
		}
	}

	snapshot := PaneStorageState{
		Items:     copyPaneStorageItems(state.Items),
		Version:   state.Version,
		UpdatedBy: state.UpdatedBy,
	}
	s.paneStorageMu.Unlock()

	if err := SavePaneStorage(backendID, snapshot); err != nil {
		log.Printf("Failed to save pane storage %s: %v", backendID, err)
	}
	if changed {
		s.diagnosticf("storage-events", "event=updated backend=%s version=%d updatedBy=%s operations=%d", diagSanitize(backendID, 48), snapshot.Version, diagSanitize(snapshot.UpdatedBy, 80), len(operations))
		if s.diagnosticsEnabled("storage-events") {
			keys := make([]string, 0, min(len(operations), 12))
			for _, op := range operations {
				if op.Key == "" {
					keys = append(keys, op.Operation)
					continue
				}
				keys = append(keys, op.Operation+":"+diagSanitize(op.Key, 140))
				if len(keys) >= 12 {
					break
				}
			}
			s.diagnosticf("storage-events", "event=updated-keys backend=%s version=%d keys=%s", diagSanitize(backendID, 48), snapshot.Version, strings.Join(keys, ","))
		}
		s.notifyPaneStorageSubscribers(backendID, paneStorageEvent{Type: "storage", Version: snapshot.Version, UpdatedBy: snapshot.UpdatedBy})
	}

	return snapshot
}

func (s *Server) handleStorageAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	namespace := strings.TrimPrefix(r.URL.Path, "/api/storage/")
	namespace = strings.Trim(namespace, "/")
	namespace = sanitizePaneStorageNamespace(namespace)
	if namespace == "" {
		http.Error(w, "missing storage namespace", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		snapshot := s.getPaneStorageSnapshot(namespace)
		json.NewEncoder(w).Encode(paneStorageAdminSnapshot(namespace, snapshot))

	case http.MethodPut:
		r.Body = http.MaxBytesReader(w, r.Body, maxPaneStorageRequestSize)
		var req struct {
			Items map[string]string `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid storage import: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Items == nil {
			req.Items = make(map[string]string)
		}
		snapshot := s.replacePaneStorage(namespace, req.Items, "admin")
		json.NewEncoder(w).Encode(paneStorageAdminSnapshot(namespace, snapshot))

	case http.MethodDelete:
		snapshot := s.replacePaneStorage(namespace, map[string]string{}, "admin")
		json.NewEncoder(w).Encode(paneStorageAdminSnapshot(namespace, snapshot))

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) replacePaneStorage(namespace string, items map[string]string, updatedBy string) PaneStorageState {
	s.paneStorageMu.Lock()
	state := s.paneStorage[namespace]
	if state == nil {
		state = &PaneStorageState{Items: make(map[string]string)}
		s.paneStorage[namespace] = state
	}
	state.Items = copyPaneStorageItems(items)
	state.Version++
	state.UpdatedBy = updatedBy
	snapshot := PaneStorageState{
		Items:     copyPaneStorageItems(state.Items),
		Version:   state.Version,
		UpdatedBy: state.UpdatedBy,
	}
	s.paneStorageMu.Unlock()

	if err := SavePaneStorage(namespace, snapshot); err != nil {
		log.Printf("Failed to save pane storage %s: %v", namespace, err)
	}
	s.notifyPaneStorageSubscribers(namespace, paneStorageEvent{Type: "storage", Version: snapshot.Version, UpdatedBy: snapshot.UpdatedBy})
	return snapshot
}

func paneStorageAdminSnapshot(namespace string, state PaneStorageState) paneStorageAdminResponse {
	items := copyPaneStorageItems(state.Items)
	data, err := json.Marshal(items)
	size := 0
	if err == nil {
		size = len(data)
	}
	return paneStorageAdminResponse{
		Namespace: namespace,
		Items:     items,
		Version:   state.Version,
		UpdatedBy: state.UpdatedBy,
		KeyCount:  len(items),
		SizeBytes: size,
	}
}

func copyPaneStorageItems(items map[string]string) map[string]string {
	copy := make(map[string]string, len(items))
	for key, value := range items {
		copy[key] = value
	}
	return copy
}
