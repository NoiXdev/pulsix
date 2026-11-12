package dbcheck

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

func TestParse(t *testing.T) {
	spec := []Field{
		{Name: "host", Required: true},
		{Name: "port", Default: "5432", Valid: Port, Want: "a valid port"},
		{Name: "sslmode", Default: "prefer", Valid: OneOf("disable", "prefer"), Want: "one of disable, prefer"},
	}
	tests := []struct {
		name    string
		fields  map[string]string
		want    map[string]string
		wantErr string
	}{
		{"defaults", map[string]string{"host": "h"}, map[string]string{"host": "h", "port": "5432", "sslmode": "prefer"}, ""},
		{"override", map[string]string{"host": "h", "port": "1"}, map[string]string{"host": "h", "port": "1", "sslmode": "prefer"}, ""},
		{"empty value uses default", map[string]string{"host": "h", "port": ""}, map[string]string{"host": "h", "port": "5432", "sslmode": "prefer"}, ""},
		{"nil map", nil, nil, "secret field `host` is required"},
		{"missing", map[string]string{}, nil, "secret field `host` is required"},
		{"bad port", map[string]string{"host": "h", "port": "99999"}, nil, "secret field `port` is not a valid port"},
		{"bad enum", map[string]string{"host": "h", "sslmode": "x"}, nil, "secret field `sslmode` is not one of disable, prefer"},
		{"unknown", map[string]string{"host": "h", "bogus": "x"}, nil, "secret field `bogus` is not known"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.fields, spec)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestParseErrorNeverCarriesValue(t *testing.T) {
	_, err := Parse(map[string]string{"port": "hunter2-secret"}, []Field{{Name: "port", Valid: Port, Want: "a valid port"}})
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("err = %v", err)
	}
}

func TestPort(t *testing.T) {
	for s, want := range map[string]bool{"1": true, "5432": true, "65535": true, "0": false, "65536": false, "": false, "-1": false, "80a": false, " 80": false, "+80": false} {
		if got := Port(s); got != want {
			t.Errorf("Port(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestOneOf(t *testing.T) {
	f := OneOf("a", "b")
	if !f("a") || !f("b") || f("c") || f("") || f("A") {
		t.Error("OneOf mismatch")
	}
	if OneOf()("") {
		t.Error("empty OneOf accepts")
	}
}

func TestClassify(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errConnRefused)}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"refused", refused, "connection refused"},
		{"refused wrapped", fmt.Errorf("dial: %w", refused), "connection refused"},
		{"dns", &net.DNSError{Err: "no such host", Name: "db.example", IsNotFound: true}, "host not found"},
		{"dns timeout", &net.DNSError{Err: "i/o timeout", Name: "x", IsTimeout: true}, "timed out"},
		{"dns wrapped", fmt.Errorf("x: %w", &net.DNSError{Err: "nope", Name: "x"}), "host not found"},
		{"deadline", os.ErrDeadlineExceeded, "timed out"},
		{"ctx deadline", fmt.Errorf("q: %w", context.DeadlineExceeded), "timed out"},
		{"op timeout", &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}, "timed out"},
		{"timeout beats net error", &net.OpError{Op: "dial", Net: "tcp", Err: timeoutErr{}}, "timed out"},
		{"record header", tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}, "TLS handshake failed"},
		{"cert verify", &tls.CertificateVerificationError{Err: errors.New("x")}, "TLS handshake failed"},
		{"unknown authority", x509.UnknownAuthorityError{}, "TLS handshake failed"},
		{"hostname", x509.HostnameError{Host: "secret-host"}, "TLS handshake failed"},
		{"cert invalid", x509.CertificateInvalidError{Reason: x509.Expired}, "TLS handshake failed"},
		{"alert", tls.AlertError(40), "TLS handshake failed"},
		{"tls wrapped", fmt.Errorf("a: %w", fmt.Errorf("b: %w", x509.UnknownAuthorityError{})), "TLS handshake failed"},
		{"generic net", &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")}, "connection failed"},
		{"other", errors.New("boom"), "connection failed"},
		{"nil", nil, "connection failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.err); got != tt.want {
				t.Errorf("Classify = %q, want %q", got, tt.want)
			}
		})
	}
}

// timeoutErr is a net.Error that reports Timeout.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return false }

func TestDown(t *testing.T) {
	obs := Down("postgres", "pg.up", []string{"pg.a", "pg.b"}, "connection refused")
	if len(obs) != 3 {
		t.Fatalf("len = %d: %+v", len(obs), obs)
	}
	if obs[0].Metric != "pg.up" || obs[0].Item != "" || obs[0].Value == nil || *obs[0].Value != 0 {
		t.Errorf("up = %+v", obs[0])
	}
	for i, m := range []string{"pg.a", "pg.b"} {
		o := obs[i+1]
		if o.Metric != m || o.Item != "" || o.Err != "postgres: connection refused" || o.Value != nil {
			t.Errorf("obs[%d] = %+v", i+1, o)
		}
	}
	if got := Down("x", "x.up", nil, "r"); len(got) != 1 {
		t.Errorf("no others: %+v", got)
	}
	var _ checks.Observation = obs[0]
}

func TestCounterRate(t *testing.T) {
	t0 := time.Unix(1000, 0)
	var c Counter
	if _, ok := c.Rate(t0, 100); ok {
		t.Fatal("first call reported a rate")
	}
	if r, ok := c.Rate(t0.Add(10*time.Second), 150); !ok || r != 5 {
		t.Fatalf("rate = %v, %v", r, ok)
	}
	if _, ok := c.Rate(t0.Add(20*time.Second), 10); ok {
		t.Fatal("reset reported a rate")
	}
	if r, ok := c.Rate(t0.Add(30*time.Second), 30); !ok || r != 2 {
		t.Fatalf("after reset: %v, %v", r, ok)
	}
	if _, ok := c.Rate(t0.Add(30*time.Second), 40); ok {
		t.Fatal("same time reported a rate")
	}
	if _, ok := c.Rate(t0.Add(25*time.Second), 50); ok {
		t.Fatal("time going back reported a rate")
	}
	// a rejected sample for time keeps the earlier baseline (30 s, 30)
	if r, ok := c.Rate(t0.Add(40*time.Second), 50); !ok || r != 2 {
		t.Fatalf("after rejected sample: %v, %v", r, ok)
	}
}

func TestTimeouts(t *testing.T) {
	if CollectTimeout != 10*time.Second || QueryTimeout != 5*time.Second {
		t.Error("timeouts changed")
	}
}
