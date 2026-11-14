package script

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseJSON(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		status  status
		message string
		values  []itemValue
	}{
		{
			name:   "valid",
			out:    `{"status": "ok", "message": "3 backups, newest 2 h old", "metrics": {"age_hours": 2, "count": 3}}`,
			status: statusOK, message: "3 backups, newest 2 h old",
			values: []itemValue{{"c:age_hours", 2}, {"c:count", 3}},
		},
		{name: "each status", out: `{"status":"warn"}`, status: statusWarn},
		{name: "crit", out: `{"status":"crit"}`, status: statusCrit},
		{name: "unknown", out: "\n  {\"status\":\"unknown\"}\n", status: statusUnknown},
		{
			name:   "extra keys ignored, metrics in output order",
			out:    `{"version": 2, "status": "ok", "extra": {"deep": [1, {"x": null}]}, "metrics": {"z": 1, "a": -2.5e3}}`,
			status: statusOK, values: []itemValue{{"c:z", 1}, {"c:a", -2500}},
		},
		{name: "null message and metrics", out: `{"status":"ok","message":null,"metrics":null}`, status: statusOK},
		{
			name:   "duplicate and unreportable metric names skipped",
			out:    `{"status":"ok","metrics":{"a":1,"a":2,"":3,"x\u0001":4}}`,
			status: statusOK, values: []itemValue{{"c:a", 1}},
		},
		{name: "invalid JSON", out: `{"status": "ok"`, status: statusUnknown, message: reasonInvalidJSON},
		{name: "trailing data", out: `{"status": "ok"} {"status": "crit"}`, status: statusUnknown, message: reasonInvalidJSON},
		{name: "empty output", out: ``, status: statusUnknown, message: reasonInvalidJSON},
		{name: "not an object", out: `["ok"]`, status: statusUnknown, message: reasonNotObject},
		{name: "a string", out: `"ok"`, status: statusUnknown, message: reasonNotObject},
		{name: "missing status", out: `{"message": "secret-ish text"}`, status: statusUnknown, message: reasonNoStatus},
		{name: "unknown status", out: `{"status": "OK"}`, status: statusUnknown, message: reasonBadStatus},
		{name: "status not a string", out: `{"status": 0}`, status: statusUnknown, message: reasonBadStatus},
		{name: "null status", out: `{"status": null}`, status: statusUnknown, message: reasonBadStatus},
		{name: "message not a string", out: `{"status": "ok", "message": 5}`, status: statusUnknown, message: reasonBadMessage},
		{name: "metrics not an object", out: `{"status": "ok", "metrics": [1]}`, status: statusUnknown, message: reasonBadMetrics},
		{name: "metric a string", out: `{"status": "ok", "metrics": {"a": "1"}}`, status: statusUnknown, message: reasonBadMetric},
		{name: "metric an object", out: `{"status": "ok", "metrics": {"a": {"b": 1}}}`, status: statusUnknown, message: reasonBadMetric},
		{name: "metric null", out: `{"status": "ok", "metrics": {"a": null}}`, status: statusUnknown, message: reasonBadMetric},
		{name: "metric out of range", out: `{"status": "ok", "metrics": {"a": 1e400}}`, status: statusUnknown, message: reasonBadMetric},
		{
			name: "deeply nested", out: `{"status":"ok","x":` + strings.Repeat("[", 20000) + strings.Repeat("]", 20000) + `}`,
			status: statusUnknown, message: reasonInvalidJSON,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, msg, values := parseJSON("c", runResult{stdout: []byte(tt.out), exited: true})
			if st != tt.status || msg != tt.message {
				t.Errorf("parseJSON = (%s, %q), want (%s, %q)", st, msg, tt.status, tt.message)
			}
			if fmt.Sprint(values) != fmt.Sprint(tt.values) {
				t.Errorf("values = %v, want %v", values, tt.values)
			}
		})
	}
}

func TestParseJSONTruncated(t *testing.T) {
	st, msg, values := parseJSON("c", runResult{stdout: []byte(`{"status":"ok"}`), truncated: true, exited: true})
	if st != statusUnknown || msg != reasonTooLarge || values != nil {
		t.Errorf("parseJSON of truncated output = (%s, %q, %v), want (unknown, %q, nil)", st, msg, values, reasonTooLarge)
	}
}

func TestParseJSONExitCodeDoesNotDecide(t *testing.T) {
	st, _, _ := parseJSON("c", runResult{stdout: []byte(`{"status":"ok"}`), exited: true, exitCode: 2})
	if st != statusOK {
		t.Errorf("status = %s, want ok", st)
	}
}

func TestParseJSONValueCap(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"status":"ok","metrics":{`)
	for i := range maxValues + 1 {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"v%d":%d`, i, i)
	}
	b.WriteString(`}}`)
	st, _, values := parseJSON("c", runResult{stdout: []byte(b.String()), exited: true})
	if st != statusOK || len(values) != maxValues {
		t.Errorf("status %s with %d values, want ok with %d", st, len(values), maxValues)
	}
}

func TestParseJSONStillChecksMetricsPastTheCap(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"status":"ok","metrics":{`)
	for i := range maxValues {
		fmt.Fprintf(&b, `"v%d":%d,`, i, i)
	}
	b.WriteString(`"late":"x"}}`)
	st, msg, _ := parseJSON("c", runResult{stdout: []byte(b.String()), exited: true})
	if st != statusUnknown || msg != reasonBadMetric {
		t.Errorf("parseJSON = (%s, %q), want (unknown, %q)", st, msg, reasonBadMetric)
	}
}
