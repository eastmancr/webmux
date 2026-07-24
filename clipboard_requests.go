/*
 * webmux - Browser-based pane multiplexer
 * Copyright (C) 2026 Webmux contributors
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 */

package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	clipboardRequestTimeout = 15 * time.Second
	clipboardReuseWindow    = 5 * time.Second
	maxClipboardTypes       = 16
	maxClipboardFiles       = 10
	maxClipboardFilesSize   = 25 * 1024 * 1024
)

type typedClipboardOffer struct {
	Representations map[string][]byte
	CapturedAt      time.Time
	ReuseUntil      time.Time
}

type clipboardReadRequest struct {
	ID        string
	MIME      string
	ListTypes bool
	Result    chan typedClipboardOffer
}

type clipboardEvent struct {
	Type      string `json:"type"`
	Version   uint64 `json:"version,omitempty"`
	RequestID string `json:"requestId,omitempty"`
	MIME      string `json:"mime,omitempty"`
	ListTypes bool   `json:"listTypes,omitempty"`
}

func normalizeClipboardMIME(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "text/plain", nil
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil || len(mediaType) > 255 {
		return "", fmt.Errorf("invalid clipboard MIME type")
	}
	return strings.ToLower(mediaType), nil
}

func (s *Server) setTypedClipboard(offer typedClipboardOffer) {
	cloned := cloneTypedClipboard(offer)
	s.clipboardMu.Lock()
	s.clipboardOffer = cloned
	s.clipboardMu.Unlock()
}

func cloneTypedClipboard(offer typedClipboardOffer) typedClipboardOffer {
	cloned := typedClipboardOffer{
		Representations: make(map[string][]byte, len(offer.Representations)),
		CapturedAt:      offer.CapturedAt,
		ReuseUntil:      offer.ReuseUntil,
	}
	for contentType, data := range offer.Representations {
		cloned.Representations[contentType] = append([]byte(nil), data...)
	}
	return cloned
}

func (s *Server) currentTypedClipboard() typedClipboardOffer {
	s.clipboardMu.RLock()
	defer s.clipboardMu.RUnlock()
	return cloneTypedClipboard(s.clipboardOffer)
}

func clipboardTypes(offer typedClipboardOffer) []string {
	types := make([]string, 0, len(offer.Representations))
	for contentType := range offer.Representations {
		types = append(types, contentType)
	}
	sort.Strings(types)
	return types
}

func clipboardOfferSummary(offer typedClipboardOffer) string {
	types := clipboardTypes(offer)
	parts := make([]string, 0, len(types))
	for _, contentType := range types {
		parts = append(parts, fmt.Sprintf("%s=%dB", contentType, len(offer.Representations[contentType])))
	}
	if len(parts) == 0 {
		return "empty"
	}
	return strings.Join(parts, ",")
}

