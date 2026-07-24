/*
 * webmux - Browser-based pane multiplexer
 * Copyright (C) 2026 Webmux contributors
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 */

package shell

import (
	"strings"
	"testing"
)

func TestClipboardWrappersForwardTypedRequests(t *testing.T) {
	scripts := ClipboardWrapperScripts("/tmp/webmux wm")
	byName := make(map[string]string, len(scripts))
	for _, script := range scripts {
		byName[script.Name] = script.Content
		if strings.Contains(script.Content, "%!") {
			t.Fatalf("wrapper %s contains an unexpanded format directive", script.Name)
		}
	}

	checks := map[string][]string{
		"wl-copy":  {`copy --type "$type"`, `--type=*)`},
		"wl-paste": {`paste --request --list-types`, `paste --request --type "$type"`},
		"xclip":    {`paste --request --list-types`, `paste --request --type "$target"`, `copy --type "$target"`},
		"xsel":     {`paste --request --type text/plain`},
		"pbpaste":  {`paste --request --type text/plain`},
	}
	for name, fragments := range checks {
		content, ok := byName[name]
		if !ok {
			t.Fatalf("missing wrapper %s", name)
		}
		for _, fragment := range fragments {
			if !strings.Contains(content, fragment) {
				t.Errorf("wrapper %s does not contain %q", name, fragment)
			}
		}
	}
}
