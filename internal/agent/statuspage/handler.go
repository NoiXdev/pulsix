package statuspage

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"time"
)

// maxLoginBody bounds the login form.
const maxLoginBody = 4 << 10

// handler serves the page, its JSON form and the login form. Every
// response carries the security headers; only GET and HEAD are served,
// plus the login form's POST.
type handler struct {
	src    Source
	auth   *auth
	redact func(string) string
	now    func() time.Time
	log    *slog.Logger
}

func newHandler(src Source, token string, redact func(string) string, now func() time.Time, log *slog.Logger) *handler {
	return &handler{src: src, auth: newAuth(token, now), redact: redact, now: now, log: log}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	nonce := newNonce()
	hd := w.Header()
	hd.Set("Content-Security-Policy", "default-src 'none'; style-src 'nonce-"+nonce+"'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Referrer-Policy", "no-referrer")
	hd.Set("X-Frame-Options", "DENY")
	hd.Set("Cache-Control", "no-store")

	switch r.URL.Path {
	case "/", "/status.json":
		if !allow(w, r, http.MethodGet, http.MethodHead) {
			return
		}
		h.serveStatus(w, r, nonce)
	case "/login":
		if h.auth.open() {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		if !allow(w, r, http.MethodGet, http.MethodHead, http.MethodPost) {
			return
		}
		h.serveLogin(w, r, nonce)
	default:
		if !allow(w, r, http.MethodGet, http.MethodHead) {
			return
		}
		http.NotFound(w, r)
	}
}

// allow answers 405 for any method not in methods.
func allow(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	for _, m := range methods {
		if r.Method == m {
			return true
		}
	}
	allowed := methods[0]
	for _, m := range methods[1:] {
		allowed += ", " + m
	}
	w.Header().Set("Allow", allowed)
	http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	return false
}

func (h *handler) serveStatus(w http.ResponseWriter, r *http.Request, nonce string) {
	isJSON := r.URL.Path == "/status.json"
	if h.auth.fails.blocked(remoteIP(r)) {
		http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
		return
	}
	if !h.auth.authorized(r) {
		if isJSON || r.Header.Get("Authorization") != "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="pulsix-agent"`)
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	snap := h.src.Snapshot().redacted(h.redact)
	if isJSON {
		h.writeJSON(w, snap)
		return
	}
	h.render(w, pageTmpl, http.StatusOK, pageData{T: textsFor(r), Nonce: nonce, S: snap, Refresh: refreshSeconds})
}

func (h *handler) serveLogin(w http.ResponseWriter, r *http.Request, nonce string) {
	data := pageData{T: textsFor(r), Nonce: nonce}
	ip := remoteIP(r)
	if h.auth.fails.blocked(ip) {
		data.TooMany = true
		h.render(w, loginTmpl, http.StatusTooManyRequests, data)
		return
	}
	if r.Method != http.MethodPost {
		h.render(w, loginTmpl, http.StatusOK, data)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if !h.auth.tokenOK(r.PostForm.Get("token")) {
		h.auth.fails.fail(ip)
		data.Failed = true
		h.render(w, loginTmpl, http.StatusUnauthorized, data)
		return
	}
	http.SetCookie(w, h.auth.sessionCookie(r.TLS != nil))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// render executes t into a buffer first, so a template error is a clean
// 500 rather than half a page.
func (h *handler) render(w http.ResponseWriter, t *template.Template, status int, data pageData) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		h.log.Warn("status page could not be rendered", "err", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func (h *handler) writeJSON(w http.ResponseWriter, snap Snapshot) {
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		h.log.Warn("status page JSON could not be encoded", "err", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(append(b, '\n'))
}

// newNonce is a fresh CSP nonce for one response.
func newNonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}
