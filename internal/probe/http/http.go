package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
)

const (
	httpDefaultTimeout = 10 * time.Second
	// httpBodyLimit is how much of the body is read, and what body_contains
	// is looked for in. It also caps the response headers.
	httpBodyLimit = 1 << 20
	// httpMaxRedirects is how many redirects are followed; one more is a
	// failed attempt.
	httpMaxRedirects = 10
	httpUserAgent    = "Pulsix-Probe/1"
)

var (
	errBadURL           = errors.New("invalid url")
	errBadMethod        = errors.New("invalid method")
	errTooManyRedirects = errors.New("too many redirects")
)

// probeHTTP requests url (with {address} replaced) and reports whether a
// response arrived, its status, the time to the end of the body read and,
// when body_contains is set, whether the first 1 MiB of the body holds it;
// each json_values entry is read from the same bytes, in a slot of gate.
// The check's secret (its fields in secret, its name in params) sends
// credentials. roots verifies https certificates; nil means the system
// roots.
func probeHTTP(ctx context.Context, g *probe.Guard, gate *JSONGate, params map[string]any, address string, secret map[string]string, roots *x509.CertPool) []probe.Observation {
	bodyContains := probe.StrParam(params, "body_contains", "")
	entries := jsonEntries(params)
	fail := func(msg string) []probe.Observation { return failAll(bodyContains != "", entries, msg) }

	target, err := httpURL(probe.StrParam(params, "url", ""), address)
	if err != nil {
		return fail(err.Error())
	}
	method := probe.StrParam(params, "method", http.MethodGet)
	if method != http.MethodGet && method != http.MethodHead {
		return fail(errBadMethod.Error())
	}
	cred, err := credentialsFor(probe.StrParam(params, "secret", ""), secret)
	if err != nil {
		return fail(err.Error())
	}

	ctx, cancel := probe.Bound(ctx, params, httpDefaultTimeout)
	defer cancel()

	// refused records a dial the guard refused on any hop: net/http does not
	// promise to keep probe.ErrNotAllowed in the error chain it returns.
	var refused atomic.Bool
	transport := newHTTPTransport(g, probe.BoolParam(params, "verify_tls", true), roots, &refused)
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: redirectPolicy(probe.BoolParam(params, "follow_redirects", true), cred.header),
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
	if err != nil {
		return fail(errBadURL.Error())
	}
	req.Header.Set("User-Agent", httpUserAgent)
	cred.apply(req)

	var hop hopTrace
	req = req.WithContext(httptrace.WithClientTrace(ctx, hop.trace()))

	start := time.Now()
	resp, err := client.Do(req)
	if refused.Load() {
		return fail(probe.ErrNotAllowed.Error())
	}
	if err != nil {
		// The client's error is never passed on: it quotes the URL.
		return probe.Down("http", hop.reason(err))
	}
	body, over, readErr := readBody(resp.Body, bodyContains != "" || len(entries) > 0, httpBodyLimit)
	elapsed := time.Since(start)
	_ = resp.Body.Close()

	// A status code is no failure: a response arrived.
	obs := []probe.Observation{
		probe.Value("http.up", 1),
		probe.Up("http"),
		probe.Value("http.status_code", float64(resp.StatusCode)),
		probe.Value("http.response_ms", probe.Millis(elapsed)),
	}
	if bodyContains != "" {
		obs = append(obs, probe.BoolValue("http.body_match", bytes.Contains(body, []byte(bodyContains))))
	}
	return append(obs, jsonObservations(ctx, gate, entries, body, over, readErr != nil)...)
}

// FailAll is the result of an http probe of params that could not be
// attempted, msg on every metric the params would have produced: the
// whole-host metrics, http.body_match when body_contains is set and each
// json_values entry's item. The runner reports it for a secret it cannot
// hand over.
func FailAll(params map[string]any, msg string) []probe.Observation {
	return failAll(probe.StrParam(params, "body_contains", "") != "", jsonEntries(params), msg)
}

// failAll is msg on the whole-host metrics, on http.body_match when
// bodyMatch and on each entry's http.json.value.
func failAll(bodyMatch bool, entries []jsonEntry, msg string) []probe.Observation {
	metrics := []string{"http.up", "http.failure", "http.status_code", "http.response_ms"}
	if bodyMatch {
		metrics = append(metrics, "http.body_match")
	}
	return append(probe.FailAll(msg, metrics...), failEntries(entries, msg)...)
}

