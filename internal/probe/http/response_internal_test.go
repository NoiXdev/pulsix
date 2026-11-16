package http

import (
	"bytes"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/noixdev/pulsix/internal/probe/probetest"
)

func TestReadBody(t *testing.T) {
	const keep, count = 4, 10
	broken := errors.New("reset")
	for _, tc := range []struct {
		name string
		body io.Reader
		keep bool
		want bodyRead
	}{
		{"short, kept", strings.NewReader("abc"), true, bodyRead{size: 3, kept: []byte("abc")}},
		{"short, not kept", strings.NewReader("abc"), false, bodyRead{size: 3}},
		{"exactly what is kept", strings.NewReader("abcd"), true, bodyRead{size: 4, kept: []byte("abcd")}},
		{"past what is kept", strings.NewReader("abcdefg"), true, bodyRead{size: 7, kept: []byte("abcd"), truncated: true}},
		{"past what is kept, not kept", strings.NewReader("abcdefg"), false, bodyRead{size: 7}},
		{"exactly what is counted", strings.NewReader("0123456789"), true, bodyRead{size: 10, kept: []byte("0123"), truncated: true}},
		{"past what is counted", strings.NewReader("0123456789x"), true, bodyRead{kept: []byte("0123"), truncated: true, tooLarge: true}},
		{"past what is counted, not kept", strings.NewReader("0123456789x"), false, bodyRead{tooLarge: true}},
		{"broken off", io.MultiReader(strings.NewReader("ab"), iotest.ErrReader(broken)), true, bodyRead{size: 2, kept: []byte("ab"), err: broken}},
		{"empty", strings.NewReader(""), true, bodyRead{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := readBody(tc.body, tc.keep, keep, count)
			if got.size != tc.want.size || got.tooLarge != tc.want.tooLarge || got.truncated != tc.want.truncated ||
				!bytes.Equal(got.kept, tc.want.kept) || !errors.Is(got.err, tc.want.err) {
				t.Fatalf("readBody = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestReadBodyStopsPastTheCount: a body that never ends is read only to
// one byte past the count.
func TestReadBodyStopsPastTheCount(t *testing.T) {
	r := &countingReader{}
	if got := readBody(r, false, 4, 1000); !got.tooLarge {
		t.Fatalf("readBody = %+v, want tooLarge", got)
	}
	if r.n > 1000+32*1024 {
		t.Fatalf("read %d bytes of an endless body, want about 1001", r.n)
	}
}

// countingReader is an endless body of zeros that counts what was read.
type countingReader struct{ n int }

func (r *countingReader) Read(p []byte) (int, error) {
	clear(p)
	r.n += len(p)
	return len(p), nil
}

func TestWantsKeepBody(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]any
		keep   bool
	}{
		{"nothing reads the body", map[string]any{}, false},
		{"body_contains", map[string]any{"body_contains": "ok"}, true},
		{"json_values", map[string]any{"json_values": []any{"a=$.a"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := wantsOf(tc.params).keepBody(); got != tc.keep {
				t.Fatalf("keepBody = %v, want %v", got, tc.keep)
			}
		})
	}
}

func TestResponseObservations(t *testing.T) {
	at := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	leaf := &x509.Certificate{
		NotAfter: at.Add(36 * time.Hour),
		Issuer:   pkix.Name{CommonName: "Test CA\x07", Organization: []string{"Org"}},
	}
	base := func() response {
		return response{
			status:    200,
			elapsed:   ms(250),
			timing:    timing{connect: ms(2), ttfb: ms(100), transfer: ms(40)},
			redirects: 2,
			body:      bodyRead{size: 1234},
			at:        at,
		}
	}

	t.Run("http over an IP literal", func(t *testing.T) {
		r := base()
		m := probetest.WantUp(t, r.observations(), "http")
		want := map[string]float64{
			"http.status_code": 200, "http.response_ms": 250, "http.connect_ms": 2, "http.ttfb_ms": 100,
			"http.transfer_ms": 40, "http.redirects": 2, "http.content_bytes": 1234,
		}
		for metric, v := range want {
			if got := probetest.WantValue(t, m, metric); got != v {
				t.Fatalf("%s = %v, want %v", metric, got, v)
			}
		}
		for _, absent := range []string{"http.dns_ms", "http.tls_ms", "http.cert_expiry_days", "http.cert_issuer"} {
			if o, ok := m[absent]; ok {
				t.Fatalf("%s = %+v, want absent", absent, o)
			}
		}
		if len(m) != 2+len(want) {
			t.Fatalf("observations = %+v, want up, failure and %d more", m, len(want))
		}
	})

	t.Run("https over a name", func(t *testing.T) {
		r := base()
		r.timing.dns, r.timing.resolved = ms(5), true
		r.timing.tls, r.timing.secured = ms(20), true
		r.leaf = leaf
		m := probetest.WantUp(t, r.observations(), "http")
		if v := probetest.WantValue(t, m, "http.dns_ms"); v != 5 {
			t.Fatalf("http.dns_ms = %v, want 5", v)
		}
		if v := probetest.WantValue(t, m, "http.tls_ms"); v != 20 {
			t.Fatalf("http.tls_ms = %v, want 20", v)
		}
		if v := probetest.WantValue(t, m, "http.cert_expiry_days"); v != 1.5 {
			t.Fatalf("http.cert_expiry_days = %v, want 1.5", v)
		}
		if l := probetest.WantLabel(t, m, "http.cert_issuer"); l != "Test CA" {
			t.Fatalf("http.cert_issuer = %q, want the cleaned common name", l)
		}
	})

	t.Run("an expired certificate counts down below zero", func(t *testing.T) {
		r := base()
		r.leaf = &x509.Certificate{NotAfter: at.Add(-12 * time.Hour)}
		m := probetest.WantUp(t, r.observations(), "http")
		if v := probetest.WantValue(t, m, "http.cert_expiry_days"); v != -0.5 {
			t.Fatalf("http.cert_expiry_days = %v, want -0.5", v)
		}
	})

	t.Run("a body past the count is an error on content_bytes only", func(t *testing.T) {
		r := base()
		r.body = bodyRead{tooLarge: true}
		m := probetest.WantUp(t, r.observations(), "http")
		if e := m["http.content_bytes"].Err; e != "larger than 16 MiB" {
			t.Fatalf("http.content_bytes = %+v, want the error %q", m["http.content_bytes"], "larger than 16 MiB")
		}
		for metric, o := range m {
			if metric != "http.content_bytes" && o.Err != "" {
				t.Fatalf("%s = %+v, want no error", metric, o)
			}
		}
	})
}
