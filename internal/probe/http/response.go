package http

import (
	"crypto/x509"
	"io"
	"net/http"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
)

const (
	// httpCountLimit is how much of a body a check with a size assertion
	// reads to count it; without one the read stops past httpBodyLimit,
	// as before http.content_bytes existed. A longer body stops the read
	// there.
	httpCountLimit = 16 << 20
	// errSizeUnknown is a size assertion on a body that went on past
	// httpCountLimit and declared no length that covers it.
	errSizeUnknown = "the response is larger than 16 MiB"
)

// wants is what a check's params ask of a response beyond the request's
// own metrics.
type wants struct {
	bodyContains string
	entries      []jsonEntry
	assertions   []assertEntry
}

// wantsOf reads the parts of params that look at a response.
func wantsOf(params map[string]any) wants {
	return wants{
		bodyContains: probe.StrParam(params, "body_contains", ""),
		entries:      jsonEntries(params),
		assertions:   assertEntries(params),
	}
}

// keepBody reports whether the first httpBodyLimit bytes of a body are
// kept: something reads them. This is the one condition to extend for a
// new reader of the body.
func (w wants) keepBody() bool {
	return w.bodyContains != "" || len(w.entries) > 0 || keepsBody(w.assertions)
}

// readLimit is how much of a body is read: httpCountLimit when a size
// assertion counts it, else httpBodyLimit. Only the size assertion reads
// further than what is kept, so a check without one costs no more
// bandwidth than before http.content_bytes existed.
func (w wants) readLimit() int64 {
	if countsBody(w.assertions) {
		return httpCountLimit
	}
	return httpBodyLimit
}

// bodyRead is what was read of a response body. Nothing read leaves the
// probe as text.
type bodyRead struct {
	// size is the bytes received, at most the read's limit; the limit
	// itself when tooLarge.
	size int64
	// tooLarge: the body went on past the read's limit and the read
	// stopped.
	tooLarge bool
	// kept is the first httpBodyLimit bytes when they were wanted, else nil;
	// truncated: the body went on past them (only when kept).
	kept      []byte
	truncated bool
	// err is the error the read broke off with (a timeout, a reset); never
	// set when tooLarge.
	err error
}

// readBody reads body to its end or one byte past limit, counting what
// arrived and keeping its first keep bytes when wanted; the rest is
// discarded.
func readBody(body io.Reader, want bool, keep, limit int64) bodyRead {
	w := &keeper{want: want, keep: keep}
	n, err := io.Copy(w, io.LimitReader(body, limit+1))
	if n > limit {
		return bodyRead{size: limit, tooLarge: true, kept: w.buf, truncated: w.truncated}
	}
	return bodyRead{size: n, kept: w.buf, truncated: w.truncated, err: err}
}

// keeper keeps the first keep bytes written to it, when want, and
// discards everything.
type keeper struct {
	want      bool
	keep      int64
	buf       []byte
	truncated bool
}

func (k *keeper) Write(p []byte) (int, error) {
	if !k.want {
		return len(p), nil
	}
	room := k.keep - int64(len(k.buf))
	if int64(len(p)) > room {
		k.buf = append(k.buf, p[:room]...)
		k.truncated = true
	} else {
		k.buf = append(k.buf, p...)
	}
	return len(p), nil
}

// response is what an attempt saw once a response arrived: the record the
// http metrics are taken from, and the one assertions are evaluated
// against.
type response struct {
	status int
	// header is the final response's.
	header http.Header
	// elapsed runs from the request's start to the end of the body read.
	elapsed   time.Duration
	timing    timing
	redirects int
	body      bodyRead
	// declared is the final response's Content-Length, -1 when it sent
	// none (or the transport decompressed the body).
	declared int64
	// leaf is the final hop's leaf certificate, nil over http.
	leaf *x509.Certificate
	// at is when the body read ended; the certificate's days count from it.
	at time.Time
}

// contentBytes is http.content_bytes: the bytes read; past the read's
// limit the declared length when it covers what was read (more than the
// limit), else the limit, which is then not exact.
func (r *response) contentBytes() (n int64, exact bool) {
	switch {
	case !r.body.tooLarge:
		return r.body.size, true
	case r.declared > r.body.size:
		return r.declared, true
	default:
		return r.body.size, false
	}
}

// redirectsBefore counts the redirects that led to resp: each request a
// redirect made carries the response that caused it.
func redirectsBefore(resp *http.Response) int {
	n := 0
	for r := resp.Request; r != nil && r.Response != nil; r = r.Response.Request {
		n++
	}
	return n
}

// leafOf is the leaf certificate resp arrived over, nil over http.
func leafOf(resp *http.Response) *x509.Certificate {
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		return nil
	}
	return resp.TLS.PeerCertificates[0]
}

// observations are r's whole-host metrics: up, the status, the timings
// (http.dns_ms only when a hop looked up a name, http.tls_ms only when one
// shook hands over TLS), the redirects, the size (contentBytes) and, over
// https, the final hop's certificate.
func (r *response) observations() []probe.Observation {
	obs := append(probe.Up("http"),
		probe.Value("http.status_code", float64(r.status)),
		probe.Value("http.response_ms", probe.Millis(r.elapsed)),
	)
	if r.timing.resolved {
		obs = append(obs, probe.Value("http.dns_ms", probe.Millis(r.timing.dns)))
	}
	obs = append(obs, probe.Value("http.connect_ms", probe.Millis(r.timing.connect)))
	if r.timing.secured {
		obs = append(obs, probe.Value("http.tls_ms", probe.Millis(r.timing.tls)))
	}
	obs = append(obs,
		probe.Value("http.ttfb_ms", probe.Millis(r.timing.ttfb)),
		probe.Value("http.transfer_ms", probe.Millis(r.timing.transfer)),
		probe.Value("http.redirects", float64(r.redirects)),
	)
	// Never an error: past the read's limit an estimate (contentBytes).
	size, _ := r.contentBytes()
	obs = append(obs, probe.Value("http.content_bytes", float64(size)))
	if r.leaf != nil {
		obs = append(obs,
			probe.Value("http.cert_expiry_days", r.leaf.NotAfter.Sub(r.at).Hours()/24),
			probe.Text("http.cert_issuer", probe.IssuerLabel(r.leaf)),
		)
	}
	return obs
}
