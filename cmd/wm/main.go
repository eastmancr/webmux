/* *
 * wm - webmux CLI helper
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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"webmux/internal/shell"
)

const defaultPort = "8080"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	host := webmuxHost()

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "info":
		err = cmdInfo(host)
	case "ls", "list":
		err = cmdList(host)
	case "new":
		err = cmdNew(host, args)
	case "close":
		err = cmdClose(host, args)
	case "rename":
		err = cmdRename(host, args)
	case "upload":
		err = cmdUpload(host, args)
	case "scratch":
		err = cmdScratch(host, args)
	case "mark":
		err = cmdMark(host, args)
	case "init":
		err = cmdInit()
	case "copy", "c":
		err = cmdCopy(args)
	case "paste", "p", "v":
		err = cmdPaste(args)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`wm - webmux CLI helper

Usage: wm <command> [arguments]

Commands:
  info               Show server info (upload dir, work dir)
  ls, list           List all panes
  new [--terminal|--opencode] [name]
                     Create a pane (defaults to terminal)
  close <id>         Close a pane
  rename <id> <name> Rename a pane
  upload <file>...   Upload files to the server
  scratch            Get current scratch pad text
  scratch <text>     Send text to scratch pad
  scratch -          Read from stdin and send to scratch pad
  scratch clear      Clear and close the scratch pad
  mark               List marked files
  mark <file>...     Mark files for download
  mark unmark <file> Unmark a file
  mark clear         Clear all marked files
  copy, c [-t type] [text]
                     Copy typed data (reads stdin if no data is provided)
  paste, p, v [--request] [-t type|--list-types]
                     Read clipboard data or request it from the focused browser
  init               Output shell code that defines the wm wrapper function

Environment:
  WEBMUX_PORT        Server port (set automatically in webmux terminals)
  WEBMUX_SESSION     Current pane ID (set automatically in webmux terminals)
  WEBMUX_HOST        Full server address (overrides WEBMUX_PORT if set)

In webmux terminals, use wm to run commands (e.g., wm ls, wm scratch hello)

Clipboard:
  The copy/paste commands use the webmux HTTP API to sync with the browser.
  Wrapper scripts for wl-copy, wl-paste, xclip, and xsel are provided in PATH,
  so programs like neovim work automatically without configuration.

  Copy: Sends text to all connected browser tabs (updates system clipboard)
  Paste: Returns stored data; --request asks the focused browser for fresh data

  To paste from your system clipboard into the terminal, use Ctrl+Shift+V.

`)
}

// SECTION: API

// API helpers

func webmuxHost() string {
	if host := os.Getenv("WEBMUX_HOST"); host != "" {
		return host
	}
	port := os.Getenv("WEBMUX_PORT")
	if port == "" {
		port = defaultPort
	}
	return "localhost:" + port
}

func apiGet(host, path string) ([]byte, error) {
	resp, err := http.Get(fmt.Sprintf("http://%s%s", host, path))
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("server error (%d): %s", resp.StatusCode, string(body))
	}

	return body, nil
}

func apiPost(host, path string, data any) ([]byte, error) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to encode request: %w", err)
	}

	resp, err := http.Post(
		fmt.Sprintf("http://%s%s", host, path),
		"application/json",
		bytes.NewReader(jsonData),
	)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("server error (%d): %s", resp.StatusCode, string(body))
	}

	return body, nil
}

func apiDelete(host, path string) error {
	req, err := http.NewRequest(http.MethodDelete, fmt.Sprintf("http://%s%s", host, path), nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server error (%d): %s", resp.StatusCode, string(body))
	}

	return nil
}

func apiPatch(host, path string, data any) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to encode request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPatch, fmt.Sprintf("http://%s%s", host, path), bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server error (%d): %s", resp.StatusCode, string(body))
	}

	return nil
}

// SECTION: CLI

// Commands

func cmdInfo(host string) error {
	body, err := apiGet(host, "/api/info")
	if err != nil {
		return err
	}

	var info struct {
		WorkDir    string `json:"workDir"`
		UploadDir  string `json:"uploadDir"`
		Shell      string `json:"shell"`
		Port       string `json:"port"`
		InstanceID string `json:"instanceID"`
		PaneCount  int    `json:"paneCount"`
		TmuxSocket string `json:"tmuxSocket"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	fmt.Printf("Server:       http://localhost:%s\n", info.Port)
	fmt.Printf("Instance:     %s\n", info.InstanceID)
	fmt.Printf("Panes:        %d\n", info.PaneCount)
	fmt.Printf("Shell:        %s\n", info.Shell)
	fmt.Printf("Work dir:     %s\n", info.WorkDir)
	fmt.Printf("Upload dir:   %s\n", info.UploadDir)
	fmt.Printf("Tmux socket:  %s\n", info.TmuxSocket)
	return nil
}

func cmdList(host string) error {
	body, err := apiGet(host, "/api/panes")
	if err != nil {
		return err
	}

	var panes []struct {
		ID              string `json:"id"`
		Type            string `json:"type"`
		Name            string `json:"name"`
		CurrentActivity string `json:"currentActivity"`
	}
	if err := json.Unmarshal(body, &panes); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if len(panes) == 0 {
		fmt.Println("No active panes")
		return nil
	}

	if stdoutIsTerminal() {
		fmt.Printf("%-12s %-10s %-20s %s\n", "ID", "TYPE", "NAME", "ACTIVITY")
	}
	for _, p := range panes {
		activity := p.CurrentActivity
		if activity == "" {
			activity = "-"
		}
		fmt.Printf("%-12s %-10s %-20s %s\n", p.ID, p.Type, p.Name, activity)
	}
	return nil
}

func stdoutIsTerminal() bool {
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func cmdNew(host string, args []string) error {
	paneType := "terminal"
	typeFlagSeen := false
	nameParts := make([]string, 0, len(args))
	for _, arg := range args {
		switch arg {
		case "--terminal":
			if len(nameParts) > 0 {
				nameParts = append(nameParts, arg)
				continue
			}
			if typeFlagSeen {
				return fmt.Errorf("only one pane type flag may be provided before the name")
			}
			typeFlagSeen = true
			paneType = "terminal"
		case "--opencode":
			if len(nameParts) > 0 {
				nameParts = append(nameParts, arg)
				continue
			}
			if typeFlagSeen {
				return fmt.Errorf("only one pane type flag may be provided before the name")
			}
			typeFlagSeen = true
			paneType = "opencode"
		default:
			nameParts = append(nameParts, arg)
		}
	}
	name := strings.Join(nameParts, " ")

	body, err := apiPost(host, "/api/panes", map[string]string{"type": paneType, "name": name})
	if err != nil {
		return err
	}

	var pane struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &pane); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	fmt.Printf("Created %s pane: %s (%s)\n", pane.Type, pane.Name, pane.ID)
	return nil
}

func normalizePaneID(id string) string {
	if _, err := strconv.Atoi(id); err == nil {
		return "pane-" + id
	}
	return id
}

func cmdClose(host string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: wm close <pane-id>")
	}

	paneID := normalizePaneID(args[0])
	if err := apiDelete(host, "/api/panes/"+paneID); err != nil {
		return err
	}

	fmt.Printf("Closed pane: %s\n", paneID)
	return nil
}

func cmdRename(host string, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: wm rename <pane-id> <new-name>")
	}

	paneID := normalizePaneID(args[0])
	newName := strings.Join(args[1:], " ")

	if err := apiPatch(host, "/api/panes/"+paneID, map[string]string{"name": newName}); err != nil {
		return err
	}

	fmt.Printf("Renamed pane %s to: %s\n", paneID, newName)
	return nil
}

func cmdUpload(host string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: wm upload <file>...")
	}

	// Get server info for upload directory
	infoBody, err := apiGet(host, "/api/info")
	if err != nil {
		return err
	}
	var info struct {
		UploadDir string `json:"uploadDir"`
	}
	json.Unmarshal(infoBody, &info)

	for _, file := range args {
		absPath, err := filepath.Abs(file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Skipping %s: %v\n", file, err)
			continue
		}

		f, err := os.Open(absPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Skipping %s: %v\n", file, err)
			continue
		}

		// Create multipart form
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)

		part, err := writer.CreateFormFile("files", filepath.Base(absPath))
		if err != nil {
			f.Close()
			fmt.Fprintf(os.Stderr, "Skipping %s: %v\n", file, err)
			continue
		}

		if _, err := io.Copy(part, f); err != nil {
			f.Close()
			fmt.Fprintf(os.Stderr, "Skipping %s: %v\n", file, err)
			continue
		}
		f.Close()
		if err := writer.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Skipping %s: %v\n", file, err)
			continue
		}

		req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/api/upload", host), body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Skipping %s: %v\n", file, err)
			continue
		}
		req.Header.Set("Content-Type", writer.FormDataContentType())

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Skipping %s: %v\n", file, err)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode >= 400 {
			fmt.Fprintf(os.Stderr, "Failed to upload %s: server returned %d\n", file, resp.StatusCode)
			continue
		}

		fmt.Printf("Uploaded: %s -> %s/%s\n", file, info.UploadDir, filepath.Base(file))
	}

	return nil
}

func cmdScratch(host string, args []string) error {
	// No args - show current scratch pad content
	if len(args) == 0 {
		body, err := apiGet(host, "/api/scratch")
		if err != nil {
			return err
		}
		var resp struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return fmt.Errorf("failed to parse response: %w", err)
		}
		fmt.Print(resp.Text)
		return nil
	}

	subcmd := args[0]

	switch subcmd {
	case "get":
		// Explicit get - same as no args
		body, err := apiGet(host, "/api/scratch")
		if err != nil {
			return err
		}
		var resp struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return fmt.Errorf("failed to parse response: %w", err)
		}
		fmt.Print(resp.Text)
		return nil

	case "clear":
		// Clear scratch pad
		if err := apiDelete(host, "/api/scratch"); err != nil {
			return err
		}
		fmt.Println("Cleared scratch pad")
		return nil

	default:
		// Treat all args as text to send
		text := strings.Join(args, " ")

		// Check if reading from stdin
		if text == "-" {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("failed to read stdin: %w", err)
			}
			text = string(data)
		}

		// Empty string = toggle visibility without clearing
		if text == "" {
			_, err := apiPost(host, "/api/scratch", map[string]string{"toggle": "true"})
			if err != nil {
				return err
			}
			return nil
		}

		_, err := apiPost(host, "/api/scratch", map[string]string{"text": text})
		if err != nil {
			return err
		}
		fmt.Println("Sent to scratch pad")
		return nil
	}
}

func cmdMark(host string, args []string) error {
	// No args - list marked files
	if len(args) == 0 {
		body, err := apiGet(host, "/api/marked")
		if err != nil {
			return err
		}
		var resp struct {
			Files []struct {
				Path  string `json:"path"`
				Name  string `json:"name"`
				Size  int64  `json:"size"`
				IsDir bool   `json:"isDir"`
			} `json:"files"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return fmt.Errorf("failed to parse response: %w", err)
		}

		if len(resp.Files) == 0 {
			fmt.Println("No files marked")
			return nil
		}

		for _, f := range resp.Files {
			if f.IsDir {
				fmt.Printf("%s/\n", f.Path)
			} else {
				fmt.Printf("%s\t%d bytes\n", f.Path, f.Size)
			}
		}
		return nil
	}

	// Handle subcommands
	switch args[0] {
	case "clear":
		if err := apiDelete(host, "/api/marked"); err != nil {
			return err
		}
		fmt.Println("Cleared all marked files")
		return nil

	case "unmark":
		if len(args) < 2 {
			return fmt.Errorf("usage: wm mark unmark <file>...")
		}
		for _, file := range args[1:] {
			absPath, err := filepath.Abs(file)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Skipping %s: %v\n", file, err)
				continue
			}
			if err := apiDelete(host, "/api/marked?path="+url.QueryEscape(absPath)); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to unmark %s: %v\n", file, err)
				continue
			}
			fmt.Printf("Unmarked: %s\n", file)
		}
		return nil

	default:
		// Mark files
		for _, file := range args {
			absPath, err := filepath.Abs(file)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Skipping %s: %v\n", file, err)
				continue
			}

			_, err = apiPost(host, "/api/marked", map[string]string{"path": absPath})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to mark %s: %v\n", file, err)
				continue
			}
			fmt.Printf("Marked: %s\n", file)
		}
		return nil
	}
}

// cmdInit outputs shell code to set up the wm wrapper function
// This is automatically injected by webmux; users don't need to call this manually
func cmdInit() error {
	// Get the path to the wm binary from the environment
	wmBin := os.Getenv("_wm_bin")
	if wmBin == "" {
		// Fall back to finding ourselves
		wmBin, _ = os.Executable()
	}

	fmt.Print(shell.InitScript(wmBin, ""))
	return nil
}

// SECTION: CLIPBOARD

// cmdCopy sends text to the server-side clipboard via HTTP API
// The server broadcasts to connected browsers to update their clipboards
// Usage: wm copy <text>  OR  echo "text" | wm copy
func cmdCopy(args []string) error {
	contentType := "text/plain"
	for len(args) > 0 {
		switch {
		case args[0] == "-t" || args[0] == "--type":
			if len(args) < 2 {
				return fmt.Errorf("%s requires a MIME type", args[0])
			}
			contentType = args[1]
			args = args[2:]
		case strings.HasPrefix(args[0], "--type="):
			contentType = strings.TrimPrefix(args[0], "--type=")
			args = args[1:]
		case args[0] == "--":
			args = args[1:]
			goto parsed
		case strings.HasPrefix(args[0], "-"):
			return fmt.Errorf("unknown copy option: %s", args[0])
		default:
			goto parsed
		}
	}

parsed:
	var data []byte

	if len(args) > 0 {
		data = []byte(strings.Join(args, " "))
	} else {
		var err error
		data, err = io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("failed to read stdin: %w", err)
		}
	}

	host := webmuxHost()

	// POST to clipboard API
	resp, err := http.Post(
		fmt.Sprintf("http://%s/api/clipboard", host),
		contentType,
		bytes.NewReader(data),
	)
	if err != nil {
		return fmt.Errorf("failed to set clipboard: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to set clipboard: %s", string(body))
	}

	return nil
}

// cmdPaste reads from the server-side clipboard via HTTP API
// Outputs the clipboard contents to stdout
func cmdPaste(args []string) error {
	requestedType := ""
	listTypes := false
	requestBrowser := false
	for len(args) > 0 {
		switch {
		case args[0] == "-t" || args[0] == "--type":
			if len(args) < 2 {
				return fmt.Errorf("%s requires a MIME type", args[0])
			}
			requestedType = args[1]
			args = args[2:]
		case strings.HasPrefix(args[0], "--type="):
			requestedType = strings.TrimPrefix(args[0], "--type=")
			args = args[1:]
		case args[0] == "-l" || args[0] == "--list-types":
			listTypes = true
			args = args[1:]
		case args[0] == "--request":
			requestBrowser = true
			args = args[1:]
		default:
			return fmt.Errorf("unknown paste option: %s", args[0])
		}
	}
	if listTypes && requestedType != "" {
		return fmt.Errorf("--list-types and --type cannot be combined")
	}

	host := webmuxHost()
	endpoint := fmt.Sprintf("http://%s/api/clipboard", host)
	if requestBrowser {
		endpoint += "/request"
	}
	query := url.Values{}
	if listTypes {
		query.Set("list-types", "1")
	} else if requestedType != "" {
		query.Set("type", requestedType)
	} else if requestBrowser {
		query.Set("type", "text/plain")
	}
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		return fmt.Errorf("failed to get clipboard: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to get clipboard: %s", string(body))
	}

	_, err = io.Copy(os.Stdout, resp.Body)
	return err
}
