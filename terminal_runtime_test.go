package main

import "testing"

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
