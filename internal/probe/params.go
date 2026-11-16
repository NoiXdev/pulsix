package probe

import (
	"context"
	"errors"
	"math"
	"net/netip"
	"strings"
	"time"
)

// Collection errors for an attempt that could not be made.
var (
	errNoAddress = errors.New("host has no address")
	errBadTarget = errors.New("target is not a valid address")
	// ErrBadPort is the collection error for a port param outside 1..65535.
	ErrBadPort = errors.New("invalid port")
)

// AddressPlaceholder in a name or URL parameter is replaced with the host's
// address.
const AddressPlaceholder = "{address}"

// Value is a numeric observation of metric.
func Value(metric string, v float64) Observation {
	return Observation{Metric: metric, Value: &v}
}

// BoolValue is a 1 or 0 observation of metric.
func BoolValue(metric string, b bool) Observation {
	if b {
		return Value(metric, 1)
	}
	return Value(metric, 0)
}

// Text is a discrete observation of metric, carrying label as it is: the
// caller cleans text a target sent (Label, ShortLabel).
func Text(metric, label string) Observation {
	return Observation{Metric: metric, Label: &label}
}

// Down is the whole result of a failed attempt: <plugin>.up = 0 and
// <plugin>.failure = reason, one of the Failure labels.
func Down(plugin, reason string) []Observation {
	return []Observation{Value(plugin+".up", 0), Text(plugin+".failure", reason)}
}

// Up is the failure reason of a successful attempt: <plugin>.failure =
// FailureNone.
func Up(plugin string) Observation {
	return Text(plugin+".failure", FailureNone)
}

// FailAll is the result of an attempt that could not be made: one error per
// metric.
func FailAll(msg string, metrics ...string) []Observation {
	out := make([]Observation, 0, len(metrics))
	for _, m := range metrics {
		out = append(out, Observation{Metric: m, Err: msg})
	}
	return out
}

// Millis is d in milliseconds.
func Millis(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

// NumParam reads a number param; JSON numbers arrive as float64.
func NumParam(params map[string]any, key string, def float64) float64 {
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

// PortParam reads the port param (or def) and reports whether it is a
// whole number in 1..65535.
func PortParam(params map[string]any, def float64) (int, bool) {
	port := NumParam(params, "port", def)
	if port < 1 || port > 65535 || port != math.Trunc(port) {
		return 0, false
	}
	return int(port), true
}

// BoolParam reads a bool param (or def).
func BoolParam(params map[string]any, key string, def bool) bool {
	if v, ok := params[key].(bool); ok {
		return v
	}
	return def
}

// StrParam reads a string param (or def).
func StrParam(params map[string]any, key, def string) string {
	if v, ok := params[key].(string); ok {
		return v
	}
	return def
}

// Bound derives the probe's context: timeout_s from params (or def), never
// later than ctx's own deadline.
func Bound(ctx context.Context, params map[string]any, def time.Duration) (context.Context, context.CancelFunc) {
	d := def
	if s := NumParam(params, "timeout_s", 0); s > 0 {
		d = time.Duration(s * float64(time.Second))
	}
	return context.WithTimeout(ctx, d)
}

// TargetHost turns a host's address into the host part of a dial target: a
// hostname, an IPv4 address or an unbracketed IPv6 address (brackets around
// an IPv6 literal are accepted and removed). Callers bracket it again with
// net.JoinHostPort.
func TargetHost(address string) (string, error) {
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
