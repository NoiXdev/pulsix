package http

import (
	"context"
	"net/http"
	"strings"

	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/assert"
)

// Item errors of the assertions beyond the JSON values' (json.go). None
// quotes the response.
const (
	errAssertInvalid = "the assertion is not valid"
	errAssertNoTLS   = "the response did not arrive over https"
	errAssertTooBusy = "the manager was too busy to read the response"
	errRegexTooMuch  = "the pattern takes too much work for this response"
)

// The labels of http.assertion: pass, or a failed entry's severity.
const verdictPass = "pass"

// assertEntry is one assertions entry: the item it reports on and the
// parsed assertion, or the problem that keeps it from being evaluated
// (only its item fails).
type assertEntry struct {
	name    string
	a       assert.Assertion
	problem string
}

// assertEntries reads assertions: at most assert.MaxEntries entries. An
// entry without a valid name, or repeating one, has no item of its own to
// report on and is left out. Params arrive validated, so an entry that
// does not parse here bypassed the descriptor.
func assertEntries(params map[string]any) []assertEntry {
	list, _ := params["assertions"].([]any)
	if len(list) > assert.MaxEntries {
		list = list[:assert.MaxEntries]
	}
	entries := make([]assertEntry, 0, len(list))
	seen := make(map[string]bool, len(list))
	for _, v := range list {
		s, _ := v.(string)
		name, _, ok := strings.Cut(s, "=")
		if !ok || seen[name] || !checkplugins.FormatSecretName.Valid(name) {
			continue
		}
		seen[name] = true
		a, err := assert.Parse(s)
		if err != nil {
			entries = append(entries, assertEntry{name: name, problem: errAssertInvalid})
			continue
		}
		entries = append(entries, assertEntry{name: name, a: a})
	}
	return entries
}

// keepsBody reports whether one of entries reads the kept body.
func keepsBody(entries []assertEntry) bool {
	for _, e := range entries {
		if e.problem == "" && e.a.Subject().KeepsBody() {
			return true
		}
	}
	return false
}

// countsBody reports whether one of entries is a size assertion, which
// reads the body past what is kept to count it.
func countsBody(entries []assertEntry) bool {
	for _, e := range entries {
		if e.problem == "" && e.a.Subject() == assert.SubjectSize {
			return true
		}
	}
	return false
}

// verdict is what an assertion came to: whether it held and what was
// seen, or why it could not be evaluated.
type verdict struct {
	holds  bool
	actual string
	err    string
}

func failed(msg string) verdict { return verdict{err: msg} }

// observations are the entry's two metrics for v.
func (e assertEntry) observations(v verdict) []probe.Observation {
	if v.err != "" {
		return failAssertion(e.name, v.err)
	}
	label := verdictPass
	if !v.holds {
		label = string(e.a.Severity())
	}
	actual := v.actual
	return []probe.Observation{
		{Metric: "http.assertion", Item: e.name, Label: &label},
		{Metric: "http.assertion_actual", Item: e.name, Label: &actual},
	}
}

// failAssertion is msg on both of an item's metrics.
func failAssertion(name, msg string) []probe.Observation {
	return []probe.Observation{
		{Metric: "http.assertion", Item: name, Err: msg},
		{Metric: "http.assertion_actual", Item: name, Err: msg},
	}
}

// failAssertions is msg on both metrics of every entry's item.
func failAssertions(entries []assertEntry, msg string) []probe.Observation {
	obs := make([]probe.Observation, 0, 2*len(entries))
	for _, e := range entries {
		obs = append(obs, failAssertion(e.name, msg)...)
	}
	return obs
}

// assertObservations evaluates each entry against r, an attempt that was
// up. html and json subjects parse the kept body once each, in a slot of
// gate, waiting until ctx ends; the other subjects need no slot.
func assertObservations(ctx context.Context, gate *JSONGate, entries []assertEntry, r *response) []probe.Observation {
	if len(entries) == 0 {
		return nil
	}
	verdicts := make([]verdict, len(entries))
	var parsed []int
	work := &regexWork{left: regexCheckBudget}
	j := judge{r: r, work: work}
	for i, e := range entries {
		switch {
		case e.problem != "":
			verdicts[i] = failed(e.problem)
		case parsesBody(e.a.Subject()):
			parsed = append(parsed, i)
		default:
			verdicts[i] = j.judge(e.a)
		}
	}
	if len(parsed) > 0 {
		judgeParsed(ctx, gate, entries, parsed, r.body, work, verdicts)
	}
	obs := make([]probe.Observation, 0, 2*len(entries))
	for i, e := range entries {
		obs = append(obs, e.observations(verdicts[i])...)
	}
	return obs
}

