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
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// SECTION: PANE PROXY

// PaneProxyConfig describes how the generic proxy reaches a pane backend.
type PaneProxyConfig struct {
	TargetHost           string
	BackendName          string
	ModifyRequest        func(*http.Request)
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

	pane, ok := s.manager.GetPane(paneID)
	if !ok {
		http.Error(w, "pane not found", http.StatusNotFound)
		return
	}
	if pane.Type == "terminal" {
		s.serveTerminalPopout(w, r, pane)
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
	started := time.Now()
	diagHTTP := s.diagnosticsEnabled("proxy")
	diagHTTPPath := diagShouldLogPaneHTTP(r.URL.Path)
	if diagHTTP && diagHTTPPath {
		s.diagnosticf("proxy", "event=http-open pane=%s backend=%s method=%s path=%s remote=%s", diagSanitize(paneID, 48), diagSanitize(proxyConfig.BackendName, 48), diagSanitize(r.Method, 12), diagSanitize(r.URL.Path, 160), diagSanitize(r.RemoteAddr, 80))
	}

	// Build the target URL for HTTP requests
	targetURL := &url.URL{
		Scheme: "http",
		Host:   proxyConfig.TargetHost,
	}

	// Create reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled) {
			return
		}
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
		if proxyConfig.ModifyRequest != nil {
			proxyConfig.ModifyRequest(req)
		}
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

	responseWriter := w
	if diagHTTP {
		dw := &diagnosticResponseWriter{ResponseWriter: responseWriter}
		proxy.ServeHTTP(dw, r)
		status := dw.status
		if status == 0 {
			status = http.StatusOK
		}
		duration := time.Since(started).Milliseconds()
		if diagHTTPPath || status >= 400 || duration > 2000 {
			s.diagnosticf("proxy", "event=http-close pane=%s backend=%s method=%s path=%s status=%d durationMs=%d bytes=%d", diagSanitize(paneID, 48), diagSanitize(proxyConfig.BackendName, 48), diagSanitize(r.Method, 12), diagSanitize(r.URL.Path, 160), status, duration, dw.bytes)
		}
		return
	}
	proxy.ServeHTTP(responseWriter, r)
}

func diagShouldLogPaneHTTP(path string) bool {
	path = strings.ToLower(path)
	if strings.Contains(path, "/assets/") || strings.HasSuffix(path, ".css") || strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".png") || strings.HasSuffix(path, ".svg") || strings.HasSuffix(path, ".ico") || strings.HasSuffix(path, ".woff") || strings.HasSuffix(path, ".woff2") {
		return false
	}
	return true
}

// proxyWebSocket handles WebSocket connections by proxying to a pane backend.
func (s *Server) proxyWebSocket(w http.ResponseWriter, r *http.Request, proxyConfig *PaneProxyConfig, parts []string) {
	paneID := ""
	if len(parts) > 0 {
		paneID = parts[0]
	}
	started := time.Now()
	s.diagnosticf("proxy", "event=open pane=%s backend=%s path=%s target=%s remote=%s", diagSanitize(paneID, 48), diagSanitize(proxyConfig.BackendName, 48), diagSanitize(r.URL.Path, 160), diagSanitize(proxyConfig.TargetHost, 80), diagSanitize(r.RemoteAddr, 80))

	// Build target WebSocket path
	targetPath := "/"
	if len(parts) > 1 {
		targetPath = "/" + parts[1]
	}

	targetConn, err := net.Dial("tcp", proxyConfig.TargetHost)
	if err != nil {
		s.diagnosticf("proxy", "event=dial-error pane=%s backend=%s target=%s err=%q", diagSanitize(paneID, 48), diagSanitize(proxyConfig.BackendName, 48), diagSanitize(proxyConfig.TargetHost, 80), err.Error())
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
		s.diagnosticf("proxy", "event=hijack-error pane=%s backend=%s err=%q", diagSanitize(paneID, 48), diagSanitize(proxyConfig.BackendName, 48), err.Error())
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
	if proxyConfig.ModifyRequest != nil {
		proxyConfig.ModifyRequest(backendReq)
	}

	// Send the upgrade request to the pane backend.
	if err := backendReq.Write(targetConn); err != nil {
		s.diagnosticf("proxy", "event=upgrade-write-error pane=%s backend=%s err=%q", diagSanitize(paneID, 48), diagSanitize(proxyConfig.BackendName, 48), err.Error())
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
	var closeMu sync.Mutex
	closeDirection := ""
	var backendToClientBytes int64
	var clientToBackendBytes int64
	backendToClientErr := ""
	clientToBackendErr := ""
	var closeOnce sync.Once
	closeConnections := func() {
		closeOnce.Do(func() {
			clientConn.Close()
			targetConn.Close()
		})
	}
	setCloseDirection := func(direction string) {
		closeMu.Lock()
		if closeDirection == "" {
			closeDirection = direction
		}
		closeMu.Unlock()
	}

	// Backend -> Client, optionally observed by the pane runtime.
	go func() {
		defer wg.Done()
		defer closeConnections()
		buf := make([]byte, 32*1024)
		for {
			n, err := targetConn.Read(buf)
			if err != nil {
				backendToClientErr = err.Error()
				setCloseDirection("backend-to-client-read")
				break
			}
			if n > 0 {
				backendToClientBytes += int64(n)
				if observer != nil {
					observer.ObserveBackendToClient(buf[:n])
				}
				if _, err := clientConn.Write(buf[:n]); err != nil {
					backendToClientErr = err.Error()
					setCloseDirection("backend-to-client-write")
					break
				}
			}
		}
	}()

	// Client -> Backend - pass through unchanged.
	go func() {
		defer wg.Done()
		defer closeConnections()
		// First flush any buffered data from the hijacked connection
		if clientBuf.Reader.Buffered() > 0 {
			if n, err := io.CopyN(targetConn, clientBuf, int64(clientBuf.Reader.Buffered())); err != nil {
				clientToBackendBytes += n
				clientToBackendErr = err.Error()
				setCloseDirection("client-to-backend-buffer")
				return
			} else {
				clientToBackendBytes += n
			}
		}
		if n, err := io.Copy(targetConn, clientConn); err != nil {
			clientToBackendBytes += n
			clientToBackendErr = err.Error()
			setCloseDirection("client-to-backend-copy")
		} else {
			clientToBackendBytes += n
			setCloseDirection("client-to-backend-eof")
		}
	}()

	wg.Wait()
	if closeDirection == "" {
		closeDirection = "unknown"
	}
	s.diagnosticf("proxy", "event=close pane=%s backend=%s path=%s direction=%s durationMs=%d b2c=%d c2b=%d b2cErr=%q c2bErr=%q", diagSanitize(paneID, 48), diagSanitize(proxyConfig.BackendName, 48), diagSanitize(r.URL.Path, 160), closeDirection, time.Since(started).Milliseconds(), backendToClientBytes, clientToBackendBytes, backendToClientErr, clientToBackendErr)
	closeConnections()
}
