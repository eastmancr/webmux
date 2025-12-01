//go:build !dev
/* *
 * Webmux - a browser-based terminal multiplexer
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
	"io/fs"
	"net/http"
)

// InitDevMode is a no-op in production builds
func InitDevMode(mux *http.ServeMux, server *Server) http.Handler {
	// In production, serve from embedded files
	staticFS, _ := fs.Sub(staticFiles, "static")
	return http.FileServer(http.FS(staticFS))
}
