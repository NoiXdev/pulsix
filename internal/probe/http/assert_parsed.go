package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/noixdev/pulsix/internal/jpath"
	"github.com/noixdev/pulsix/internal/probe/assert"
	"golang.org/x/net/html"
)

// judgeParsed evaluates the json and html entries at the indexes parsed
// into verdicts. A body past the cap or cut short cannot be parsed whole,
// so each is an error; otherwise the body is parsed once per language in
// one slot of gate, each entry evaluated while ctx lasts.
func judgeParsed(ctx context.Context, gate *JSONGate, entries []assertEntry, parsed []int, body bodyRead, verdicts []verdict) {
	fail := func(msg string) {
		for _, i := range parsed {
			verdicts[i] = failed(msg)
		}
	}
	switch {
	case body.truncated:
		fail(errTooLarge)
		return
	case body.err != nil:
		fail(errCutShort)
		return
	}
	leave, ok := gate.enter(ctx)
	if !ok {
		fail(errAssertTooBusy)
		return
	}
	defer leave()
	docs := parsedDocs{body: body.kept, left: defaultJSONBudgets.perCheck, htmlLeft: htmlCheckBudget}
	for _, i := range parsed {
		if ctx.Err() != nil {
			verdicts[i] = failed(errOutOfTime)
			continue
		}
		a := entries[i].a
		if a.Subject() == assert.SubjectJSON {
			verdicts[i] = docs.judgeJSON(ctx, a)
		} else {
			verdicts[i] = docs.judgeHTML(ctx, a)
		}
	}
}

// parsedDocs parses the body as JSON and as HTML when first asked, and
// remembers the outcome; left is the JSONPath visits the check's json
// assertions still have, htmlLeft the node visits of its html ones.
type parsedDocs struct {
	body []byte

	jsonDone bool
	json     any
	jsonErr  string

	htmlDone bool
	html     *html.Node
	htmlErr  string

	left     int
	htmlLeft int64
}

// jsonDoc decodes the body as JSON once, as json_values does.
func (d *parsedDocs) jsonDoc() (any, string) {
	if !d.jsonDone {
		d.jsonDone = true
		switch doc, ok := decodeJSON(d.body); {
		case !ok:
			d.jsonErr = errNotJSON
		case tooDeep(d.body):
			d.jsonErr = errTooDeep
		default:
			d.json = doc
		}
	}
	return d.json, d.jsonErr
}

// judgeJSON: what the path selects. Nothing is absent; exists and
// !exists take any number of values, every other op exactly one. A
// string compares by its value, a number by its text, a boolean and null
// by their JSON spelling; an object or an array fails every op but
// exists.
func (d *parsedDocs) judgeJSON(ctx context.Context, a assert.Assertion) verdict {
	doc, problem := d.jsonDoc()
	if problem != "" {
		return failed(problem)
	}
	if d.left <= 0 {
		return failed(errTooMuch)
	}
	nodes, used, err := a.Path().Select(ctx, doc, min(defaultJSONBudgets.perPath, d.left))
	d.left -= used
	switch {
	case errors.Is(err, jpath.ErrTooMuch):
		return failed(errTooMuch)
	case err != nil:
		return failed(errOutOfTime)
	case len(nodes) == 0:
		return text(a, "", false)
	}
	exists := a.Op() == assert.OpExists || a.Op() == assert.OpNotExists
	if len(nodes) > 1 {
		if !exists {
			return failed(fmt.Sprintf(errManyFmt, len(nodes)))
		}
		return verdict{holds: a.HoldsText("", true), actual: fmt.Sprintf("%d values", len(nodes))}
	}
	value, actual, problem := jsonScalar(nodes[0])
	switch {
	case problem != "":
		return failed(problem)
	case value == nil:
		// An object or an array: there, but equal to nothing.
		return verdict{holds: a.Op() == assert.OpExists, actual: actual}
	}
	return verdict{holds: a.HoldsText(*value, true), actual: actual}
}

// jsonScalar is a selected node's text and its actual; an object or an
// array has no text (nil) and is named, a number past what the subset
// compares is a problem.
func jsonScalar(node any) (value *string, actual, problem string) {
	var s string
	switch v := node.(type) {
	case string:
		return &v, textActual(v), ""
	case json.Number:
		f, err := strconv.ParseFloat(string(v), 64)
		if len(v) > jpath.MaxNumberLen || err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, "", errOutOfRange
		}
		s = strconv.FormatFloat(f, 'f', -1, 64)
	case bool:
		s = strconv.FormatBool(v)
	case nil:
		s = "null"
	case map[string]any:
		return nil, "object", ""
	case []any:
		return nil, "array", ""
	default:
		// encoding/json decodes into nothing else.
		return nil, "", errNotJSON
	}
	return &s, shortActual(s), ""
}
