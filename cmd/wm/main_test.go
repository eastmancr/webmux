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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestParsePaneSelector(t *testing.T) {
	tests := []struct {
		ref       string
		wantKind  string
		wantValue string
		wantError bool
	}{
		{ref: ".", wantKind: "current"},
		{ref: "focused", wantKind: "focused"},
		{ref: "pane-12", wantKind: "id", wantValue: "pane-12"},
		{ref: "id:pane-12", wantKind: "id", wantValue: "pane-12"},
		{ref: "12", wantKind: "position", wantValue: "12"},
		{ref: "pos:12", wantKind: "position", wantValue: "12"},
		{ref: "name:.", wantKind: "name", wantValue: "."},
		{ref: "current", wantKind: "name", wantValue: "current"},
		{ref: "", wantError: true},
		{ref: "01", wantError: true},
		{ref: "0", wantError: true},
		{ref: "-2", wantError: true},
		{ref: "pane-0", wantError: true},
		{ref: "pane-01", wantError: true},
		{ref: "pane-x", wantError: true},
		{ref: "id:12", wantError: true},
		{ref: "pos:01", wantError: true},
		{ref: "name:", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			got, err := parsePaneSelector(tt.ref)
			if (err != nil) != tt.wantError {
				t.Fatalf("parsePaneSelector(%q) error = %v", tt.ref, err)
			}
			if err == nil && (got.kind != tt.wantKind || got.value != tt.wantValue) {
				t.Fatalf("parsePaneSelector(%q) = %#v, want kind %q value %q", tt.ref, got, tt.wantKind, tt.wantValue)
			}
		})
	}
}

