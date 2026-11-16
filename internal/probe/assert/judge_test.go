package assert_test

import (
	"testing"

	"github.com/noixdev/pulsix/internal/probe/assert"
)

func mustParse(t *testing.T, entry string) assert.Assertion {
	t.Helper()
	a, err := assert.Parse(entry)
	if err != nil {
		t.Fatalf("Parse(%q) = %v", entry, err)
	}
	return a
}

// TestHoldsNumber: every numeric op, both ends of in inclusive.
func TestHoldsNumber(t *testing.T) {
	cases := []struct {
		entry string
		v     float64
		want  bool
	}{
		{"a=status == 200", 200, true},
		{"a=status == 200", 201, false},
		{"a=status != 200", 201, true},
		{"a=status != 200", 200, false},
		{"a=status < 400", 399, true},
		{"a=status < 400", 400, false},
		{"a=status <= 400", 400, true},
		{"a=status <= 400", 400.5, false},
		{"a=status > 199", 200, true},
		{"a=status > 199", 199, false},
		{"a=status >= 200", 200, true},
		{"a=status >= 200", 199.9, false},
		{"a=status in 200..299", 200, true},
		{"a=status in 200..299", 299, true},
		{"a=status in 200..299", 250, true},
		{"a=status in 200..299", 199, false},
		{"a=status in 200..299", 300, false},
		{"a=cert_days in -1.5..0", -1.5, true},
		{"a=cert_days in -1.5..0", -1.6, false},
	}
	for _, tc := range cases {
		if got := mustParse(t, tc.entry).HoldsNumber(tc.v); got != tc.want {
			t.Errorf("%s with %v = %v, want %v", tc.entry, tc.v, got, tc.want)
		}
	}
}

// TestHoldsText: every text op on a present value, and the absent-value
// rules: exists fails, !exists passes, != passes, every other op fails.
func TestHoldsText(t *testing.T) {
	cases := []struct {
		entry   string
		v       string
		present bool
		want    bool
	}{
		{`a=header("X") == "ok"`, "ok", true, true},
		{`a=header("X") == "ok"`, "OK", true, false},
		{`a=header("X") != "ok"`, "OK", true, true},
		{`a=header("X") != "ok"`, "ok", true, false},
		{`a=header("X") contains "json"`, "application/json; charset=utf-8", true, true},
		{`a=header("X") contains "json"`, "text/html", true, false},
		{`a=header("X") !contains "json"`, "text/html", true, true},
		{`a=header("X") !contains "json"`, "application/json", true, false},
		{`a=header("X") matches "^v[0-9]+$"`, "v12", true, true},
		{`a=header("X") matches "^v[0-9]+$"`, "v12b", true, false},
		{`a=header("X") !matches "(?i)error"`, "fine", true, true},
		{`a=header("X") !matches "(?i)error"`, "An ERROR", true, false},
		{`a=header("X") exists`, "", true, true},
		{`a=header("X") !exists`, "", true, false},
		{`a=body == ""`, "", true, true},
		// Absent.
		{`a=header("X") exists`, "", false, false},
		{`a=header("X") !exists`, "", false, true},
		{`a=header("X") != "ok"`, "", false, true},
		{`a=header("X") == ""`, "", false, false},
		{`a=header("X") contains ""`, "", false, false},
		{`a=header("X") !contains "x"`, "", false, false},
		{`a=header("X") matches ".*"`, "", false, false},
		{`a=header("X") !matches "x"`, "", false, false},
	}
	for _, tc := range cases {
		if got := mustParse(t, tc.entry).HoldsText(tc.v, tc.present); got != tc.want {
			t.Errorf("%s with %q (present %v) = %v, want %v", tc.entry, tc.v, tc.present, got, tc.want)
		}
	}
}

// TestNegative: the ops a cut-off body cannot prove.
func TestNegative(t *testing.T) {
	for op, want := range map[assert.Op]bool{
		assert.OpNotContains: true, assert.OpNotMatches: true, assert.OpNotExists: true,
		assert.OpContains: false, assert.OpMatches: false, assert.OpExists: false,
		assert.OpEq: false, assert.OpNe: false, assert.OpIn: false, assert.OpLt: false,
	} {
		if op.Negative() != want {
			t.Errorf("%s.Negative() = %v", op, op.Negative())
		}
	}
}
