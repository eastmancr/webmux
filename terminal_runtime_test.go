package main

import "testing"

func TestIdleShellProcessDisplay(t *testing.T) {
	tests := []struct {
		name  string
		proc  string
		path  string
		shell string
		want  string
	}{
		{name: "idle shell", proc: "zsh", path: "/home/user/projects/webmux", shell: "/bin/zsh", want: "/webmux"},
		{name: "root directory", proc: "bash", path: "/", shell: "/bin/bash", want: "/"},
		{name: "running command", proc: "vim", path: "/home/user/projects/webmux", shell: "/bin/zsh", want: "vim"},
		{name: "missing path", proc: "zsh", shell: "/bin/zsh", want: "zsh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := foregroundProcessDisplay(tt.proc, tt.path, tt.shell)
			if got != tt.want {
				t.Fatalf("display = %q, want %q", got, tt.want)
			}
		})
	}
}
