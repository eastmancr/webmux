/* *
 * Webmux - a browser-based pane multiplexer
 * Copyright (C) 2026  Webmux contributors
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 */
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestClientAssetVersionIncludesPathAndContents(t *testing.T) {
	assets := make(map[string][]byte, len(clientAssetPaths))
	for _, assetPath := range clientAssetPaths {
		assets[assetPath] = []byte("same contents")
	}
	readAsset := func(assetPath string) ([]byte, error) { return assets[assetPath], nil }
	initial := clientAssetVersion(readAsset)
	assets["app.js"] = []byte("changed contents")
	if changed := clientAssetVersion(readAsset); changed == initial {
		t.Fatal("asset version did not change with app.js")
	}
}

func TestPaneEventsReadyIdentifiesServerRun(t *testing.T) {
	shutdownContext, shutdownCancel := context.WithCancel(context.Background())
	server := &Server{
		runID:           "run-test",
		assetVersion:    "assets-test",
		shutdown:        make(chan struct{}),
		shutdownContext: shutdownContext,
		shutdownCancel:  shutdownCancel,
		paneSubs:        make(map[chan paneEvent]struct{}),
	}
	httpServer := httptest.NewServer(server.shutdownAwareHandler(http.HandlerFunc(server.handlePaneEvents)))
	defer httpServer.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var event paneEvent
	if err := conn.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "ready" || event.ServerRunID != "run-test" || event.AssetVersion != "assets-test" {
		t.Fatalf("ready event = %+v, want server and asset versions", event)
	}

	server.beginShutdown()
	if err := conn.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "server-shutdown" {
		t.Fatalf("shutdown event = %+v, want server-shutdown", event)
	}
}

func TestShutdownAwareHandlerCancelsHTTPRequests(t *testing.T) {
	shutdownContext, shutdownCancel := context.WithCancel(context.Background())
	server := &Server{
		shutdown:        make(chan struct{}),
		shutdownContext: shutdownContext,
		shutdownCancel:  shutdownCancel,
	}
	started := make(chan struct{})
	done := make(chan struct{})
	handler := server.shutdownAwareHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(done)
	}))

	go handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/events", nil))
	<-started
	server.beginShutdown()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("HTTP request context was not cancelled during shutdown")
	}
}
