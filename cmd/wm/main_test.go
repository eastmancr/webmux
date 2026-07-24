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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

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