func clipboardLogID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func randomClipboardRequestID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func (s *Server) handleClipboardRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !terminalOriginAllowed(r) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	listTypes := r.URL.Query().Get("list-types") == "1"
	requestedType, err := normalizeClipboardMIME(r.URL.Query().Get("type"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	current := s.currentTypedClipboard()
	if !listTypes && time.Now().Before(current.ReuseUntil) && current.Representations[requestedType] != nil {
		log.Printf("[clipboard] list continuation reused type=%q offer=%s", requestedType, clipboardOfferSummary(current))
		s.writeClipboardOfferResponse(w, current, requestedType, false)
		return
	}

	id, err := randomClipboardRequestID()
	if err != nil {
		http.Error(w, "Failed to create clipboard request", http.StatusInternalServerError)
		return
	}
	request := &clipboardReadRequest{
		ID:        id,
		MIME:      requestedType,
		ListTypes: listTypes,
		Result:    make(chan typedClipboardOffer, 1),
	}

	s.clipboardRequestMu.Lock()
	if len(s.clipboardRequests) != 0 {
		s.clipboardRequestMu.Unlock()
		log.Printf("[clipboard] request rejected because another request is pending type=%q list=%t", requestedType, listTypes)
		http.Error(w, "Another clipboard request is already pending", http.StatusTooManyRequests)
		return
	}
	s.clipboardRequests[id] = request
	s.clipboardRequestMu.Unlock()
	defer func() {
		s.clipboardRequestMu.Lock()
		delete(s.clipboardRequests, id)
		s.clipboardRequestMu.Unlock()
	}()

	s.notifyClipboardSubscribers(clipboardEvent{
		Type:      "clipboard-request",
		RequestID: id,
		MIME:      requestedType,
		ListTypes: listTypes,
	})
	log.Printf("[clipboard] request dispatched id=%s type=%q list=%t", clipboardLogID(id), requestedType, listTypes)

	timer := time.NewTimer(clipboardRequestTimeout)
	defer timer.Stop()
	select {
	case offer := <-request.Result:
		log.Printf("[clipboard] request fulfilled id=%s offer=%s", clipboardLogID(id), clipboardOfferSummary(offer))
		s.writeClipboardOfferResponse(w, offer, requestedType, listTypes)
	case <-timer.C:
		log.Printf("[clipboard] request timed out id=%s type=%q list=%t", clipboardLogID(id), requestedType, listTypes)
		http.Error(w, "Clipboard request timed out", http.StatusGatewayTimeout)
	case <-r.Context().Done():
		log.Printf("[clipboard] request cancelled id=%s", clipboardLogID(id))
		return
	}
}

func (s *Server) writeClipboardOfferResponse(w http.ResponseWriter, offer typedClipboardOffer, requestedType string, listTypes bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if listTypes {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		for _, contentType := range clipboardTypes(offer) {
			fmt.Fprintln(w, contentType)
		}
		return
	}
	data, ok := offer.Representations[requestedType]
	if !ok {
		available := strings.Join(clipboardTypes(offer), ", ")
		if available == "" {
			available = "none"
		}
		log.Printf("[clipboard] requested type unavailable type=%q available=%q", requestedType, available)
		http.Error(w, fmt.Sprintf("Requested clipboard type %q is unavailable (available: %s)", requestedType, available), http.StatusUnsupportedMediaType)
		return
	}
	w.Header().Set("Content-Type", requestedType)
	w.Write(data)
}

func (s *Server) handleClipboardRequestResponse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !terminalOriginAllowed(r) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/clipboard/requests/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}

	s.clipboardRequestMu.Lock()
	request := s.clipboardRequests[id]
	s.clipboardRequestMu.Unlock()
	if request == nil {
		http.Error(w, "Clipboard request not found", http.StatusNotFound)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxClipboardSize+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "Clipboard content too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Invalid clipboard response", http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()

	offer, err := readClipboardMultipart(r.MultipartForm)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	offer.CapturedAt = time.Now()
	log.Printf("[clipboard] response received id=%s offer=%s", clipboardLogID(id), clipboardOfferSummary(offer))

	s.clipboardRequestMu.Lock()
	request = s.clipboardRequests[id]
	if request != nil {
		delete(s.clipboardRequests, id)
	}
	s.clipboardRequestMu.Unlock()
	if request == nil {
		http.Error(w, "Clipboard request already fulfilled", http.StatusConflict)
		return
	}
	if request.ListTypes {
		offer.ReuseUntil = time.Now().Add(clipboardReuseWindow)
	}

	s.clipboardMu.Lock()
	s.clipboardOffer = cloneTypedClipboard(offer)
	text, hasText := offer.Representations["text/plain"]
	var version uint64
	if hasText {
		s.clipboard = string(text)
		s.clipboardVersion++
		version = s.clipboardVersion
	}
	s.clipboardMu.Unlock()
	if hasText {
		s.notifyClipboardSubscribers(clipboardEvent{Type: "clipboard", Version: version})
	}
	request.Result <- offer
	log.Printf("[clipboard] response accepted id=%s", clipboardLogID(id))
	w.WriteHeader(http.StatusNoContent)
}

