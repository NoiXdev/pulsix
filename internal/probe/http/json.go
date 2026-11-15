package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/theory/jsonpath"

	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/probe"
)

// Bounds of json_values; the descriptor (internal/checkplugins) refuses
// more already, the probe never trusts a length blindly.
const (
	maxJSONValues  = 32
	maxJSONPathLen = 512
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
	errBadPath    = "the path is not a valid JSONPath"
)

// jsonEntry is one json_values entry: the item it reports on and its
// parsed path, nil when the path cannot be used (only its item fails).
type jsonEntry struct {
	name string
	path *jsonpath.Path
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
		e := jsonEntry{name: name}
		if len(path) <= maxJSONPathLen {
			if p, err := jsonpath.Parse(path); err == nil {
				e.path = p
			}
		}
		entries = append(entries, e)
	}
	return entries
}

// jsonObservations reads each entry from body; over marks a body that went
// on past the cap. A body that is not one JSON value, or is cut, is the
// same error on every entry.
func jsonObservations(entries []jsonEntry, body []byte, over bool) []probe.Observation {
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
	if over {
		return fail(errTooLarge)
	}
	doc, ok := decodeJSON(body)
	if !ok {
		return fail(errNotJSON)
	}
	obs := make([]probe.Observation, len(entries))
	for i, e := range entries {
		if e.path == nil {
			obs[i] = jsonError(e.name, errBadPath)
			continue
		}
		obs[i] = jsonValue(e.name, e.path.Select(doc))
	}
	return obs
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
func jsonValue(name string, nodes jsonpath.NodeList) probe.Observation {
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
