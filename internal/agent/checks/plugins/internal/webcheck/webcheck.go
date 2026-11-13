// Package webcheck holds what the web server check plugins (nginx, apache,
// caddy, phpfpm) share: fetching a status page under hard limits, the
// optional basic-auth secret and the parameter helpers. It is internal to
// plugins/. Failure texts are fixed strings: never the URL, a header, the
// body, a Go error message, the user or the password.
package webcheck

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
)

const (
	// MaxBody is the largest page Fetch returns.
	MaxBody = 4 << 20
	// UserAgent is sent with every request.
	UserAgent = "pulsix-agent"
)

// ErrFormat is the failure of a page that is not the status page of the
// plugin's server, the same text for all four plugins.
var ErrFormat = errors.New("unexpected response format")

// Auth is the optional basic-authentication credential of a secret.
type Auth struct {
	User, Password string
	// Set is true when a secret was named: the header is sent exactly then.
	Set bool
}

var authSpec = []dbcheck.Field{
	{Name: "basic_auth_user", Required: true},
	{Name: "basic_auth_password"},
}

// ParseAuth reads a secret's fields. Nil fields (no secret named) mean no
// authentication; a named secret needs basic_auth_user, and any other field
// is refused.
func ParseAuth(fields map[string]string) (Auth, error) {
	if fields == nil {
		return Auth{}, nil
	}
	v, err := dbcheck.Parse(fields, authSpec)
	if err != nil {
		return Auth{}, err
	}
	return Auth{User: v["basic_auth_user"], Password: v["basic_auth_password"], Set: true}, nil
}

// Options are the per-check settings of a fetch.
type Options struct {
	VerifyTLS bool
	Auth      Auth
	// MaxBody bounds the page tighter than the package's MaxBody, for a
	// plugin whose real page is small; 0, or a value above MaxBody, means
	// MaxBody.
	MaxBody int
}

// limit is the largest body a fetch with opt returns.
func (opt Options) limit() int {
	if opt.MaxBody > 0 && opt.MaxBody < MaxBody {
		return opt.MaxBody
	}
	return MaxBody
}

// newClient builds a fresh client per call: no shared transport, no proxy
// (status pages are local and credentials must not go to a proxy), no
// redirects (a 3xx reads as its status), TLS 1.2 or newer, one connection
// per request.
func newClient(opt Options) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: dbcheck.QueryTimeout}).DialContext,
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: !opt.VerifyTLS}, //nolint:gosec // verify_tls = false is the operator's explicit choice
			TLSHandshakeTimeout:   dbcheck.QueryTimeout,
			ResponseHeaderTimeout: dbcheck.QueryTimeout,
			DisableKeepAlives:     true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Fetch GETs rawURL and returns the body of a 200 response within MaxBody
// bytes (or opt.MaxBody). Otherwise body is nil and reason is one fixed text. The request
// takes at most dbcheck.QueryTimeout; the caller's context ending reads as
// "timed out" too, and the plugin tells the two apart.
func Fetch(ctx context.Context, rawURL string, opt Options) (body []byte, reason string) {
	ctx, cancel := context.WithTimeout(ctx, dbcheck.QueryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, classify(err)
	}
	if req.URL.User != nil {
		// FormatStatusURL refuses user info; this is the second layer, so
		// net/http never derives a basic-auth header from the URL.
		return nil, "connection failed"
	}
	req.Header.Set("User-Agent", UserAgent)
	if opt.Auth.Set {
		req.SetBasicAuth(opt.Auth.User, opt.Auth.Password)
	}
	resp, err := newClient(opt).Do(req)
	if err != nil {
		return nil, classify(err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return nil, "authentication failed (HTTP 401)"
	case http.StatusForbidden:
		return nil, "permission denied (HTTP 403)"
	default:
		return nil, "HTTP status " + strconv.Itoa(resp.StatusCode)
	}
	limit := opt.limit()
	body, err = io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, classify(err)
	}
	if len(body) > limit {
		return nil, "response too large"
	}
	return body, ""
}

// classify drops Go's *url.Error wrapper (its text carries the URL) before
// handing the cause to dbcheck.Classify, which only ever returns fixed texts.
// A URL that does not even parse is a plain connection failure.
func classify(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		err = ue.Err
	}
	return dbcheck.Classify(err)
}

// WithQueryKey appends the bare query key (auto, json) when the URL has no
// query key of that name; everything else is left as written.
func WithQueryKey(rawURL, key string) string {
	base, frag, hasFrag := strings.Cut(rawURL, "#")
	if hasFrag {
		frag = "#" + frag
	}
	base, query, hasQuery := strings.Cut(base, "?")
	switch {
	case !hasQuery:
		return base + "?" + key + frag
	case query == "":
		return base + "?" + key + frag
	}
	for _, pair := range strings.Split(query, "&") {
		name, _, _ := strings.Cut(pair, "=")
		if n, err := url.QueryUnescape(name); err == nil {
			name = n
		}
		if name == key {
			return rawURL
		}
	}
	if !strings.HasSuffix(query, "&") {
		query += "&"
	}
	return base + "?" + query + key + frag
}

// URLParam reads a string parameter; absent, empty or not a string mean def.
func URLParam(params map[string]any, name, def string) string {
	if s, ok := params[name].(string); ok && s != "" {
		return s
	}
	return def
}

// VerifyTLS reads the verify_tls parameter; the default is true.
func VerifyTLS(params map[string]any) bool {
	if b, ok := params["verify_tls"].(bool); ok {
		return b
	}
	return true
}

// DownItem is dbcheck.Down for one item (phpfpm's pools): up = 0 and an
// error observation "<prefix>: <reason>" on each of others.
func DownItem(item, prefix, up string, others []string, reason string) []checks.Observation {
	obs := dbcheck.Down(prefix, up, others, reason)
	for i := range obs {
		obs[i].Item = item
	}
	return obs
}
