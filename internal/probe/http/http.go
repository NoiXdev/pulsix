package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
)

const (
	httpDefaultTimeout = 10 * time.Second
	// httpBodyLimit is how much of the body is read and kept, and what
	// body_contains, json_values and the body assertions look in (a size
	// assertion reads on to httpCountLimit). It also caps the response
	// headers.
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

// probeHTTP requests url (with {address} replaced) with the check's
// method, headers and body (requestOf) and reports whether a
// response arrived, its status, the time to the end of the body read, its
// breakdown (lookups, connects, handshakes, time to first byte, transfer),
// the redirects followed, the body's size and, over https, the final hop's
// certificate; when body_contains is set, whether the first 1 MiB of the
// body holds it; each json_values entry is read from the same bytes, in a
// slot of gate; each assertion is evaluated on the response (html and
// json in a slot of gate too). A failed attempt reports neither. The check's secret (its fields in secret, its name in
// params) sends credentials. roots verifies https certificates; nil means
// the system roots.
func probeHTTP(ctx context.Context, g *probe.Guard, gate *JSONGate, params map[string]any, address string, secret map[string]string, roots *x509.CertPool) []probe.Observation {
	w := wantsOf(params)
	rawURL := probe.StrParam(params, "url", "")
	fail := func(msg string) []probe.Observation { return failAll(rawURL, w, msg) }

	target, err := httpURL(rawURL, address)
	if err != nil {
		return fail(err.Error())
	}
	rq, err := requestOf(params)
	if err != nil {
		return fail(err.Error())
	}
	// A request that may change its target goes out only while the
	// operator allows it; a check stored while it was allowed reads this
	// error after it is turned off, never a plain GET.
	if rq.writes() && !g.WritesAllowed() {
		return fail(errWritesOff.Error())
	}
	method := rq.method
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
		CheckRedirect: redirectPolicy(probe.BoolParam(params, "follow_redirects", true), append(rq.headerNames(), cred.header)),
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), rq.bodyReader())
	if err != nil {
		return fail(errBadURL.Error())
	}
	req.Header.Set("User-Agent", httpUserAgent)
	rq.apply(req)
	cred.apply(req)

	// hop owns the failure reason, phases the timing breakdown; the
	// context composes their hooks.
	var hop hopTrace
	phases := newPhases(time.Now)
	traced := httptrace.WithClientTrace(httptrace.WithClientTrace(ctx, hop.trace()), phases.trace())
	req = req.WithContext(traced)

	start := time.Now()
	resp, err := client.Do(req)
	if refused.Load() {
		return fail(probe.ErrNotAllowed.Error())
	}
	if err != nil {
		// The client's error is never passed on: it quotes the URL.
		return probe.Down("http", hop.reason(err))
	}
	body := readBody(resp.Body, w.keepBody(), httpBodyLimit, w.readLimit())
	end := time.Now()
	_ = resp.Body.Close()

	r := response{
		status:    resp.StatusCode,
		header:    resp.Header,
		elapsed:   end.Sub(start),
		timing:    phases.timing(end),
		redirects: redirectsBefore(resp),
		body:      body,
		declared:  resp.ContentLength,
		leaf:      leafOf(resp),
		at:        end,
	}
	if method == http.MethodHead {
		// No body is read: the time after the first byte is the header's.
		r.timing.transfer = 0
	}

	// A status code is no failure: a response arrived.
	obs := r.observations()
	if w.bodyContains != "" {
		obs = append(obs, probe.BoolValue("http.body_match", bytes.Contains(body.kept, []byte(w.bodyContains))))
	}
	obs = append(obs, jsonObservations(ctx, gate, w.entries, body.kept, body.truncated, body.err != nil)...)
	return append(obs, assertObservations(ctx, gate, w.assertions, &r)...)
}

// FailAll is the result of an http probe of params that could not be
// attempted, msg on every metric the params would surely have produced
// (certainHostMetrics), on http.body_match when body_contains is set, on
// each json_values entry's item and on both metrics of each assertion's
// item. The runner reports it for a secret it
// cannot hand over.
func FailAll(params map[string]any, msg string) []probe.Observation {
	return failAll(probe.StrParam(params, "url", ""), wantsOf(params), msg)
}

// certainHostMetrics are the whole-host metrics every up attempt of
// rawURL reports, the ones an attempt that could not be made puts its
// error on. A check_current row is never deleted and an error row reads
// unknown, so an error on a metric the URL never reports again would keep
// the host unknown after it recovers; a metric left out only goes stale
// for the outage. Hence http.dns_ms only for a literal name (not an IP
// literal, not {address}, which FailAll cannot resolve), http.tls_ms only
// for https, and never the certificate's metrics: the final hop may be
// http.
func certainHostMetrics(rawURL string) []string {
	resolves, secured := urlShape(rawURL)
	metrics := []string{"http.up", "http.failure", "http.status_code", "http.response_ms"}
	if resolves {
		metrics = append(metrics, "http.dns_ms")
	}
	metrics = append(metrics, "http.connect_ms")
	if secured {
		metrics = append(metrics, "http.tls_ms")
	}
	return append(metrics, "http.ttfb_ms", "http.transfer_ms", "http.redirects", "http.content_bytes")
}

// urlShape reports whether rawURL's host is a name to look up (neither
// an IP literal nor {address}) and whether its scheme is https; a URL
// that does not parse is neither.
func urlShape(rawURL string) (resolves, secured bool) {
	if strings.Contains(rawURL, probe.AddressPlaceholder) {
		return false, strings.HasPrefix(strings.ToLower(rawURL), "https://")
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return false, false
	}
	_, ipErr := netip.ParseAddr(u.Hostname())
	return ipErr != nil, u.Scheme == "https"
}

// failAll is msg on rawURL's certain host metrics, on http.body_match
// when body_contains is set, on each entry's http.json.value and on
// http.assertion and http.assertion_actual of each assertion's item.
func failAll(rawURL string, w wants, msg string) []probe.Observation {
	obs := probe.FailAll(msg, certainHostMetrics(rawURL)...)
	if w.bodyContains != "" {
		obs = append(obs, probe.Observation{Metric: "http.body_match", Err: msg})
	}
	obs = append(obs, failEntries(w.entries, msg)...)
	return append(obs, failAssertions(w.assertions, msg)...)
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
// the 3xx itself is the response. The configured headers (the check's
// plain headers and its credential header; "" names none) go along only
// while every hop keeps the first request's scheme, host and port: once a
// redirect leaves that origin, or goes from https to http, they are
// removed and stay removed.
func redirectPolicy(follow bool, headers []string) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if !follow {
			return http.ErrUseLastResponse
		}
		if len(via) > httpMaxRedirects {
			return errTooManyRedirects
		}
		if !sameOriginThroughout(req, via) {
			for _, h := range headers {
				if h != "" {
					req.Header.Del(h)
				}
			}
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
