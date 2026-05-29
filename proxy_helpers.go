/* *
 * Webmux - a browser-based pane multiplexer
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
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// SECTION: PROXY HELPERS

func readProxyResponseBody(resp *http.Response) ([]byte, error) {
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gr, err := gzip.NewReader(resp.Body)
		if err != nil {
			resp.Body.Close()
			return nil, err
		}
		body, err := io.ReadAll(gr)
		gr.Close()
		resp.Body.Close()
		resp.Header.Del("Content-Encoding")
		return body, err
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	return body, err
}

func writeProxyResponseBody(resp *http.Response, content string) {
	resp.Body = io.NopCloser(strings.NewReader(content))
	resp.ContentLength = int64(len(content))
	resp.Header.Set("Content-Length", strconv.Itoa(len(content)))
}

func injectIntoHTMLHead(content, script string) string {
	bodyLower := strings.ToLower(content)
	headIdx := strings.Index(bodyLower, "<head>")
	if headIdx == -1 {
		return script + content
	}
	return content[:headIdx+6] + script + content[headIdx+6:]
}
