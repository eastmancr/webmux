package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFilterActivityCommandSuppressesOnlyFirstTmux(t *testing.T) {
	state := tmuxActivityUnseen
	tests := []struct {
		proc      string
		want      string
		wantState tmuxActivityState
	}{
		{proc: "zsh", want: "zsh", wantState: tmuxActivityUnseen},
		{proc: "tmux", want: "", wantState: tmuxActivitySuppressing},
		{proc: "tmux", want: "", wantState: tmuxActivitySuppressing},
		{proc: "zsh", want: "zsh", wantState: tmuxActivityVisible},
		{proc: "tmux", want: "tmux", wantState: tmuxActivityVisible},
	}

	for i, tt := range tests {
		if got := filterActivityCommand(tt.proc, &state); got != tt.want {
			t.Fatalf("step %d: command = %q, want %q", i, got, tt.want)
		}
		if state != tt.wantState {
			t.Fatalf("step %d: state = %d, want %d", i, state, tt.wantState)
		}
	}
}

func TestForegroundActivityDisplay(t *testing.T) {
	tests := []struct {
		name  string
		proc  string
		path  string
		shell string
		home  string
		want  string
	}{
		{name: "idle shell", proc: "zsh", path: "/home/user/projects/webmux", shell: "/bin/zsh", home: "/home/user", want: "/webmux"},
		{name: "home directory", proc: "zsh", path: "/home/user", shell: "/bin/zsh", home: "/home/user", want: "~"},
		{name: "root directory", proc: "bash", path: "/", shell: "/bin/bash", home: "/home/user", want: "/"},
		{name: "running command", proc: "vim", path: "/home/user/projects/webmux", shell: "/bin/zsh", home: "/home/user", want: "/webmux · vim"},
		{name: "running command at home", proc: "make", path: "/home/user", shell: "/bin/zsh", home: "/home/user", want: "~ · make"},
		{name: "missing path", proc: "zsh", shell: "/bin/zsh", want: "zsh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := foregroundActivityDisplay(tt.proc, tt.path, tt.shell, tt.home)
			if got != tt.want {
				t.Fatalf("display = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTerminalPaneCanRunNestedTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	isolateWebmuxTest(t)
	t.Setenv("SHELL", "/bin/sh")
	root := t.TempDir()
	manager := NewPaneManager(29000, "/bin/sh", root, "nested-tmux-test")

	pane, err := manager.CreatePane("terminal", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = manager.ClosePane(pane.ID)
	})
	state, ok := manager.terminal.getState(pane.ID)
	if !ok {
		t.Fatal("terminal state not found")
	}

	envResult := filepath.Join(root, "environment")
	command := fmt.Sprintf(`if [ -z "${TMUX+x}" ] && [ -z "${TMUX_PANE+x}" ]; then printf clean > %q; else printf leaked > %q; fi`, envResult, envResult)
	if err := manager.terminal.SendInput(pane.ID, &PaneInputRequest{Sequence: []PaneInputStep{
		{Type: "text", Value: command},
		{Type: "key", Value: "Enter"},
	}}); err != nil {
		t.Fatal(err)
	}
	waitForFileContents(t, envResult, "clean")

	innerSocket := filepath.Join(root, "inner.sock")
	command = fmt.Sprintf("tmux -S %q new-session -s inner", innerSocket)
	if err := manager.terminal.SendInput(pane.ID, &PaneInputRequest{Sequence: []PaneInputStep{
		{Type: "text", Value: command},
		{Type: "key", Value: "Enter"},
	}}); err != nil {
		t.Fatal(err)
	}
	waitForCommand(t, func() error {
		out, err := exec.Command("tmux", "-S", innerSocket, "display-message", "-p", "-t", "inner", "#{pane_current_command}").Output()
		if err != nil {
			return err
		}
		if string(out) == "" || string(out) == "tmux\n" {
			return fmt.Errorf("inner shell is not ready: foreground command = %q", out)
		}
		return nil
	})
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", innerSocket, "kill-server").Run() })

	innerResult := filepath.Join(root, "inner-input")
	command = fmt.Sprintf("printf nested-ok > %q", innerResult)
	if err := manager.terminal.SendInput(pane.ID, &PaneInputRequest{Sequence: []PaneInputStep{
		{Type: "text", Value: command},
		{Type: "key", Value: "Enter"},
	}}); err != nil {
		t.Fatal(err)
	}
	waitForFileContents(t, innerResult, "nested-ok")

	if err := manager.terminal.SendInput(pane.ID, &PaneInputRequest{Sequence: []PaneInputStep{
		{Type: "text", Value: "exit"},
		{Type: "key", Value: "Enter"},
	}}); err != nil {
		t.Fatal(err)
	}
	waitForCommand(t, func() error {
		if exec.Command("tmux", "-S", innerSocket, "has-session", "-t", "inner").Run() == nil {
			return fmt.Errorf("inner tmux session is still running")
		}
		return nil
	})

	if err := exec.Command("tmux", "-S", manager.terminal.tmuxSocketPath(), "has-session", "-t", state.tmuxSession).Run(); err != nil {
		t.Fatalf("outer tmux session stopped after using inner tmux: %v", err)
	}
}

func waitForFileContents(t *testing.T, path, want string) {
	t.Helper()
	waitForCommand(t, func() error {
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if string(contents) != want {
			return fmt.Errorf("contents = %q, want %q", contents, want)
		}
		return nil
	})
}

func waitForCommand(t *testing.T, check func() error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		if err = check(); err == nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal(err)
}
