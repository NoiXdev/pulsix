// Package probe runs synthetic checks from the manager: one connection
// attempt per call against a host's address, reported as observations. It
// knows nothing of check definitions or the database; the params it receives
// are already validated by internal/checks.
package probe

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"time"
)

// Observation is one metric of one probe: a Value, or an Err when the
// attempt could not be made at all. Never both.
type Observation struct {
	Metric string
	Value  *float64
	Err    string
}

// Collection errors for an attempt that could not be made.
var (
	errNoAddress = errors.New("host has no address")
	errBadTarget = errors.New("target is not a valid address")
	// errNameIsAddress: a dns A or AAAA check of an IP literal asks no
	// server, so it cannot be attempted.
	errNameIsAddress = errors.New("name is an address")
)

// addressPlaceholder in a name or URL parameter is replaced with the host's
// address.
const addressPlaceholder = "{address}"

// Run probes address with plugin and params, already validated by
// internal/checks. It never returns a Go error: every outcome is in the
// observations. A failed attempt (refused, timed out, no answer) is
// `<plugin>.up = 0` alone, except that icmp also reports a measured
// `icmp.loss_pct = 100` when its requests went out and none came back. An
// attempt that could not be made is one error observation per metric the
// params would have produced.
//
// The probe's timeout is its params' timeout_s (or the plugin's default),
// cut to ctx's deadline when that is earlier.
func Run(ctx context.Context, g *Guard, plugin string, params map[string]any, address string) []Observation {
	if g == nil {
		g = NewGuard(nil)
	}
	switch plugin {
	case "tcp":
		return probeTCP(ctx, g, params, address)
	case "icmp":
		return probeICMP(ctx, g, params, address, listenICMP)
	case "dns":
		return probeDNS(ctx, g, params, address, g.Dialer)
	}
	return failAll("unknown probe plugin "+plugin, plugin+".up")
}

func value(metric string, v float64) Observation {
	return Observation{Metric: metric, Value: &v}
}

func boolValue(metric string, b bool) Observation {
	if b {
		return value(metric, 1)
	}
	return value(metric, 0)
}

// down is the whole result of a failed attempt.
func down(plugin string) []Observation {
	return []Observation{value(plugin+".up", 0)}
}

// failAll is the result of an attempt that could not be made: one error per
// metric.
func failAll(msg string, metrics ...string) []Observation {
	out := make([]Observation, 0, len(metrics))
	for _, m := range metrics {
		out = append(out, Observation{Metric: m, Err: msg})
	}
	return out
}

func millis(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

// numParam reads a number param; JSON numbers arrive as float64.
func numParam(params map[string]any, key string, def float64) float64 {
	switch v := params[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return def
}

func strParam(params map[string]any, key, def string) string {
	if v, ok := params[key].(string); ok {
		return v
	}
	return def
}

// bound derives the probe's context: timeout_s from params (or def), never
// later than ctx's own deadline.
func bound(ctx context.Context, params map[string]any, def time.Duration) (context.Context, context.CancelFunc) {
	d := def
	if s := numParam(params, "timeout_s", 0); s > 0 {
		d = time.Duration(s * float64(time.Second))
	}
	return context.WithTimeout(ctx, d)
}

// targetHost turns a host's address into the host part of a dial target: a
// hostname, an IPv4 address or an unbracketed IPv6 address (brackets around
// an IPv6 literal are accepted and removed). Callers bracket it again with
// net.JoinHostPort.
func targetHost(address string) (string, error) {
	a := strings.TrimSpace(address)
	if a == "" {
		return "", errNoAddress
	}
	if strings.HasPrefix(a, "[") && strings.HasSuffix(a, "]") {
		inner := a[1 : len(a)-1]
		if ip, err := netip.ParseAddr(inner); err != nil || !ip.Is6() {
			return "", errBadTarget
		}
		return inner, nil
	}
	if _, err := netip.ParseAddr(a); err == nil {
		return a, nil
	}
	if !validHostname(a) {
		return "", errBadTarget
	}
	return a, nil
}

// validHostname accepts letters, digits, '-', '_' and '.', at most 253 bytes.
func validHostname(s string) bool {
	if len(s) > 253 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}
