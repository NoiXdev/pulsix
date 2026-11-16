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
		{"past the limit", strings.NewReader("0123456789x"), true, bodyRead{size: 10, kept: []byte("0123"), truncated: true, tooLarge: true}},
		{"past the limit, not kept", strings.NewReader("0123456789x"), false, bodyRead{size: 10, tooLarge: true}},
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

// TestReadBodyStopsPastTheLimit: a body that never ends is read only to
// one byte past the limit.
func TestReadBodyStopsPastTheLimit(t *testing.T) {
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

// TestWantsReadLimit: only a size assertion reads past what is kept.
func TestWantsReadLimit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]any
		want   int64
	}{
		{"nothing", map[string]any{}, httpBodyLimit},
		{"body_contains", map[string]any{"body_contains": "ok"}, httpBodyLimit},
		{"other assertions", map[string]any{"assertions": []any{"st=status == 200", `b=body contains "x"`}}, httpBodyLimit},
		{"a size assertion", map[string]any{"assertions": []any{"st=status == 200", "s=size < 100"}}, httpCountLimit},
		{"an invalid size assertion", map[string]any{"assertions": []any{"s=size <"}}, httpBodyLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := wantsOf(tc.params).readLimit(); got != tc.want {
				t.Fatalf("readLimit = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestContentBytes: past the read's limit, the declared length when it
// covers what was read, else the limit, which is not exact.
func TestContentBytes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      bodyRead
		declared  int64
		want      int64
		wantExact bool
	}{
		{"read to its end", bodyRead{size: 1234}, -1, 1234, true},
		{"read to its end, declared otherwise", bodyRead{size: 1234}, 99, 1234, true},
		{"past the limit, nothing declared", bodyRead{size: 1000, tooLarge: true}, -1, 1000, false},
		{"past the limit, declared", bodyRead{size: 1000, tooLarge: true}, 5000, 5000, true},
		{"past the limit, declared one past it", bodyRead{size: 1000, tooLarge: true}, 1001, 1001, true},
		{"past the limit, declared less than was read", bodyRead{size: 1000, tooLarge: true}, 1000, 1000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := response{body: tc.body, declared: tc.declared}
			if got, exact := r.contentBytes(); got != tc.want || exact != tc.wantExact {
				t.Fatalf("contentBytes = %d, %v, want %d, %v", got, exact, tc.want, tc.wantExact)
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

	t.Run("a body past the limit is never an error on content_bytes", func(t *testing.T) {
		r := base()
		r.body, r.declared = bodyRead{size: 1 << 20, tooLarge: true}, -1
		m := probetest.WantUp(t, r.observations(), "http")
		for metric, o := range m {
			if o.Err != "" {
				t.Fatalf("%s = %+v, want no error", metric, o)
			}
		}
		if v := probetest.WantValue(t, m, "http.content_bytes"); v != 1<<20 {
			t.Fatalf("http.content_bytes = %v, want the limit", v)
		}
		r.declared = 5 << 20
		m = probetest.WantUp(t, r.observations(), "http")
		if v := probetest.WantValue(t, m, "http.content_bytes"); v != 5<<20 {
			t.Fatalf("http.content_bytes = %v, want the declared length", v)
		}
	})
}
