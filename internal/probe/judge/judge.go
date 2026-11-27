// Package judge evaluates a synthetic check's assertions (backlog 3.13b,
// 3.87) the same way for every probe: it reads the assertions parameter
// in the plugin's dialect (internal/probe/assert), charges regular
// expressions to a per-check budget, formats what was seen and turns each
// entry's verdict into its two observations, <plugin>.assertion (pass, or
// the entry's severity) and <plugin>.assertion_actual, on the entry's
// item. A probe only says, per assertion, what it measured.
//
// The http probe keeps its own evaluation (its body, JSON and HTML
// subjects need the response gate) and shares only the actual's
// formatting.
package judge

import (
	"strings"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/assert"
)

// Item errors. None quotes what the target sent.
const (
	// ErrInvalid: the entry does not parse in the plugin's dialect; the
	// descriptor refuses such an entry, so it bypassed validation or was
	// written for a newer grammar.
	ErrInvalid = "the assertion is not valid"
	// ErrRegexTooMuch: the check's regular-expression budget is spent.
	ErrRegexTooMuch = "the pattern takes too much work for this response"
	// ErrUnevaluated: the vantage point whose round the check reports did
	// not evaluate the assertion — an agent from before the plugin had
	// assertions (Unevaluated).
	ErrUnevaluated = "the vantage point that answered did not evaluate this assertion; update its agent"
)

// VerdictPass is <plugin>.assertion of an entry that held; a failed one
// reports its severity.
const VerdictPass = "pass"

// Entry is one assertions entry: the item it reports on and the parsed
// assertion, or the problem that keeps it from being evaluated (only its
// item fails).
type Entry struct {
	Name    string
	A       assert.Assertion
	Problem string
}

// Entries reads params' assertions in dialect d: at most assert.MaxEntries
// entries. An entry without a valid name, or repeating one, has no item of
// its own to report on and is left out.
func Entries(d assert.Dialect, params map[string]any) []Entry {
	list := stringList(params["assertions"])
	if len(list) > assert.MaxEntries {
		list = list[:assert.MaxEntries]
	}
	entries := make([]Entry, 0, len(list))
	seen := make(map[string]bool, len(list))
	for _, s := range list {
		name, _, ok := strings.Cut(s, "=")
		if !ok || seen[name] || !assert.ValidName(name) {
			continue
		}
		seen[name] = true
		a, err := d.Parse(s)
		if err != nil {
			entries = append(entries, Entry{Name: name, Problem: ErrInvalid})
			continue
		}
		entries = append(entries, Entry{Name: name, A: a})
	}
	return entries
}

// Uses reports whether one of entries that parses looks at one of
// subjects.
func Uses(entries []Entry, subjects ...assert.Subject) bool {
	for _, e := range entries {
		if e.Problem == "" {
			for _, s := range subjects {
				if e.A.Subject() == s {
					return true
				}
			}
		}
	}
	return false
}

// stringList reads a list param: []any as decoded from JSON, or []string
// from a Go caller; a non-string entry is skipped.
func stringList(v any) []string {
	switch list := v.(type) {
	case []string:
		return list
	case []any:
		out := make([]string, 0, len(list))
		for _, e := range list {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// Verdict is what an assertion came to: whether it held and what was
// seen, or why it could not be evaluated.
type Verdict struct {
	Holds  bool
	Actual string
	Err    string
}

// Failed is an assertion that could not be evaluated, for msg.
func Failed(msg string) Verdict { return Verdict{Err: msg} }

// Number judges a numeric subject's value v.
func Number(a assert.Assertion, v float64) Verdict {
	return Verdict{Holds: a.HoldsNumber(v), Actual: NumberActual(a.Subject(), v)}
}

// Absent judges a numeric subject the attempt has no value for: it fails
// whatever the operator (a time limit must not pass when nothing came).
func Absent(assert.Assertion) Verdict { return Verdict{Actual: ActualAbsent} }

// Observe evaluates each entry with judge, which says what one parsed
// assertion came to, and returns the entries' observations in order; an
// entry with a problem reports it without being judged. All of one
// check's regular expressions share one budget.
func Observe(plugin string, entries []Entry, judge func(a assert.Assertion, w *Work) Verdict) []probe.Observation {
	if len(entries) == 0 {
		return nil
	}
	w := NewWork()
	obs := make([]probe.Observation, 0, 2*len(entries))
	for _, e := range entries {
		v := Failed(e.Problem)
		if e.Problem == "" {
			v = judge(e.A, w)
		}
		obs = append(obs, e.observations(plugin, v)...)
	}
	return obs
}

// observations are the entry's two metrics for v.
func (e Entry) observations(plugin string, v Verdict) []probe.Observation {
	if v.Err != "" {
		return failEntry(plugin, e.Name, v.Err)
	}
	label := VerdictPass
	if !v.Holds {
		label = string(e.A.Severity())
	}
	actual := v.Actual
	return []probe.Observation{
		{Metric: plugin + ".assertion", Item: e.Name, Label: &label},
		{Metric: plugin + ".assertion_actual", Item: e.Name, Label: &actual},
	}
}

// failEntry is msg on both of an item's metrics.
func failEntry(plugin, name, msg string) []probe.Observation {
	return []probe.Observation{
		{Metric: plugin + ".assertion", Item: name, Err: msg},
		{Metric: plugin + ".assertion_actual", Item: name, Err: msg},
	}
}

// FailAll is msg on both metrics of every entry's item: the assertions'
// share of an attempt that could not be made.
func FailAll(plugin string, entries []Entry, msg string) []probe.Observation {
	obs := make([]probe.Observation, 0, 2*len(entries))
	for _, e := range entries {
		obs = append(obs, failEntry(plugin, e.Name, msg)...)
	}
	return obs
}

// Unevaluated is ErrUnevaluated on both metrics of every assertion entry
// of a check of plugin with params that obs, an up round (<plugin>.up 1),
// reports nothing on. A probe that knows the plugin's assertions reports
// every entry of an up round, as a verdict or an error, so a missing one
// means the round came from a prober that ignores the parameter: an agent
// older than the plugin's assertions (backlog 3.87). Without this the
// items would go stale and then vanish, as if the operator had removed
// them. A down round, a round without an up reading and a plugin without
// assertions get nothing. The probe runner calls it only for rounds that
// can be blamed (internal/probes, unevaluated): probed after the check's
// assertions last changed, and the second of two such rounds.
func Unevaluated(plugin string, params map[string]any, obs []probe.Observation) []probe.Observation {
	d, ok := assert.DialectOf(plugin)
	if !ok || !upRound(plugin, obs) {
		return nil
	}
	reported := map[string]bool{}
	for _, o := range obs {
		if o.Metric == plugin+".assertion" {
			reported[o.Item] = true
		}
	}
	var out []probe.Observation
	for _, e := range Entries(d, params) {
		if !reported[e.Name] {
			out = append(out, failEntry(plugin, e.Name, ErrUnevaluated)...)
		}
	}
	return out
}

// upRound reports whether obs holds <plugin>.up = 1 on the whole-host
// item.
func upRound(plugin string, obs []probe.Observation) bool {
	for _, o := range obs {
		if o.Item == "" && o.Metric == plugin+".up" && o.Err == "" && o.Value != nil && *o.Value == 1 {
			return true
		}
	}
	return false
}
