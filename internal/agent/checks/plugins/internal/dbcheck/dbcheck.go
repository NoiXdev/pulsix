// Package dbcheck holds what the database check plugins (postgres, mysql,
// redis) share: secret field parsing, network-level error classification,
// the "server down" observation set and a counter-to-rate helper. It is
// internal to plugins/, so only the plugin packages can import it.
package dbcheck

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

const (
	// CollectTimeout bounds one whole collection.
	CollectTimeout = 10 * time.Second
	// QueryTimeout bounds one query on the server side where supported.
	QueryTimeout = 5 * time.Second
)

// Field describes one secret field a plugin understands. An empty value is
// treated as absent.
type Field struct {
	Name     string
	Default  string
	Required bool
	// Valid, when set, checks a non-empty value; Want completes the error
	// "secret field `<name>` is not <Want>".
	Valid func(string) bool
	Want  string
}

// Parse validates fields against spec and returns the values with defaults
// applied. Errors name the field, never its value.
func Parse(fields map[string]string, spec []Field) (map[string]string, error) {
	known := make(map[string]struct{}, len(spec))
	out := make(map[string]string, len(spec))
	for _, f := range spec {
		known[f.Name] = struct{}{}
		v := fields[f.Name]
		if v == "" {
			if f.Required {
				return nil, errors.New("secret field `" + f.Name + "` is required")
			}
			v = f.Default
		} else if f.Valid != nil && !f.Valid(v) {
			return nil, errors.New("secret field `" + f.Name + "` is not " + f.Want)
		}
		out[f.Name] = v
	}
	for name := range fields {
		if _, ok := known[name]; !ok {
			return nil, errors.New("secret field `" + name + "` is not known")
		}
	}
	return out, nil
}

// Port reports whether s is a decimal TCP port, 1 to 65535.
func Port(s string) bool {
	if s == "" || s[0] < '0' || s[0] > '9' {
		return false
	}
	n, err := strconv.ParseUint(s, 10, 16)
	return err == nil && n >= 1
}

// OneOf returns a validator accepting exactly the given values.
func OneOf(opts ...string) func(string) bool {
	return func(s string) bool {
		for _, o := range opts {
			if s == o {
				return true
			}
		}
		return false
	}
}

// Classify maps a network-layer error to one fixed text. It never returns
// any part of err, which may name a host or user. Plugins classify their
// own driver errors (authentication, permissions) before calling it.
func Classify(err error) string {
	if err == nil {
		return "connection failed"
	}
	var ne net.Error
	if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &ne) && ne.Timeout()) {
		return "timed out"
	}
	if isRefused(err) {
		return "connection refused"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "host not found"
	}
	if isTLS(err) {
		return "TLS handshake failed"
	}
	return "connection failed"
}

func isTLS(err error) bool {
	var (
		rec    tls.RecordHeaderError
		verify *tls.CertificateVerificationError
		auth   x509.UnknownAuthorityError
		host   x509.HostnameError
		inv    x509.CertificateInvalidError
		alert  tls.AlertError
	)
	return errors.As(err, &rec) || errors.As(err, &verify) || errors.As(err, &auth) ||
		errors.As(err, &host) || errors.As(err, &inv) || errors.As(err, &alert)
}

// Down is the observation set of an unreachable server: up = 0 and one
// error observation "<prefix>: <reason>" on each of others, all on item "".
func Down(prefix, up string, others []string, reason string) []checks.Observation {
	obs := make([]checks.Observation, 0, 1+len(others))
	obs = append(obs, checks.ValueObservation("", up, 0))
	for _, m := range others {
		obs = append(obs, checks.ErrObservation("", m, errors.New(prefix+": "+reason)))
	}
	return obs
}

// Counter turns a monotonically increasing server counter into a rate.
type Counter struct {
	at   time.Time
	v    uint64
	have bool
}

// Rate records the sample and returns the per-second increase since the
// previous one. ok is false on the first sample, when time did not advance
// (the earlier baseline is kept) and when v fell below the previous value
// (a server restart; v becomes the new baseline).
func (c *Counter) Rate(at time.Time, v uint64) (float64, bool) {
	if !c.have {
		c.at, c.v, c.have = at, v, true
		return 0, false
	}
	dt := at.Sub(c.at).Seconds()
	if dt <= 0 {
		return 0, false
	}
	prev := c.v
	c.at, c.v = at, v
	if v < prev {
		return 0, false
	}
	return float64(v-prev) / dt, true
}
