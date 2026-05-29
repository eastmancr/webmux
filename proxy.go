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
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
)

// SECTION: PANE PROXY

// PaneProxyConfig describes how the generic proxy reaches a pane backend.
type PaneProxyConfig struct {
	TargetHost           string
	BackendName          string
	ModifyResponse       func(*Server, *http.Response) error
	ModifyIndexResponse  func(*Server, *http.Response) error
	NewWebSocketObserver func(*Server) WebSocketTrafficObserver
}

// WebSocketTrafficObserver observes backend-to-client WebSocket traffic.
type WebSocketTrafficObserver interface {
	ObserveBackendToClient([]byte)
}

// handlePaneProxy proxies all HTTP requests to the appropriate pane backend.
// Path format: /p/{paneID}/...
func (s *Server) handlePaneProxy(w http.ResponseWriter, r *http.Request) {
	// Extract pane ID from path: /p/{paneID}/...
	path := strings.TrimPrefix(r.URL.Path, "/p/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "Invalid pane path", http.StatusBadRequest)
		return
	}
	paneID := parts[0]

	if _, ok := s.manager.GetPane(paneID); !ok {
		http.Error(w, "pane not found", http.StatusNotFound)
		return
	}
	proxyConfig, ok := s.manager.ProxyConfig(paneID)
	if !ok {
		http.Error(w, "unsupported pane type", http.StatusBadGateway)
		return
	}

	// Check if this is a WebSocket upgrade request
	if r.Header.Get("Upgrade") == "websocket" {
		s.proxyWebSocket(w, r, proxyConfig, parts)
		return
	}

	// Build the target URL for HTTP requests
	targetURL := &url.URL{
		Scheme: "http",
		Host:   proxyConfig.TargetHost,
	}

	// Create reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("Pane %s %s proxy error for %s: %v", paneID, proxyConfig.BackendName, r.URL.Path, err)
		http.Error(w, "Failed to connect to "+proxyConfig.BackendName, http.StatusBadGateway)
	}

	// Modify the request
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		// Strip the /p/{paneID} prefix from the path
		if len(parts) > 1 {
			req.URL.Path = "/" + parts[1]
		} else {
			req.URL.Path = "/"
		}
		req.URL.RawPath = ""
		req.Host = targetURL.Host
	}

	isIndexRequest := len(parts) == 1 || parts[1] == "" || parts[1] == "index.html"
	if proxyConfig.ModifyResponse != nil || (isIndexRequest && proxyConfig.ModifyIndexResponse != nil) {
		proxy.ModifyResponse = func(resp *http.Response) error {
			if proxyConfig.ModifyResponse != nil {
				if err := proxyConfig.ModifyResponse(s, resp); err != nil {
					return err
				}
			}
			if isIndexRequest && proxyConfig.ModifyIndexResponse != nil {
				return proxyConfig.ModifyIndexResponse(s, resp)
			}
			return nil
		}
	}

	proxy.ServeHTTP(w, r)
}

// proxyWebSocket handles WebSocket connections by proxying to a pane backend.
func (s *Server) proxyWebSocket(w http.ResponseWriter, r *http.Request, proxyConfig *PaneProxyConfig, parts []string) {
	// Build target WebSocket path
	targetPath := "/"
	if len(parts) > 1 {
		targetPath = "/" + parts[1]
	}

	targetConn, err := net.Dial("tcp", proxyConfig.TargetHost)
	if err != nil {
		http.Error(w, "Failed to connect to "+proxyConfig.BackendName, http.StatusBadGateway)
		return
	}

	// Hijack the client connection
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		targetConn.Close()
		http.Error(w, "WebSocket not supported", http.StatusInternalServerError)
		return
	}

	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		targetConn.Close()
		http.Error(w, "Failed to hijack connection", http.StatusInternalServerError)
		return
	}

	// Forward a cloned upgrade request so path escaping, query strings, and
	// repeated headers keep normal net/http serialization semantics.
	backendReq := r.Clone(r.Context())
	backendReq.URL.Scheme = "http"
	backendReq.URL.Host = proxyConfig.TargetHost
	backendReq.URL.Path = targetPath
	backendReq.URL.RawPath = ""
	backendReq.URL.RawQuery = r.URL.RawQuery
	backendReq.Host = proxyConfig.TargetHost
	backendReq.RequestURI = ""

	// Send the upgrade request to the pane backend.
	if err := backendReq.Write(targetConn); err != nil {
		clientConn.Close()
		targetConn.Close()
		return
	}

	var observer WebSocketTrafficObserver
	if proxyConfig.NewWebSocketObserver != nil {
		observer = proxyConfig.NewWebSocketObserver(s)
	}

	// Bidirectionally copy data between client and backend
	var wg sync.WaitGroup
	wg.Add(2)

	// Backend -> Client, optionally observed by the pane runtime.
	go func() {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, err := targetConn.Read(buf)
			if err != nil {
				break
			}
			if n > 0 {
				if observer != nil {
					observer.ObserveBackendToClient(buf[:n])
				}
				if _, err := clientConn.Write(buf[:n]); err != nil {
					break
				}
			}
		}
		if tc, ok := clientConn.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()

	// Client -> Backend - pass through unchanged.
	go func() {
		defer wg.Done()
		// First flush any buffered data from the hijacked connection
		if clientBuf.Reader.Buffered() > 0 {
			io.CopyN(targetConn, clientBuf, int64(clientBuf.Reader.Buffered()))
		}
		io.Copy(targetConn, clientConn)
		if tc, ok := targetConn.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()

	wg.Wait()
	clientConn.Close()
	targetConn.Close()
}
