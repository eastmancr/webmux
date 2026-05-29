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
	"net/http"
	"strings"
)

// SECTION: PANE STORAGE

// PaneStorageState stores browser localStorage-style state for a shared backend.
type PaneStorageState struct {
	Items     map[string]string `json:"items"`
	Version   uint64            `json:"version"`
	UpdatedBy string            `json:"updatedBy,omitempty"`
}

const maxPaneStorageRequestSize = 10 * 1024 * 1024

type paneStorageRequest struct {
	Operation string            `json:"operation"`
	Key       string            `json:"key"`
	Value     string            `json:"value"`
	Items     map[string]string `json:"items"`
	ClientID  string            `json:"clientId"`
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

	switch req.Operation {
	case "seed":
		if len(state.Items) == 0 && state.Version == 0 {
			for key, value := range req.Items {
				state.Items[key] = value
			}
			if len(req.Items) > 0 {
				state.Version++
				state.UpdatedBy = req.ClientID
			}
		}
	case "set":
		if state.Items[req.Key] != req.Value {
			state.Items[req.Key] = req.Value
			state.Version++
			state.UpdatedBy = req.ClientID
		}
	case "remove":
		if _, ok := state.Items[req.Key]; ok {
			delete(state.Items, req.Key)
			state.Version++
			state.UpdatedBy = req.ClientID
		}
	case "clear":
		if len(state.Items) > 0 {
			state.Items = make(map[string]string)
			state.Version++
			state.UpdatedBy = req.ClientID
		}
	default:
		// Unknown operations are ignored so older pages do not break if a newer
		// page posts an operation after the server has changed versions.
	}

	snapshot := PaneStorageState{
		Items:     copyPaneStorageItems(state.Items),
		Version:   state.Version,
		UpdatedBy: state.UpdatedBy,
	}
	s.paneStorageMu.Unlock()

	return snapshot
}

func copyPaneStorageItems(items map[string]string) map[string]string {
	copy := make(map[string]string, len(items))
	for key, value := range items {
		copy[key] = value
	}
	return copy
}
