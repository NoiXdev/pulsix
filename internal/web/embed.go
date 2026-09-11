// Package web serves the built SPA from the binary.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler serves static assets and falls back to index.html for app
// routes. It is mounted as the API router's NotFound handler
// (internal/api.New), so it only ever sees requests that did not match a
// registered API route — including, deliberately, any /api/... path that
// does not exist: those must keep 404ing as missing API endpoints, never
// silently fall back to the SPA's index page, or a typo'd or removed API
// route would look to a client like "page not found" instead of "endpoint
// not found".
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FS(sub)
	fileServer := http.FileServer(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if f, err := files.Open(r.URL.Path); err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}
