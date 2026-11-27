// Owner decision 4 of backlog 3.132: a check value, a check error, a probe
// result or a fact can carry a secret's value — a database error that
// repeats its connection string, say. Every current agent.yaml secret
// value of secrets.RedactMinLen bytes or more (of the fields
// secrets.IsSecretField names) is replaced by secrets.Redacted at the
// sinks, before a result is buffered or sent, so the manager and the local
// status page see the same text and the value never leaves the machine.
// Check keys and metric names are definitions and are left as they are.
//
// The placeholder can be longer than the value it replaces (a 4-byte value
// becomes 8 bytes), so a redacted text can outgrow the bound its plugin
// held it to. The manager refuses a whole batch, and ends the stream, for
// one item or label over its bound, so such a result is dropped and
// logged (redactWarn); an error text and a fact value are cut back to
// their bounds instead. A text its producer cut at its bound may end in
// the start of a value the cut went through; RedactTail removes it.
package agent

import (
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/facts"
	"github.com/noixdev/pulsix/internal/agent/probes"
	"github.com/noixdev/pulsix/internal/agent/secrets"
	"github.com/noixdev/pulsix/internal/probe"
)

// maxErrLen is the manager's bound on an error text
// (internal/checks.MaxErrLen), which the plugins cut errors at.
const maxErrLen = 2048

// redactor is the watcher's current redactor (nil: nothing to replace).
func (l *Loop) redactor() *secrets.Redactor { return l.secrets.Redactor() }

// redactBounded redacts s, which its producer held to bound bytes: a text
// at its bound may have been cut, so the start of a value is stripped
// too.
func redactBounded(r *secrets.Redactor, s string, bound int) string {
	if len(s) > bound-utf8.UTFMax {
		return r.RedactTail(s)
	}
	return r.Redact(s)
}

// cutUTF8 cuts s to at most n bytes on a rune boundary.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func redactErr(r *secrets.Redactor, s string) string {
	return cutUTF8(redactBounded(r, s, maxErrLen), maxErrLen)
}

// redactResults returns results with item, label and error redacted, as a
// copy (the caller's slice is the scheduler's), leaving out a result whose
// item or label redaction pushed past its bound.
func (l *Loop) redactResults(results []checks.Result) []checks.Result {
	r := l.redactor()
	if r == nil {
		return results
	}
	out := make([]checks.Result, 0, len(results))
	for _, x := range results {
		x.Item, x.Err, x.Label = r.Redact(x.Item), redactErr(r, x.Err), redactPtr(r, x.Label)
		if len(x.Item) > checks.MaxItemLen || (x.Label != nil && len(*x.Label) > checks.MaxLabelLen) {
			l.noteRedactDrop("check results", x.CheckKey, x.Metric)
			continue
		}
		out = append(out, x)
	}
	return out
}

// redactProbeResults is redactResults for probe results.
func (l *Loop) redactProbeResults(results []probes.Result) []probes.Result {
	r := l.redactor()
	if r == nil {
		return results
	}
	out := make([]probes.Result, 0, len(results))
	for _, x := range results {
		x.Item, x.Err, x.Label = r.Redact(x.Item), redactErr(r, x.Err), redactPtr(r, x.Label)
		if len(x.Item) > probe.MaxItemLen || (x.Label != nil && len(*x.Label) > probe.MaxLabelLen) {
			l.noteRedactDrop("probe results", x.CheckKey, x.Metric)
			continue
		}
		out = append(out, x)
	}
	return out
}

// noteRedactDrop logs a dropped result at most once per
// invalidUTF8WarnEvery, with how many the line covers.
func (l *Loop) noteRedactDrop(kind, key, metric string) {
	if covers, due := l.redactWarn.note(l.now()); due {
		l.log.Warn(kind+" dropped: an item or label is too long once secret values were replaced",
			"dropped", covers, "check_key", key, "metric", metric)
	}
}

// redactFacts returns facts with every value redacted and held to the
// facts' bound, as a copy.
func redactFacts(r *secrets.Redactor, values map[string]string) map[string]string {
	if r == nil || values == nil {
		return values
	}
	out := make(map[string]string, len(values))
	for k, v := range values {
		out[k] = cutUTF8(redactBounded(r, v, facts.MaxValueLen), facts.MaxValueLen)
	}
	return out
}

func redactPtr(r *secrets.Redactor, s *string) *string {
	if s == nil {
		return nil
	}
	v := r.Redact(*s)
	return &v
}
