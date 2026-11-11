package probe

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
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
// when body_contains is set, whether the first 1 MiB of the body holds it.
// roots verifies https certificates; nil means the system roots.
func probeHTTP(ctx context.Context, g *Guard, params map[string]any, address string, roots *x509.CertPool) []Observation {
	bodyContains := strParam(params, "body_contains", "")
	metrics := []string{"http.up", "http.status_code", "http.response_ms"}
	if bodyContains != "" {
		metrics = append(metrics, "http.body_match")
	}

	target, err := httpURL(strParam(params, "url", ""), address)
	if err != nil {
		return failAll(err.Error(), metrics...)
	}
	method := strParam(params, "method", http.MethodGet)
	if method != http.MethodGet && method != http.MethodHead {
		return failAll(errBadMethod.Error(), metrics...)
	}

	ctx, cancel := bound(ctx, params, httpDefaultTimeout)
	defer cancel()

	// refused records a dial the guard refused on any hop: net/http does not
	// promise to keep ErrNotAllowed in the error chain it returns.
	var refused atomic.Bool
	transport := newHTTPTransport(g, boolParam(params, "verify_tls", true), roots, &refused)
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: redirectPolicy(boolParam(params, "follow_redirects", true)),
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
	if err != nil {
		return failAll(errBadURL.Error(), metrics...)
	}
	req.Header.Set("User-Agent", httpUserAgent)

	start := time.Now()
	resp, err := client.Do(req)
	if refused.Load() {
		return failAll(ErrNotAllowed.Error(), metrics...)
	}
	if err != nil {
		return down("http")
	}
	match := readBody(resp.Body, bodyContains, httpBodyLimit)
	elapsed := time.Since(start)
	_ = resp.Body.Close()

	obs := []Observation{
		value("http.up", 1),
		value("http.status_code", float64(resp.StatusCode)),
		value("http.response_ms", millis(elapsed)),
	}
	if bodyContains != "" {
		obs = append(obs, boolValue("http.body_match", match))
	}
	return obs
}

// httpURL replaces {address} in raw with the host's address (an IPv6
// address bracketed) and accepts only an http or https URL with a host.
func httpURL(raw, address string) (*url.URL, error) {
	if strings.Contains(raw, addressPlaceholder) {
		host, err := targetHost(address)
		if err != nil {
			return nil, err
		}
		if strings.Contains(host, ":") {
			host = "[" + strings.ReplaceAll(host, "%", "%25") + "]"
		}
		raw = strings.ReplaceAll(raw, addressPlaceholder, host)
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, errBadURL
	}
	return u, nil
}

// newHTTPTransport returns a single-use transport that dials only through
// the guard, uses no proxy and keeps no connection for a later probe. A dial
// the guard refuses sets refused.
func newHTTPTransport(g *Guard, verify bool, roots *x509.CertPool, refused *atomic.Bool) *http.Transport {
	dialer := g.Dialer(0)
	return &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, addr)
			if errors.Is(err, ErrNotAllowed) {
				refused.Store(true)
			}
			return conn, err
		},
		TLSClientConfig: &tls.Config{
			RootCAs: roots,
			// verify_tls false is the operator's explicit choice.
			InsecureSkipVerify: !verify,
		},
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: httpBodyLimit,
	}
}

// redirectPolicy follows at most httpMaxRedirects redirects, or none: then
// the 3xx itself is the response.
func redirectPolicy(follow bool) func(*http.Request, []*http.Request) error {
	return func(_ *http.Request, via []*http.Request) error {
		if !follow {
			return http.ErrUseLastResponse
		}
		if len(via) > httpMaxRedirects {
			return errTooManyRedirects
		}
		return nil
	}
}

// readBody reads body up to limit bytes and reports whether want appears in
// what was read. Nothing read leaves the probe.
func readBody(body io.Reader, want string, limit int64) bool {
	capped := io.LimitReader(body, limit)
	if want == "" {
		_, _ = io.Copy(io.Discard, capped)
		return false
	}
	buf, _ := io.ReadAll(capped)
	return bytes.Contains(buf, []byte(want))
}
