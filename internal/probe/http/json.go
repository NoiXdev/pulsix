package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/jpath"
	"github.com/noixdev/pulsix/internal/probe"
)

// Bounds of json_values; the descriptor (internal/checkplugins) refuses
// more already, the probe never trusts a length blindly.
const (
	maxJSONValues  = 32
	maxJSONPathLen = 512
	// maxJSONDepth bounds how deeply a document may nest: a descendant
	// segment's work grows with the depth of a document the target
	// writes.
	maxJSONDepth = 64
	// jsonPathBudget bounds the nodes one path's evaluation visits. A
	// 1 MiB body holds about 525 000 values; the costliest path of the
	// subset a real API needs, a filter under the descendant segment,
	// visits each about three times.
	jsonPathBudget = 2_000_000
)

// Item errors of the JSON values. None quotes the response.
const (
	errNotJSON    = "the response is not JSON"
	errTooLarge   = "the response is larger than 1 MiB"
	errNoMatch    = "the path matched nothing"
	errManyFmt    = "the path matched %d values"
	errObject     = "the path matched an object"
	errList       = "the path matched a list"
	errNull       = "the path matched null"
	errOutOfRange = "the number is out of range"
	errNotAllowed = "the path is not allowed"
	errTooMuch    = "the path selected too much"
	errTooDeep    = "the response is nested too deeply"
	errCutShort   = "the response was cut short"
)

// jsonEntry is one json_values entry: the item it reports on and its
// parsed path, or the problem that keeps the path from running (only its
// item fails).
type jsonEntry struct {
	name    string
	path    *jpath.Path
	problem string
}

// jsonEntries reads json_values: at most maxJSONValues entries, each
// name=path. An entry without a valid name, or repeating one, has no item
// of its own to report on and is left out.
func jsonEntries(params map[string]any) []jsonEntry {
	list, _ := params["json_values"].([]any)
	if len(list) > maxJSONValues {
		list = list[:maxJSONValues]
	}
	entries := make([]jsonEntry, 0, len(list))
	seen := make(map[string]bool, len(list))
	for _, v := range list {
		s, _ := v.(string)
		name, path, ok := strings.Cut(s, "=")
		if !ok || seen[name] || !checkplugins.FormatSecretName.Valid(name) {
			continue
		}
		seen[name] = true
		entries = append(entries, parseEntry(name, path))
	}
	return entries
}

// parseEntry parses an entry's path with the subset's parser. Params
// arrive validated, so a refusal here means they bypassed the descriptor.
func parseEntry(name, path string) jsonEntry {
	if len(path) > maxJSONPathLen {
		return jsonEntry{name: name, problem: errNotAllowed}
	}
	p, err := jpath.Parse(path)
	if err != nil {
		return jsonEntry{name: name, problem: errNotAllowed}
	}
	return jsonEntry{name: name, path: p}
}

// jsonObservations reads each entry from body; over marks a body that went
// on past the cap, cut one whose read broke off. A body that is not one
// JSON value of at most maxJSONDepth levels, or is incomplete, is the
// same error on every entry.
func jsonObservations(entries []jsonEntry, body []byte, over, cut bool) []probe.Observation {
	if len(entries) == 0 {
		return nil
	}
	fail := func(msg string) []probe.Observation {
		obs := make([]probe.Observation, len(entries))
		for i, e := range entries {
			obs[i] = jsonError(e.name, msg)
		}
		return obs
	}
	switch {
	case over:
		return fail(errTooLarge)
	case cut:
		return fail(errCutShort)
	}
	// Decoding checks the syntax (encoding/json bounds the depth at
	// 10 000); tooDeep then scans the valid body for the probe's bound.
	doc, ok := decodeJSON(body)
	if !ok {
		return fail(errNotJSON)
	}
	if tooDeep(body) {
		return fail(errTooDeep)
	}
	obs := make([]probe.Observation, len(entries))
	for i, e := range entries {
		if e.path == nil {
			obs[i] = jsonError(e.name, e.problem)
			continue
		}
		nodes, err := e.path.Select(doc, jsonPathBudget)
		if err != nil {
			obs[i] = jsonError(e.name, errTooMuch)
			continue
		}
		obs[i] = jsonValue(e.name, nodes)
	}
	return obs
}

// tooDeep reports whether valid JSON nests more than maxJSONDepth arrays
// and objects; brackets inside strings do not count.
func tooDeep(body []byte) bool {
	depth := 0
	inString, escaped := false, false
	for _, c := range body {
		switch {
		case escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case inString:
			inString = c != '"'
		case c == '"':
			inString = true
		case c == '[' || c == '{':
			depth++
			if depth > maxJSONDepth {
				return true
			}
		case c == ']' || c == '}':
			depth--
		}
	}
	return false
}

// decodeJSON decodes body as exactly one JSON value, numbers as
// json.Number so a large integer keeps its digits until it becomes a
// float64.
func decodeJSON(body []byte) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return doc, true
}

// jsonValue turns what a path selected into the entry's observation: a
// number is http.json.value, a string or a boolean http.json.text, and
// anything but exactly one scalar an error on http.json.value.
func jsonValue(name string, nodes []any) probe.Observation {
	if len(nodes) == 0 {
		return jsonError(name, errNoMatch)
	}
	if len(nodes) > 1 {
		return jsonError(name, fmt.Sprintf(errManyFmt, len(nodes)))
	}
	switch v := nodes[0].(type) {
	case json.Number:
		f, err := strconv.ParseFloat(string(v), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return jsonError(name, errOutOfRange)
		}
		return probe.Observation{Metric: "http.json.value", Item: name, Value: &f}
	case string:
		return jsonText(name, probe.Label(v))
	case bool:
		return jsonText(name, strconv.FormatBool(v))
	case map[string]any:
		return jsonError(name, errObject)
	case []any:
		return jsonError(name, errList)
	case nil:
		return jsonError(name, errNull)
	}
	// encoding/json decodes into nothing else.
	return jsonError(name, errNotJSON)
}

func jsonText(name, label string) probe.Observation {
	return probe.Observation{Metric: "http.json.text", Item: name, Label: &label}
}

// jsonError is msg on the entry's item. The error sits on http.json.value,
// as snmp puts an oids entry's error on snmp.value.
func jsonError(name, msg string) probe.Observation {
	return probe.Observation{Metric: "http.json.value", Item: name, Err: msg}
}
