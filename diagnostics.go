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
	"regexp"
	"strings"
)

// SECTION: DIAGNOSTICS

var diagTokenRE = regexp.MustCompile(`[^A-Za-z0-9_./:=+@-]+`)

type clientDiagnosticEvent struct {
	Source    string         `json:"source"`
	Event     string         `json:"event"`
	PaneID    string         `json:"paneId,omitempty"`
	BackendID string         `json:"backendId,omitempty"`
	PaneType  string         `json:"paneType,omitempty"`
	Path      string         `json:"path,omitempty"`
	AgeMS     float64        `json:"ageMs,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
}

type diagnosticResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *diagnosticResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *diagnosticResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += int64(n)
	return n, err
}

func (w *diagnosticResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (s *Server) diagnosticsSettings() DiagnosticsSettings {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	if s.settings == nil {
		return DiagnosticsSettings{}
	}
	return s.settings.Diagnostics
}

func (s *Server) diagnosticsEnabled(category string) bool {
	d := s.diagnosticsSettings()
	if !d.Enabled {
		return false
	}
	switch category {
	case "client":
		return d.ClientEvents || d.IframeLifecycle
	case "proxy":
		return d.ProxyWebSockets
	case "pane-events":
		return d.PaneEvents
	case "storage-events":
		return d.StorageEvents
	case "iframe":
		return d.IframeLifecycle || d.ClientEvents
	default:
		return true
	}
}

func (s *Server) diagnosticf(category, format string, args ...any) {
	if !s.diagnosticsEnabled(category) {
		return
	}
	log.Printf("[diag %s] %s", category, fmt.Sprintf(format, args...))
}

func diagSanitize(value string, maxLen int) string {
	value = strings.TrimSpace(value)
	if maxLen > 0 && len(value) > maxLen {
		value = value[:maxLen] + "..."
	}
	if value == "" {
		return "-"
	}
	return diagTokenRE.ReplaceAllString(value, "_")
}

func diagString(value any) string {
	switch v := value.(type) {
	case string:
		return diagSanitize(v, 160)
	case float64, bool, nil:
		return fmt.Sprintf("%v", v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return diagSanitize(fmt.Sprintf("%v", v), 160)
		}
		return diagSanitize(string(b), 160)
	}
}

func (s *Server) handleClientDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.diagnosticsEnabled("client") {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var events []clientDiagnosticEvent
	if err := json.NewDecoder(r.Body).Decode(&events); err != nil {
		http.Error(w, "invalid diagnostics payload", http.StatusBadRequest)
		return
	}
	if len(events) > 50 {
		events = events[:50]
	}

	for _, event := range events {
		parts := []string{
			"source=" + diagSanitize(event.Source, 48),
			"event=" + diagSanitize(event.Event, 48),
		}
		if event.PaneID != "" {
			parts = append(parts, "pane="+diagSanitize(event.PaneID, 48))
		}
		if event.BackendID != "" {
			parts = append(parts, "backend="+diagSanitize(event.BackendID, 48))
		}
		if event.PaneType != "" {
			parts = append(parts, "type="+diagSanitize(event.PaneType, 48))
		}
		if event.Path != "" {
			parts = append(parts, "path="+diagSanitize(event.Path, 160))
		}
		if event.AgeMS > 0 {
			parts = append(parts, fmt.Sprintf("ageMs=%.0f", event.AgeMS))
		}
		for key, value := range event.Data {
			parts = append(parts, diagSanitize(key, 32)+"="+diagString(value))
		}
		log.Printf("[diag client] %s", strings.Join(parts, " "))
	}
	w.WriteHeader(http.StatusNoContent)
}
