//go:build dev
/* *
 * Webmux - a browser-based terminal multiplexer
 * Copyright (C) 2025  Webmux contributors
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
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// DevMode state for live reload
type DevMode struct {
	enabled   bool
	clients   map[*websocket.Conn]bool
	clientsMu sync.RWMutex
	staticDir string
}

var devMode = &DevMode{
	enabled: true,
	clients: make(map[*websocket.Conn]bool),
}

// WebSocket upgrader for dev reload
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func init() {
	log.Println("[dev] Dev mode compiled in")
}

// InitDevMode sets up dev mode if enabled
func InitDevMode(mux *http.ServeMux, server *Server) http.Handler {
	// Get the directory where the executable is
	exe, _ := os.Executable()
	devMode.staticDir = filepath.Join(filepath.Dir(exe), "static")
	log.Printf("[dev] Watching %s for changes", devMode.staticDir)

	// Add dev reload endpoint
	mux.HandleFunc("/api/dev-reload", server.handleDevReload)

	// Start file watcher
	go watchStaticFiles(devMode.staticDir)

	// Return filesystem handler with no-cache headers
	return noCacheHandler(http.FileServer(http.Dir(devMode.staticDir)))
}

// handleDevReload handles WebSocket connections for live reload
func (s *Server) handleDevReload(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	devMode.clientsMu.Lock()
	devMode.clients[conn] = true
	devMode.clientsMu.Unlock()

	log.Printf("[dev] Reload client connected (%d total)", len(devMode.clients))

	// Keep connection open, remove on close
	defer func() {
		devMode.clientsMu.Lock()
		delete(devMode.clients, conn)
		devMode.clientsMu.Unlock()
		conn.Close()
		log.Printf("[dev] Reload client disconnected (%d total)", len(devMode.clients))
	}()

	// Just keep reading to detect close
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
}

// notifyReload tells all connected dev clients to reload
func notifyReload() {
	devMode.clientsMu.RLock()
	defer devMode.clientsMu.RUnlock()

	log.Printf("[dev] Notifying %d clients to reload", len(devMode.clients))
	for conn := range devMode.clients {
		conn.WriteMessage(websocket.TextMessage, []byte("reload"))
	}
}

// watchStaticFiles watches the static directory for changes
func watchStaticFiles(dir string) {
	lastMod := make(map[string]time.Time)

	// Initial scan
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			lastMod[path] = info.ModTime()
		}
		return nil
	})

	for {
		time.Sleep(500 * time.Millisecond)

		changed := false
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if last, ok := lastMod[path]; !ok || info.ModTime().After(last) {
				if ok {
					log.Printf("[dev] File changed: %s", filepath.Base(path))
					changed = true
				}
				lastMod[path] = info.ModTime()
			}
			return nil
		})

		if changed {
			notifyReload()
		}
	}
}

// noCacheHandler wraps a handler to add no-cache headers
func noCacheHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		h.ServeHTTP(w, r)
	})
}