func readClipboardMultipart(form *multipart.Form) (typedClipboardOffer, error) {
	offer := typedClipboardOffer{Representations: make(map[string][]byte)}
	files := form.File["data"]
	if len(files) == 0 || len(files) > maxClipboardTypes {
		return offer, fmt.Errorf("clipboard response must contain 1-%d representations", maxClipboardTypes)
	}
	total := 0
	for _, header := range files {
		contentType, err := normalizeClipboardMIME(header.Header.Get("Content-Type"))
		if err != nil {
			return offer, err
		}
		file, err := header.Open()
		if err != nil {
			return offer, fmt.Errorf("failed to open clipboard representation")
		}
		data, err := io.ReadAll(io.LimitReader(file, maxClipboardSize+1))
		file.Close()
		if err != nil {
			return offer, fmt.Errorf("failed to read clipboard representation")
		}
		total += len(data)
		if len(data) > maxClipboardSize || total > maxClipboardSize {
			return offer, fmt.Errorf("clipboard content too large")
		}
		offer.Representations[contentType] = data
	}
	return offer, nil
}

func (s *Server) setClipboardOfferFromHTTP(contentType string, data []byte) error {
	normalized, err := normalizeClipboardMIME(contentType)
	if err != nil {
		return err
	}
	s.setTypedClipboard(typedClipboardOffer{
		Representations: map[string][]byte{normalized: data},
		CapturedAt:      time.Now(),
	})
	return nil
}

func (s *Server) handleClipboardFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !terminalOriginAllowed(r) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxClipboardFilesSize+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "Clipboard files too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Invalid clipboard files", http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["files"]
	if len(files) == 0 || len(files) > maxClipboardFiles {
		http.Error(w, fmt.Sprintf("Expected 1-%d clipboard files", maxClipboardFiles), http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(s.uploadDir, 0755); err != nil {
		http.Error(w, "Failed to create upload directory", http.StatusInternalServerError)
		return
	}

	uploaded := make([]string, 0, len(files))
	success := false
	defer func() {
		if success {
			return
		}
		for _, path := range uploaded {
			os.Remove(path)
		}
	}()
	representations := make(map[string][]byte)
	total := 0
	for _, header := range files {
		file, err := header.Open()
		if err != nil {
			http.Error(w, "Failed to open clipboard file", http.StatusBadRequest)
			return
		}
		data, err := io.ReadAll(io.LimitReader(file, maxClipboardSize+1))
		file.Close()
		if err != nil {
			http.Error(w, "Failed to read clipboard file", http.StatusBadRequest)
			return
		}
		total += len(data)
		if len(data) > maxClipboardSize || total > maxClipboardFilesSize {
			http.Error(w, "Clipboard files too large", http.StatusRequestEntityTooLarge)
			return
		}

		contentType, err := normalizeClipboardMIME(header.Header.Get("Content-Type"))
		if err != nil || contentType == "application/octet-stream" || contentType == "text/plain" && header.Header.Get("Content-Type") == "" {
			contentType = strings.ToLower(strings.Split(http.DetectContentType(data), ";")[0])
		}
		if _, exists := representations[contentType]; !exists {
			representations[contentType] = append([]byte(nil), data...)
		}
		ext := clipboardExtension(contentType)
		id, err := randomClipboardRequestID()
		if err != nil {
			http.Error(w, "Failed to name clipboard file", http.StatusInternalServerError)
			return
		}
		name := fmt.Sprintf("pasted-%s-%s%s", time.Now().Format("20060102-150405"), id[:8], ext)
		path := filepath.Join(s.uploadDir, name)
		dest, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			http.Error(w, "Failed to create clipboard file", http.StatusInternalServerError)
			return
		}
		if _, err := dest.Write(data); err != nil {
			dest.Close()
			os.Remove(path)
			http.Error(w, "Failed to save clipboard file", http.StatusInternalServerError)
			return
		}
		if err := dest.Close(); err != nil {
			os.Remove(path)
			http.Error(w, "Failed to save clipboard file", http.StatusInternalServerError)
			return
		}
		uploaded = append(uploaded, path)
	}

	offer := typedClipboardOffer{Representations: representations, CapturedAt: time.Now()}
	s.setTypedClipboard(offer)
	log.Printf("[clipboard] pasted files stored count=%d total=%dB offer=%s", len(uploaded), total, clipboardOfferSummary(offer))
	success = true
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"uploaded": uploaded, "types": clipboardTypes(offer)})
}

func clipboardExtension(contentType string) string {
	switch contentType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	default:
		return ".bin"
	}
}

func encodeClipboardTypesJSON(offer typedClipboardOffer) ([]byte, error) {
	return json.Marshal(clipboardTypes(offer))
}
