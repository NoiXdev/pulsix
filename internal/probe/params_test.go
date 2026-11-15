package probe_test

import (
	"math"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
)

func TestValueAndBoolValue(t *testing.T) {
	tests := []struct {
		name   string
		obs    probe.Observation
		metric string
		want   float64
	}{
		{"value", probe.Value("tcp.time_ms", 12.5), "tcp.time_ms", 12.5},
		{"true", probe.BoolValue("tls.valid", true), "tls.valid", 1},
		{"false", probe.BoolValue("tls.valid", false), "tls.valid", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := tc.obs
			if o.Metric != tc.metric || o.Value == nil || *o.Value != tc.want || o.Err != "" || o.Label != nil || o.Item != "" {
				t.Fatalf("observation %+v, want only %s = %v", o, tc.metric, tc.want)
			}
		})
	}
}

// TestValueDoesNotAlias: every observation owns its value, so changing one
// cannot change another built from the same variable.
func TestValueDoesNotAlias(t *testing.T) {
	v := 1.0
	a, b := probe.Value("m", v), probe.Value("m", v)
	*a.Value = 2
	if *b.Value != 1 || v != 1 {
		t.Fatalf("observations share their value: %v, %v", *b.Value, v)
	}
}

func TestDown(t *testing.T) {
	obs := probe.Down("tcp")
	if len(obs) != 1 || obs[0].Metric != "tcp.up" || obs[0].Value == nil || *obs[0].Value != 0 || obs[0].Err != "" {
		t.Fatalf("Down(tcp) = %+v, want only tcp.up = 0", obs)
	}
}

func TestFailAll(t *testing.T) {
	obs := probe.FailAll("boom", "dns.up", "dns.time_ms")
	if len(obs) != 2 {
		t.Fatalf("FailAll = %+v, want one error per metric", obs)
	}
	for i, metric := range []string{"dns.up", "dns.time_ms"} {
		if o := obs[i]; o.Metric != metric || o.Err != "boom" || o.Value != nil {
			t.Fatalf("observation %d = %+v, want error boom for %s", i, o, metric)
		}
	}
	if obs := probe.FailAll("boom"); len(obs) != 0 {
		t.Fatalf("FailAll without metrics = %+v, want none", obs)
	}
}

func TestMillis(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want float64
	}{
		{0, 0},
		{time.Millisecond, 1},
		{1500 * time.Microsecond, 1.5},
		{2 * time.Second, 2000},
	}
	for _, tc := range tests {
		if got := probe.Millis(tc.d); got != tc.want {
			t.Errorf("Millis(%v) = %v, want %v", tc.d, got, tc.want)
		}
	}
}

func TestNumParam(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want float64
	}{
		{"float64 from JSON", float64(2.5), 2.5},
		{"int", 3, 3},
		{"int64", int64(4), 4},
		{"string falls back", "5", 9},
		{"bool falls back", true, 9},
		{"missing falls back", nil, 9},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{}
			if tc.v != nil {
				params["n"] = tc.v
			}
			if got := probe.NumParam(params, "n", 9); got != tc.want {
				t.Fatalf("NumParam = %v, want %v", got, tc.want)
			}
		})
	}
	if got := probe.NumParam(nil, "n", 7); got != 7 {
		t.Fatalf("NumParam(nil params) = %v, want the default", got)
	}
}

func TestPortParam(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		want   int
		ok     bool
	}{
		{"given", map[string]any{"port": float64(443)}, 443, true},
		{"given as int", map[string]any{"port": 8080}, 8080, true},
		{"default", nil, 22, true},
		{"lowest", map[string]any{"port": float64(1)}, 1, true},
		{"highest", map[string]any{"port": float64(65535)}, 65535, true},
		{"zero", map[string]any{"port": float64(0)}, 0, false},
		{"negative", map[string]any{"port": float64(-1)}, 0, false},
		{"too high", map[string]any{"port": float64(65536)}, 0, false},
		{"fraction", map[string]any{"port": 80.5}, 0, false},
		{"not a number", map[string]any{"port": math.NaN()}, 0, false},
		{"infinite", map[string]any{"port": math.Inf(1)}, 0, false},
		{"string uses the default", map[string]any{"port": "80"}, 22, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := probe.PortParam(tc.params, 22)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("PortParam(%v) = %d, %v; want %d, %v", tc.params, got, ok, tc.want, tc.ok)
			}
		})
	}
	if _, ok := probe.PortParam(nil, 0); ok {
		t.Fatal("PortParam accepted a default of 0")
	}
}

func TestBoolParam(t *testing.T) {
	params := map[string]any{"on": true, "off": false, "text": "true", "num": float64(1)}
	tests := []struct {
		key       string
		def, want bool
	}{
		{"on", false, true},
		{"off", true, false},
		{"text", false, false},
		{"text", true, true},
		{"num", false, false},
		{"missing", true, true},
		{"missing", false, false},
	}
	for _, tc := range tests {
		if got := probe.BoolParam(params, tc.key, tc.def); got != tc.want {
			t.Errorf("BoolParam(%s, %v) = %v, want %v", tc.key, tc.def, got, tc.want)
		}
	}
}

func TestStrParam(t *testing.T) {
	params := map[string]any{"name": "example.com", "empty": "", "num": float64(1), "flag": true}
	tests := []struct{ key, want string }{
		{"name", "example.com"},
		{"empty", ""},
		{"num", "def"},
		{"flag", "def"},
		{"missing", "def"},
	}
	for _, tc := range tests {
		if got := probe.StrParam(params, tc.key, "def"); got != tc.want {
			t.Errorf("StrParam(%s) = %q, want %q", tc.key, got, tc.want)
		}
	}
}