// httpURL replaces {address} in raw with the host's address (an IPv6
// address bracketed) and accepts only an http or https URL with a host.
func httpURL(raw, address string) (*url.URL, error) {
	if strings.Contains(raw, probe.AddressPlaceholder) {
		host, err := probe.TargetHost(address)
		if err != nil {
			return nil, err
		}
		if strings.Contains(host, ":") {
			host = "[" + strings.ReplaceAll(host, "%", "%25") + "]"
		}
		raw = strings.ReplaceAll(raw, probe.AddressPlaceholder, host)
	}
	u, err := url.Parse(raw)
	// User info is refused like the url format refuses it: credentials
	// come from the check's secret only.
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return nil, errBadURL
	}
	return u, nil
}

// newHTTPTransport returns a single-use transport that dials only through
// the guard, uses no proxy and keeps no connection for a later probe. A dial
// the guard refuses sets refused.
func newHTTPTransport(g *probe.Guard, verify bool, roots *x509.CertPool, refused *atomic.Bool) *http.Transport {
	dialer := g.Dialer(0)
	return &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, addr)
			if errors.Is(err, probe.ErrNotAllowed) {
				refused.Store(true)
			}
			return conn, err
		},
		TLSClientConfig:        httpTLSConfig(verify, roots),
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: httpBodyLimit,
	}
}

// httpTLSConfig is the http probe's TLS client configuration. verify_tls
// false is the operator's explicit choice, typically for an old LAN
// appliance with a self-signed certificate; such appliances often speak
// only TLS 1.0 or 1.1, which Go's client floor of TLS 1.2 refuses, so an
// unverified probe accepts them too. A verifying probe keeps Go's floor.
func httpTLSConfig(verify bool, roots *x509.CertPool) *tls.Config {
	c := &tls.Config{RootCAs: roots, InsecureSkipVerify: !verify} //nolint:gosec // verify_tls false is the operator's choice
	if !verify {
		c.MinVersion = tls.VersionTLS10
	}
	return c
}

// redirectPolicy follows at most httpMaxRedirects redirects, or none: then
// the 3xx itself is the response. The credential header (none when "")
// goes along only while every hop keeps the first request's scheme, host
// and port: once a redirect leaves that origin, or goes from https to
// http, it is removed and stays removed.
func redirectPolicy(follow bool, header string) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if !follow {
			return http.ErrUseLastResponse
		}
		if len(via) > httpMaxRedirects {
			return errTooManyRedirects
		}
		if header != "" && !sameOriginThroughout(req, via) {
			req.Header.Del(header)
		}
		return nil
	}
}

// sameOriginThroughout reports whether req and every hop in via have the
// first hop's origin.
func sameOriginThroughout(req *http.Request, via []*http.Request) bool {
	if len(via) == 0 {
		return true
	}
	first := origin(via[0].URL)
	for _, r := range via[1:] {
		if origin(r.URL) != first {
			return false
		}
	}
	return origin(req.URL) == first
}

// origin is u's scheme, host (without case) and port, the scheme's
// default port when u names none.
func origin(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// readBody reads body up to limit bytes, keeping them when keep, and
// reports whether the body went on past limit (only when keep) and the
// error a read broke off with (a timeout, a reset). body_contains looks in
// what arrived either way. Nothing read leaves the probe.
func readBody(body io.Reader, keep bool, limit int64) ([]byte, bool, error) {
	if !keep {
		_, err := io.Copy(io.Discard, io.LimitReader(body, limit))
		return nil, false, err
	}
	buf, err := io.ReadAll(io.LimitReader(body, limit+1))
	if int64(len(buf)) > limit {
		return buf[:limit], true, nil
	}
	return buf, false, err
}

// NewProbe returns the http probe, as the registry runs it. Its JSON
// stages wait for a slot of gate, which the manager builds once so they
// are bounded across all its probes; nil is a gate of DefaultJSONStages
// of this probe's own.
func NewProbe(gate *JSONGate) probe.Func {
	if gate == nil {
		gate = NewJSONGate(DefaultJSONStages())
	}
	return func(ctx context.Context, g *probe.Guard, params map[string]any, address string, secret map[string]string) []probe.Observation {
		return probeHTTP(ctx, g, gate, params, address, secret, nil)
	}
}
