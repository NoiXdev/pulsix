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

// FS returns the embedded SPA files rooted at their serving root (so
// FS().Open("index.html") works, not FS().Open("dist/index.html")).
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// Handler serves the embedded build. It is mounted as the API router's
// NotFound handler (internal/api.New), so it only ever sees requests that
// did not match a registered API route.
func Handler() http.Handler {
	return HandlerFS(FS())
}

// HandlerFS builds the SPA handler over an arbitrary fs.FS rooted at the
// serving root (an embedded build in production, an fstest.MapFS in
// tests) — see Handler's own doc comment for the request contract, and
// finding "Important 10" in task-15a-report.md's fix round for why this
// takes an fs.FS instead of always reading the tracked placeholder files
// under dist/: `make build` replaces those files with the real build
// output (and CI never commits it back), so a test that opened them
// directly would depend on whatever happened to be on disk instead of on
// content it controls.
//
// Request handling, in order:
//  1. "/api" or anything under "/api/" always 404s — a typo'd or removed
//     API route must never look like "page not found" instead of
//     "endpoint not found" (finding "Important 9").
//  2. The root path "/" serves index.html directly.
//  3. A path that names a real file is served as that file — unless it
//     names a directory (e.g. "/assets/" with no index.html in it), which
//     404s rather than producing a directory listing (finding
//     "Important 9").
//  4. A path with no matching file: an extension-less path (a
//     client-side app route with no dot in its last segment) falls back
//     to index.html, the SPA's history-mode entry point. A path under
//     "/assets/" or one that has a file extension 404s instead — a
//     missing hashed asset (e.g. a stale chunk after a redeploy) must
//     come back as a real 404, not a 200 carrying the index page, or the
//     browser will try to parse HTML as JavaScript and white-screen
//     (finding "Important 9").
func HandlerFS(fsys fs.FS) http.Handler {
	files := http.FS(fsys)
	fileServer := http.FileServer(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		if path == "/api" || strings.HasPrefix(path, "/api/") {
			http.NotFound(w, r)
			return
		}

		if path == "/" {
			fileServer.ServeHTTP(w, r)
			return
		}

		if f, err := files.Open(path); err == nil {
			info, statErr := f.Stat()
			_ = f.Close()
			if statErr == nil && !info.IsDir() {
				fileServer.ServeHTTP(w, r)
				return
			}
			// A directory (most commonly /assets/ itself, with no
			// index.html inside it) — never list it.
			http.NotFound(w, r)
			return
		}

		if strings.HasPrefix(path, "/assets/") || hasExtension(path) {
			http.NotFound(w, r)
			return
		}

		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}

// hasExtension reports whether the final path segment contains a ".",
// e.g. "/foo/bar.js" (true) vs. "/o/acme/hosts" (false, an app route).
func hasExtension(p string) bool {
	base := p
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		base = p[i+1:]
	}
	return strings.Contains(base, ".")
}