// parsesBody reports whether s parses the body as JSON or HTML.
func parsesBody(s assert.Subject) bool {
	return s == assert.SubjectJSON || s == assert.SubjectHTML || s == assert.SubjectHTMLCount
}

// judge evaluates the subjects read straight off a response; body is the
// kept body as text, converted once.
type judge struct {
	r       *response
	work    *regexWork
	body    string
	hasBody bool
}

func (j *judge) judge(a assert.Assertion) verdict {
	r := j.r
	switch a.Subject() {
	case assert.SubjectStatus:
		return number(a, float64(r.status))
	case assert.SubjectResponseMS:
		return number(a, probe.Millis(r.elapsed))
	case assert.SubjectTTFBMS:
		return number(a, probe.Millis(r.timing.ttfb))
	case assert.SubjectRedirects:
		return number(a, float64(r.redirects))
	case assert.SubjectSize:
		size, exact := r.contentBytes()
		if !exact {
			return failed(errSizeUnknown)
		}
		return number(a, float64(size))
	case assert.SubjectCertDays:
		if r.leaf == nil {
			return failed(errAssertNoTLS)
		}
		return number(a, r.leaf.NotAfter.Sub(r.at).Hours()/24)
	case assert.SubjectCertIssuer:
		if r.leaf == nil {
			return failed(errAssertNoTLS)
		}
		return j.work.text(a, probe.IssuerLabel(r.leaf), true)
	case assert.SubjectHeader:
		return j.judgeHeader(a, r.header)
	case assert.SubjectBody:
		return j.judgeBody(a)
	}
	return failed(errAssertInvalid)
}

// judgeHeader: the final response's values of the header, joined with
// ", "; a Set-Cookie value is judged but never shown.
func (j *judge) judgeHeader(a assert.Assertion, h http.Header) verdict {
	values := h.Values(a.Arg())
	v := j.work.text(a, strings.Join(values, ", "), len(values) > 0)
	if len(values) > 0 && strings.EqualFold(a.Arg(), "Set-Cookie") {
		v.actual = actualHidden
	}
	return v
}

// judgeBody: the kept first MiB as text. What is not there, and whether
// it equals something, only the whole body can tell: a negative op on a
// body past the cap or cut short, and == or != on one cut short (its
// length unknown), cannot be evaluated; contains and matches still pass
// when they find what they look for.
func (j *judge) judgeBody(a assert.Assertion) verdict {
	b := j.r.body
	cut := b.err != nil
	switch {
	case b.truncated && a.Op().Negative():
		return failed(errTooLarge)
	case cut && (a.Op().Negative() || a.Op() == assert.OpEq || a.Op() == assert.OpNe):
		return failed(errCutShort)
	}
	if !j.hasBody {
		j.body, j.hasBody = string(b.kept), true
	}
	return j.work.text(a, j.body, true)
}

// number judges a numeric subject's value v.
func number(a assert.Assertion, v float64) verdict {
	return verdict{holds: a.HoldsNumber(v), actual: numberActual(a.Subject(), v)}
}

// regexCheckBudget bounds a check's regular expression work, in program
// instructions times input bytes. The costliest program measured
// ([ab]{990}c, 993 instructions, over 1 MiB) took 6.2 ns per unit, so
// this is about a second of CPU: a 1 000-instruction pattern can read
// about 160 KB, while a common one (v[0-9]+, 5 instructions; (?i)error,
// 7) reads the whole 1 MiB body many times over.
const regexCheckBudget = 160_000_000

// regexWork is a check's budget for its regular expressions: Go's
// matcher costs up to the program's instructions times the input's bytes
// in one call that cannot be interrupted, so that product is charged
// before each match.
type regexWork struct {
	left int64
}

// text judges a text subject's value v, absent when !present.
func (w *regexWork) text(a assert.Assertion, v string, present bool) verdict {
	if !present {
		return verdict{holds: a.HoldsText("", false), actual: actualAbsent}
	}
	holds, problem := w.holds(a, v)
	if problem != "" {
		return failed(problem)
	}
	return verdict{holds: holds, actual: textActual(v)}
}

// holds is a.HoldsText(v, true), a regular expression's work charged
// first: past the budget it is errRegexTooMuch and nothing runs.
func (w *regexWork) holds(a assert.Assertion, v string) (bool, string) {
	if n := a.ProgramSize(); n > 0 {
		cost := int64(n) * int64(len(v)+1)
		if cost > w.left {
			return false, errRegexTooMuch
		}
		w.left -= cost
	}
	return a.HoldsText(v, true), ""
}