func TestResolvePaneSelector(t *testing.T) {
	panes := []paneView{
		{ID: "pane-9", Name: "same", DisplayName: "ignored", Position: 2},
		{ID: "pane-3", Name: "same", Position: 1, Focused: true},
		{ID: "pane-7", Name: ".", Position: 3},
	}

	tests := []struct {
		name      string
		selector  paneSelector
		session   string
		want      string
		wantError string
	}{
		{name: "position", selector: paneSelector{kind: "position", value: "1"}, want: "pane-3"},
		{name: "focused", selector: paneSelector{kind: "focused"}, want: "pane-3"},
		{name: "literal dot name", selector: paneSelector{kind: "name", value: "."}, want: "pane-7"},
		{name: "current", selector: paneSelector{kind: "current"}, session: "pane-9", want: "pane-9"},
		{name: "invalid current", selector: paneSelector{kind: "current"}, session: "9", wantError: "WEBMUX_SESSION"},
		{name: "missing current", selector: paneSelector{kind: "current"}, session: "pane-99", wantError: "no pane matches"},
		{name: "display name is not raw name", selector: paneSelector{kind: "name", value: "ignored"}, wantError: "no pane matches"},
		{name: "ambiguous raw name", selector: paneSelector{kind: "name", value: "same"}, wantError: `position 2, pane-9, title "ignored", position 1, pane-3`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolvePaneSelector(tt.selector, panes, tt.session)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("error = %v, want substring %q", err, tt.wantError)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("resolvePaneSelector() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestCmdCurrentValidatesSessionAndExistence(t *testing.T) {
	server := paneServer(t, `[{"id":"pane-4","position":1}]`, nil)
	t.Setenv("WEBMUX_SESSION", "pane-4")
	output := captureStdout(t, func() error { return cmdCurrent(server.Listener.Addr().String(), nil) })
	if output != "pane-4\n" {
		t.Fatalf("output = %q, want pane-4", output)
	}

	t.Setenv("WEBMUX_SESSION", "4")
	if err := cmdCurrent(server.Listener.Addr().String(), nil); err == nil || !strings.Contains(err.Error(), "WEBMUX_SESSION") {
		t.Fatalf("invalid session error = %v", err)
	}
	t.Setenv("WEBMUX_SESSION", "pane-8")
	if err := cmdCurrent(server.Listener.Addr().String(), nil); err == nil || !strings.Contains(err.Error(), "no pane matches") {
		t.Fatalf("missing session error = %v", err)
	}
	if err := cmdCurrent(server.Listener.Addr().String(), []string{"extra"}); err == nil {
		t.Fatal("cmdCurrent accepted an argument")
	}
}

func TestCmdListJSONIncludesServerFieldsAndCurrent(t *testing.T) {
	response := `[{"id":"pane-2","type":"terminal","backendId":"b1","backendScope":"pane","backendLifetime":"managed","name":"raw","port":1234,"createdAt":"2026-07-30T10:00:00Z","currentActivity":"vim","position":4,"displayName":"Editor","focused":true}]`
	server := paneServer(t, response, nil)
	t.Setenv("WEBMUX_SESSION", "pane-2")
	output := captureStdout(t, func() error { return cmdList(server.Listener.Addr().String(), []string{"--json"}) })
	var records []map[string]any
	if err := json.Unmarshal([]byte(output), &records); err != nil {
		t.Fatalf("invalid JSON %q: %v", output, err)
	}
	if len(records) != 1 || records[0]["displayName"] != "Editor" || records[0]["current"] != true || records[0]["focused"] != true {
		t.Fatalf("records = %#v", records)
	}
	if _, ok := records[0]["backendId"]; !ok {
		t.Fatalf("server fields missing from %#v", records[0])
	}
}

func TestCmdListEmptyJSON(t *testing.T) {
	server := paneServer(t, `[]`, nil)
	output := captureStdout(t, func() error { return cmdList(server.Listener.Addr().String(), []string{"--json"}) })
	if output != "[]\n" {
		t.Fatalf("output = %q, want []", output)
	}
}

func TestCmdCloseResolvesPosition(t *testing.T) {
	var method, path string
	server := paneServer(t, `[{"id":"pane-17","position":2}]`, func(r *http.Request, body []byte) {
		method, path = r.Method, r.URL.Path
	})
	if err := cmdClose(server.Listener.Addr().String(), []string{"2"}); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete || path != "/api/panes/pane-17" {
		t.Fatalf("request = %s %s", method, path)
	}
}

func TestCmdRenameReset(t *testing.T) {
	var method, path string
	var payload map[string]string
	server := paneServer(t, `[{"id":"pane-6","name":"work","position":1}]`, func(r *http.Request, body []byte) {
		method, path = r.Method, r.URL.Path
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
	})
	if err := cmdRename(server.Listener.Addr().String(), []string{"work", "--reset"}); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPatch || path != "/api/panes/pane-6" || payload["name"] != "" {
		t.Fatalf("request = %s %s payload %#v", method, path, payload)
	}
}

func paneServer(t *testing.T, panes string, mutation func(*http.Request, []byte)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/panes" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, panes)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if mutation != nil {
			mutation(r, body)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	err = fn()
	writer.Close()
	os.Stdout = original
	if err != nil {
		reader.Close()
		t.Fatal(err)
	}
	output, readErr := io.ReadAll(reader)
	reader.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(output)
}

func TestCmdCopyPreservesTypedBinaryInput(t *testing.T) {
	payload := []byte{0, 1, 2, 0x80, 0xff}
	var received []byte
	var receivedType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedType = r.Header.Get("Content-Type")
		received, _ = io.ReadAll(r.Body)
	}))
	defer server.Close()
	t.Setenv("WEBMUX_HOST", server.Listener.Addr().String())

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	originalStdin := os.Stdin
	os.Stdin = reader
	defer func() {
		os.Stdin = originalStdin
		reader.Close()
	}()

	if err := cmdCopy([]string{"--type", "image/png"}); err != nil {
		t.Fatal(err)
	}
	if receivedType != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", receivedType)
	}
	if !bytes.Equal(received, payload) {
		t.Fatalf("payload = %v, want %v", received, payload)
	}
}

func TestCmdPasteRequestsExactType(t *testing.T) {
	payload := []byte{0, 1, 0xff}
	var path, query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, query = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "image/png")
		w.Write(payload)
	}))
	defer server.Close()
	t.Setenv("WEBMUX_HOST", server.Listener.Addr().String())

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStdout := os.Stdout
	os.Stdout = writer
	defer func() {
		os.Stdout = originalStdout
		writer.Close()
	}()
	if err := cmdPaste([]string{"--request", "--type", "image/png"}); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	os.Stdout = originalStdout
	defer reader.Close()
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/api/clipboard/request" || query != "type=image%2Fpng" {
		t.Fatalf("request = %s?%s", path, query)
	}
	if !bytes.Equal(output, payload) {
		t.Fatalf("output = %v, want %v", output, payload)
	}
}
