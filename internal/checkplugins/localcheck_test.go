package checkplugins_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

func TestValidLocalCheckKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"local-backup", true},
		{"local-pg-2", true},
		{"local-0", true},
		{"local--x", true},
		{"local-" + strings.Repeat("a", 57), true}, // 63 characters, the pattern's longest
		{"local-" + strings.Repeat("a", 58), false},
		{"local-", false}, // the prefix alone names no check
		{"backup", false},
		{"local", false},
		{"locals-backup", false},
		{"Local-backup", false},
		{"local-Bad", false},
		{"local-a_b", false},
		{" local-a", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := checkplugins.ValidLocalCheckKey(tt.key); got != tt.want {
			t.Errorf("ValidLocalCheckKey(%q) = %v, want %v", tt.key, got, tt.want)
		}
	}
	if checkplugins.LocalCheckPrefix != "local-" {
		t.Errorf("LocalCheckPrefix = %q", checkplugins.LocalCheckPrefix)
	}
}

func TestIntervalBounds(t *testing.T) {
	if checkplugins.MinInterval != 10*time.Second || checkplugins.MaxInterval != 24*time.Hour {
		t.Fatalf("bounds = %v, %v; want 10s, 24h", checkplugins.MinInterval, checkplugins.MaxInterval)
	}
}

func TestCanonicalParams(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		want   string
	}{
		{"nil", nil, "{}"},
		{"empty", map[string]any{}, "{}"},
		{"sorted keys", map[string]any{"format": "json", "commands": []any{"backup_age"}}, `{"commands":["backup_age"],"format":"json"}`},
		{"numbers and bools", map[string]any{"port": float64(5432), "tls": true, "ratio": 1.5}, `{"port":5432,"ratio":1.5,"tls":true}`},
		{"nested maps sorted", map[string]any{"b": map[string]any{"z": 1, "a": 2}, "a": "x"}, `{"a":"x","b":{"a":2,"z":1}}`},
	}
	for _, tt := range tests {
		got, err := checkplugins.CanonicalParams(tt.params)
		if err != nil || string(got) != tt.want {
			t.Errorf("%s: CanonicalParams = %s, %v; want %s", tt.name, got, err, tt.want)
		}
	}
}

func TestCanonicalParamsRefusesWhatJSONCannotCarry(t *testing.T) {
	if _, err := checkplugins.CanonicalParams(map[string]any{"x": math.NaN()}); err == nil {
		t.Fatal("CanonicalParams(NaN) = nil error")
	}
}
