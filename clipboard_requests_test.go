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
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"testing"
	"time"
)

func newClipboardTestServer() *Server {
	return &Server{
		clipboardRequests: make(map[string]*clipboardReadRequest),
		clipboardSubs:     make(map[chan clipboardEvent]struct{}),
	}
}

func TestTypedClipboardLegacyAPI(t *testing.T) {
	server := newClipboardTestServer()
	payload := []byte{0x89, 'P', 'N', 'G', 0, 0xff}

	post := httptest.NewRequest(http.MethodPost, "/api/clipboard", bytes.NewReader(payload))
	post.Header.Set("Content-Type", "image/png")
	postResult := httptest.NewRecorder()
	server.handleClipboard(postResult, post)
	if postResult.Code != http.StatusOK {
		t.Fatalf("POST status = %d, body = %q", postResult.Code, postResult.Body.String())
	}

	get := httptest.NewRequest(http.MethodGet, "/api/clipboard?type=image/png", nil)
	getResult := httptest.NewRecorder()
	server.handleClipboard(getResult, get)
	if getResult.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %q", getResult.Code, getResult.Body.String())
	}
	if !bytes.Equal(getResult.Body.Bytes(), payload) {
		t.Fatalf("GET payload = %v, want %v", getResult.Body.Bytes(), payload)
	}
	if got := getResult.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", got)
	}

	list := httptest.NewRequest(http.MethodGet, "/api/clipboard?list-types=1", nil)
	listResult := httptest.NewRecorder()
	server.handleClipboard(listResult, list)
	if got := listResult.Body.String(); got != "image/png\n" {
		t.Fatalf("types = %q, want %q", got, "image/png\n")
	}
}

func TestClipboardRequestRoundTrip(t *testing.T) {
	server := newClipboardTestServer()
	events := make(chan clipboardEvent, 2)
	server.clipboardSubs[events] = struct{}{}

	requestResult := httptest.NewRecorder()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		request := httptest.NewRequest(http.MethodGet, "/api/clipboard/request?type=image/png", nil)
		server.handleClipboardRequest(requestResult, request)
	}()

	var event clipboardEvent
	select {
	case event = <-events:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for clipboard request event")
	}
	if event.Type != "clipboard-request" || event.RequestID == "" || event.MIME != "image/png" {
		t.Fatalf("unexpected event: %+v", event)
	}

	payload := []byte{0, 1, 2, 3, 0xff}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", `form-data; name="data"; filename="image.png"`)
	partHeader.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(partHeader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRequest(http.MethodPost, "/api/clipboard/requests/"+event.RequestID, &body)
	response.Header.Set("Content-Type", writer.FormDataContentType())
	responseResult := httptest.NewRecorder()
	server.handleClipboardRequestResponse(responseResult, response)
	if responseResult.Code != http.StatusNoContent {
		t.Fatalf("response status = %d, body = %q", responseResult.Code, responseResult.Body.String())
	}

	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for clipboard request result")
	}
	if requestResult.Code != http.StatusOK {
		t.Fatalf("request status = %d, body = %q", requestResult.Code, requestResult.Body.String())
	}
	if !bytes.Equal(requestResult.Body.Bytes(), payload) {
		t.Fatalf("request payload = %v, want %v", requestResult.Body.Bytes(), payload)
	}
}

func TestClipboardRequestReusesListContinuation(t *testing.T) {
	server := newClipboardTestServer()
	server.setTypedClipboard(typedClipboardOffer{
		Representations: map[string][]byte{"text/plain": []byte("fresh")},
		CapturedAt:      time.Now(),
		ReuseUntil:      time.Now().Add(clipboardReuseWindow),
	})

	request := httptest.NewRequest(http.MethodGet, "/api/clipboard/request?type=text/plain", nil)
	result := httptest.NewRecorder()
	server.handleClipboardRequest(result, request)
	if result.Code != http.StatusOK || result.Body.String() != "fresh" {
		t.Fatalf("status = %d, body = %q", result.Code, result.Body.String())
	}
}

func TestClipboardRequestRejectsConcurrentRequest(t *testing.T) {
	server := newClipboardTestServer()
	server.setTypedClipboard(typedClipboardOffer{
		Representations: map[string][]byte{"text/plain": []byte("previous")},
		CapturedAt:      time.Now(),
	})
	server.clipboardRequests["pending"] = &clipboardReadRequest{ID: "pending"}

	request := httptest.NewRequest(http.MethodGet, "/api/clipboard/request?type=text/plain", nil)
	result := httptest.NewRecorder()
	server.handleClipboardRequest(result, request)
	if result.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", result.Code, http.StatusTooManyRequests)
	}
}

func TestClipboardListRequestNeverReusesContinuation(t *testing.T) {
	server := newClipboardTestServer()
	server.setTypedClipboard(typedClipboardOffer{
		Representations: map[string][]byte{"text/plain": []byte("previous")},
		CapturedAt:      time.Now(),
		ReuseUntil:      time.Now().Add(clipboardReuseWindow),
	})
	server.clipboardRequests["pending"] = &clipboardReadRequest{ID: "pending"}

	request := httptest.NewRequest(http.MethodGet, "/api/clipboard/request?list-types=1", nil)
	result := httptest.NewRecorder()
	server.handleClipboardRequest(result, request)
	if result.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", result.Code, http.StatusTooManyRequests)
	}
}

func TestClipboardTypeMismatchReportsAvailableTypes(t *testing.T) {
	server := newClipboardTestServer()
	server.setTypedClipboard(typedClipboardOffer{
		Representations: map[string][]byte{"image/png": []byte("png")},
		CapturedAt:      time.Now(),
	})

	request := httptest.NewRequest(http.MethodGet, "/api/clipboard?type=text/plain", nil)
	result := httptest.NewRecorder()
	server.handleClipboard(result, request)
	if result.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want %d", result.Code, http.StatusUnsupportedMediaType)
	}
	if body := result.Body.String(); !strings.Contains(body, `type "text/plain"`) || !strings.Contains(body, "image/png") {
		t.Fatalf("mismatch response = %q", body)
	}
}

func TestClipboardRequestRejectsCrossOriginBrowser(t *testing.T) {
	server := newClipboardTestServer()
	request := httptest.NewRequest(http.MethodGet, "http://localhost/api/clipboard/request?type=text/plain", nil)
	request.Header.Set("Origin", "https://example.com")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	result := httptest.NewRecorder()
	server.handleClipboardRequest(result, request)
	if result.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", result.Code, http.StatusForbidden)
	}
}

func TestClipboardMultipartRequiresRepresentations(t *testing.T) {
	form := &multipart.Form{File: map[string][]*multipart.FileHeader{}}
	if _, err := readClipboardMultipart(form); err == nil || !strings.Contains(err.Error(), "representations") {
		t.Fatalf("empty form error = %v", err)
	}
}

func TestClipboardFileUploadGeneratesPrivateTypedFile(t *testing.T) {
	server := newClipboardTestServer()
	server.uploadDir = t.TempDir()
	payload := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("files", "clipboard-data")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/clipboard/files", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	result := httptest.NewRecorder()
	server.handleClipboardFiles(result, request)
	if result.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", result.Code, result.Body.String())
	}

	offer := server.currentTypedClipboard()
	if !bytes.Equal(offer.Representations["image/png"], payload) {
		t.Fatalf("typed image payload = %v, want %v", offer.Representations["image/png"], payload)
	}
	entries, err := os.ReadDir(server.uploadDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".png") {
		t.Fatalf("uploaded entries = %v", entries)
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("uploaded mode = %o, want 600", info.Mode().Perm())
	}
}
