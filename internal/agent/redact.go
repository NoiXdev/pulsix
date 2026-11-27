// Owner decision 4 of backlog 3.132: a check value, a check error, a probe
// result or a fact can carry a secret's value — a database error that
// repeats its connection string, say. Every current agent.yaml secret
// value of secrets.RedactMinLen bytes or more is replaced by
// secrets.Redacted at the sinks, before a result is buffered or sent, so
// the manager and the local status page see the same text and the value
// never leaves the machine. Check keys and metric names are definitions
// and are left as they are.
package agent

import (
	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/probes"
	"github.com/noixdev/pulsix/internal/agent/secrets"
)

// redactor is the watcher's current redactor (nil: nothing to replace).
func (l *Loop) redactor() *secrets.Redactor { return l.secrets.Redactor() }

// redactResults returns results with item, label and error redacted, as a
// copy: the caller's slice is the scheduler's.
func redactResults(r *secrets.Redactor, results []checks.Result) []checks.Result {
	if r == nil {
		return results
	}
	out := make([]checks.Result, len(results))
	for i, x := range results {
		x.Item, x.Err, x.Label = r.Redact(x.Item), r.Redact(x.Err), redactPtr(r, x.Label)
		out[i] = x
	}
	return out
}

// redactProbeResults is redactResults for probe results.
func redactProbeResults(r *secrets.Redactor, results []probes.Result) []probes.Result {
	if r == nil {
		return results
	}
	out := make([]probes.Result, len(results))
	for i, x := range results {
		x.Item, x.Err, x.Label = r.Redact(x.Item), r.Redact(x.Err), redactPtr(r, x.Label)
		out[i] = x
	}
	return out
}

// redactFacts returns facts with every value redacted, as a copy.
func redactFacts(r *secrets.Redactor, facts map[string]string) map[string]string {
	if r == nil || facts == nil {
		return facts
	}
	out := make(map[string]string, len(facts))
	for k, v := range facts {
		out[k] = r.Redact(v)
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
