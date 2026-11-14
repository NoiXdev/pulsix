package script

import (
	"bytes"
	"encoding/json"
	"math"
)

// The fixed outputs for JSON output the plugin refuses; none quotes the
// script's text.
const (
	reasonTooLarge    = "JSON output: more than 64 KiB"
	reasonInvalidJSON = "JSON output: not valid JSON"
	reasonNotObject   = "JSON output: not a JSON object"
	reasonNoStatus    = "JSON output: status is missing"
	reasonBadStatus   = "JSON output: status is not one of ok, warn, crit, unknown"
	reasonBadMessage  = "JSON output: message is not a string"
	reasonBadMetrics  = "JSON output: metrics is not an object"
	reasonBadMetric   = "JSON output: a metric is not a finite number"
)

// parseJSON reads the JSON format: one object with a required status, an
// optional message string and an optional metrics object of numbers
// (values in output order, through valueSet); other keys are ignored and
// the exit code does not count. Output the plugin refuses gives status
// unknown, one of the fixed reasons as message and no values. The message
// is returned uncleaned.
func parseJSON(command string, res runResult) (status, string, []itemValue) {
	if res.truncated {
		return statusUnknown, reasonTooLarge, nil
	}
	// json.Valid bounds nesting (encoding/json's own depth limit), and
	// refuses trailing data after the object.
	if !json.Valid(res.stdout) {
		return statusUnknown, reasonInvalidJSON, nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(res.stdout, &top); err != nil || top == nil {
		return statusUnknown, reasonNotObject, nil
	}

	raw, ok := top["status"]
	if !ok {
		return statusUnknown, reasonNoStatus, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return statusUnknown, reasonBadStatus, nil
	}
	st := status(s)
	switch st {
	case statusOK, statusWarn, statusCrit, statusUnknown:
	default:
		return statusUnknown, reasonBadStatus, nil
	}

	var message string
	if raw, ok := top["message"]; ok {
		if err := json.Unmarshal(raw, &message); err != nil {
			return statusUnknown, reasonBadMessage, nil
		}
	}

	vs := newValueSet(command)
	if raw, ok := top["metrics"]; ok {
		if reason := jsonMetrics(raw, vs); reason != "" {
			return statusUnknown, reason, nil
		}
	}
	return st, message, vs.values
}

// jsonMetrics reads the metrics object, already known to be valid JSON,
// into vs in output order. Every metric must be a finite number, also
// those past vs's bound. null is no metrics.
func jsonMetrics(raw json.RawMessage, vs *valueSet) string {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return reasonBadMetrics
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return reasonBadMetrics
		}
		key, _ := kt.(string)
		vt, err := dec.Token()
		if err != nil {
			return reasonBadMetrics
		}
		n, ok := vt.(json.Number)
		if !ok {
			return reasonBadMetric
		}
		v, err := n.Float64()
		if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
			return reasonBadMetric
		}
		vs.add(key, v)
	}
	return ""
}
