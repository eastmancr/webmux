/* *
 * wm - webmux CLI helper
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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const defaultPort = "8080"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// Determine host from environment
	host := os.Getenv("WEBMUX_HOST")
	if host == "" {
		port := os.Getenv("WEBMUX_PORT")
		if port == "" {
			port = defaultPort
		}
		host = "localhost:" + port
	}

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
  ls, list           List all sessions
  new [name]         Create a new session
  close <id>         Close a session
  rename <id> <name> Rename a session
  upload <file>...   Upload files to the server
  scratch            Get current scratch pad text
  scratch <text>     Send text to scratch pad
  scratch -          Read from stdin and send to scratch pad
  scratch clear      Clear and close the scratch pad
  mark               List marked files
  mark <file>...     Mark files for download
  mark unmark <file> Unmark a file
  mark clear         Clear all marked files

Environment:
  WEBMUX_PORT        Server port (default: 8080, set automatically)
  WEBMUX_HOST        Full server address (overrides WEBMUX_PORT if set)

In webmux terminals, use $wm to run commands (e.g., $wm ls, $wm scratch hello)

`)
}

// API helpers

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

// Commands

func cmdInfo(host string) error {
	body, err := apiGet(host, "/api/info")
	if err != nil {
		return err
	}

	var info struct {
		WorkDir      string `json:"workDir"`
		UploadDir    string `json:"uploadDir"`
		Shell        string `json:"shell"`
		Port         string `json:"port"`
		SessionCount int    `json:"sessionCount"`
		TmuxSocket   string `json:"tmuxSocket"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	fmt.Printf("Server:       http://localhost:%s\n", info.Port)
	fmt.Printf("Sessions:     %d\n", info.SessionCount)
	fmt.Printf("Shell:        %s\n", info.Shell)
	fmt.Printf("Work dir:     %s\n", info.WorkDir)
	fmt.Printf("Upload dir:   %s\n", info.UploadDir)
	fmt.Printf("Tmux socket:  %s\n", info.TmuxSocket)
	return nil
}

func cmdList(host string) error {
	body, err := apiGet(host, "/api/sessions")
	if err != nil {
		return err
	}

	var sessions []struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		CurrentProcess string `json:"currentProcess"`
	}
	if err := json.Unmarshal(body, &sessions); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if len(sessions) == 0 {
		fmt.Println("No active sessions")
		return nil
	}

	for _, s := range sessions {
		proc := s.CurrentProcess
		if proc == "" {
			proc = "-"
		}
		fmt.Printf("%s\t%s\t(%s)\n", s.ID, s.Name, proc)
	}
	return nil
}

func cmdNew(host string, args []string) error {
	name := ""
	if len(args) > 0 {
		name = args[0]
	}

	body, err := apiPost(host, "/api/sessions", map[string]string{"name": name})
	if err != nil {
		return err
	}

	var session struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &session); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	fmt.Printf("Created session: %s (%s)\n", session.Name, session.ID)
	return nil
}

func cmdClose(host string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: wm close <session-id>")
	}

	sessionID := args[0]
	if err := apiDelete(host, "/api/sessions/"+sessionID); err != nil {
		return err
	}

	fmt.Printf("Closed session: %s\n", sessionID)
	return nil
}

func cmdRename(host string, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: wm rename <session-id> <new-name>")
	}

	sessionID := args[0]
	newName := strings.Join(args[1:], " ")

	if err := apiPatch(host, "/api/sessions/"+sessionID, map[string]string{"name": newName}); err != nil {
		return err
	}

	fmt.Printf("Renamed session %s to: %s\n", sessionID, newName)
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
		writer := newMultipartWriter(body)

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
		writer.Close()

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

// Multipart helper
type multipartWriter struct {
	*bytes.Buffer
	boundary string
}

func newMultipartWriter(buf *bytes.Buffer) *multipartWriter {
	return &multipartWriter{
		Buffer:   buf,
		boundary: "----WebmuxFormBoundary",
	}
}

func (w *multipartWriter) CreateFormFile(fieldname, filename string) (io.Writer, error) {
	fmt.Fprintf(w.Buffer, "--%s\r\n", w.boundary)
	fmt.Fprintf(w.Buffer, "Content-Disposition: form-data; name=\"%s\"; filename=\"%s\"\r\n", fieldname, filename)
	fmt.Fprintf(w.Buffer, "Content-Type: application/octet-stream\r\n\r\n")
	return w.Buffer, nil
}

func (w *multipartWriter) Close() error {
	fmt.Fprintf(w.Buffer, "\r\n--%s--\r\n", w.boundary)
	return nil
}

func (w *multipartWriter) FormDataContentType() string {
	return "multipart/form-data; boundary=" + w.boundary
}
