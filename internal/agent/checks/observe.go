package checks

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValueObservation builds a numeric Observation; v is copied so the
// caller's loop variable is never aliased into the returned slice.
func ValueObservation(item, metric string, v float64) Observation {
	return Observation{Item: item, Metric: metric, Value: &v}
}

// LabelObservation builds a label Observation (service.state today).
func LabelObservation(item, metric, label string) Observation {
	return Observation{Item: item, Metric: metric, Label: &label}
}

// maxErrLen is the manager's bound on an observation's error text
// (internal/checks.MaxErrLen; a test keeps the two equal). The manager cuts
// a longer one itself (backlog 7.110); the agent cuts first so a fault that
// quotes a long mount point never travels the wire at full length.
const maxErrLen = 2048

// ErrObservation builds an error Observation: collection failed for this
// exact item/metric, so no sample should be stored and the manager's
// current value shows the failure text instead of a stale reading. The
// text is cut at maxErrLen on a UTF-8 boundary.
func ErrObservation(item, metric string, err error) Observation {
	return Observation{Item: item, Metric: metric, Err: cutErrText(err.Error())}
}

// cutErrText cuts s to maxErrLen bytes, dropping a rune that would
// straddle the bound rather than splitting it.
func cutErrText(s string) string {
	if len(s) <= maxErrLen {
		return s
	}
	n := maxErrLen
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// ParamStrings reads params[key] as a list of strings. present is false
// only when the key is absent or nil — the caller decides what "absent"
// means (disk: watch every non-pseudo mount; service: nothing to check).
// Both shapes a caller can reasonably hand in are accepted: []string, built
// directly (tests, and internal/checks' own in-process callers), and
// []any with string elements, the shape a JSON/JSONB round trip produces
// (internal/checks.Resolved.Params after a store read) — anything else is
// a plugin-level error, since the manager's own validation
// (internal/checks.validateParams) means this plugin should never actually
// see one.
func ParamStrings(params map[string]any, key string) (values []string, present bool, err error) {
	v, ok := params[key]
	if !ok || v == nil {
		return nil, false, nil
	}
	switch vv := v.(type) {
	case []string:
		return append([]string(nil), vv...), true, nil
	case []any:
		out := make([]string, len(vv))
		for i, e := range vv {
			s, ok := e.(string)
			if !ok {
				return nil, true, fmt.Errorf("param %q: element %d is not a string", key, i)
			}
			out[i] = s
		}
		return out, true, nil
	default:
		return nil, true, fmt.Errorf("param %q: must be a list of strings", key)
	}
}

// MaxItemLen is the manager's bound on an observation's item
// (internal/checks.MaxItemLen; a test keeps the two equal).
const MaxItemLen = 512

// ReportableItem reports whether the manager's check-results contract
// accepts item: at most MaxItemLen bytes, valid UTF-8, and no control
// character or line separator (internal/checks.withinContract). The
// manager refuses a whole batch, and ends the stream, for one item that
// breaks it (backlog 7.40), and a mount point is a path whoever can mount
// something chooses, FUSE through a setuid helper included; one the agent
// cannot name inside the contract is left out rather than cost every other
// mount its report.
//
// Valid UTF-8 is not the manager's rule alone (backlog 7.52): the item is
// a proto3 string, and gRPC refuses to encode one that is not, on this
// side, before anything reaches the wire. gopsutil hands such a path over
// raw — it unescapes a mountinfo field through strconv.Unquote and returns
// the input unchanged when that fails, which a '"' in the path makes it do.
func ReportableItem(item string) bool {
	return len(item) <= MaxItemLen && utf8.ValidString(item) && strings.IndexFunc(item, func(r rune) bool {
		return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
	}) < 0
}

// MaxLabelLen is the manager's bound on an observation's label
// (internal/checks.MaxLabelLen; a test keeps the two equal).
const MaxLabelLen = 512

// ReportableLabel reports whether the check-results contract accepts label:
// at most MaxLabelLen bytes and no NUL byte (internal/checks.withinContract,
// backlog 7.110 and 7.115), and valid UTF-8, because the label is a proto3
// string gRPC refuses to encode otherwise (backlog 7.52). The built-in
// plugins name their labels from fixed sets; this guards one that relays a
// string someone else chose (docker's state and health).
func ReportableLabel(label string) bool {
	return len(label) <= MaxLabelLen && utf8.ValidString(label) && !strings.ContainsRune(label, 0)
}
