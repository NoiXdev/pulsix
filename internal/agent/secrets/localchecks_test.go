package secrets

import (
	"bytes"
	"fmt"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// paramValue is planted in local check params below: no problem text,
// printed or logged form may ever contain it.
const paramValue = "param-Val-91"

func TestParseAcceptsTheDocumentedLocalChecks(t *testing.T) {
	src := "secrets:\n  pg_main:\n    dsn: x\n" +
		"commands:\n  backup_age: [" + quoted(program()) + "]\n" +
		`checks:
  local-pg:
    plugin: postgres
    params:
      secret: pg_main
  local-backup:
    plugin: script
    interval_seconds: 300
    params:
      commands: [backup_age]
      format: json
`
	set, err := parse([]byte(src))
	if err != nil {
		t.Fatalf("parse = %v", err)
	}
	if !reflect.DeepEqual(set.Names(), []string{"pg_main"}) || !reflect.DeepEqual(set.CommandNames(), []string{"backup_age"}) {
		t.Fatalf("set = %v", set)
	}
	want := []LocalCheck{
		{
			Key: "local-backup", Plugin: "script", Interval: 300 * time.Second,
			Params:     map[string]any{"commands": []any{"backup_age"}, "format": "json"},
			ParamsJSON: `{"commands":["backup_age"],"format":"json"}`,
		},
		{
			Key: "local-pg", Plugin: "postgres", Interval: 60 * time.Second,
			Params:     map[string]any{"secret": "pg_main"},
			ParamsJSON: `{"secret":"pg_main"}`,
		},
	}
	if got := set.LocalChecks(); !reflect.DeepEqual(got, want) {
		t.Fatalf("LocalChecks =\n%#v\nwant\n%#v", got, want)
	}
	if p := set.LocalCheckProblems(); len(p) != 0 {
		t.Fatalf("LocalCheckProblems = %q", p)
	}
}

func TestParseLocalChecksWithoutParams(t *testing.T) {
	for _, src := range []string{
		"checks:\n  local-cpu:\n    plugin: cpu\n",
		"checks:\n  local-cpu:\n    plugin: cpu\n    params: {}\n",
		"checks: {local-cpu: {plugin: cpu, interval_seconds: 60}}\n",
	} {
		set, err := parse([]byte(src))
		if err != nil {
			t.Errorf("parse(%q) = %v", src, err)
			continue
		}
		want := []LocalCheck{{Key: "local-cpu", Plugin: "cpu", Interval: time.Minute, ParamsJSON: "{}"}}
		if got := set.LocalChecks(); !reflect.DeepEqual(got, want) || len(set.LocalCheckProblems()) != 0 {
			t.Errorf("parse(%q): LocalChecks = %#v, problems %q", src, got, set.LocalCheckProblems())
		}
	}
}

func TestParseEmptyChecksHoldNone(t *testing.T) {
	for _, src := range []string{"checks:\n", "checks: {}\n", "checks: ~\nsecrets: {}\n"} {
		set, err := parse([]byte(src))
		if err != nil || len(set.LocalChecks()) != 0 || len(set.LocalCheckProblems()) != 0 {
			t.Errorf("parse(%q) = %v, %v", src, set, err)
		}
	}
}

// TestParamScalarTyping: a plain scalar resolves the way YAML's core schema
// does, as a JSON-decoded manager push would carry it; a quoted scalar and
// !!str are strings.
func TestParamScalarTyping(t *testing.T) {
	src := `port: 5432
ratio: 1.5
tls: true
off: false
quoted: "5432"
single: '1.5'
name: !!str 12
huge: 18446744073709551615
neg: -12
plus: +5
exp: 1e3
negexp: -1.5e-2
dot: .5
trail: 1.
zero: 0
zerof: 0.25
clock: 1:30
yes: yes
when: 2026-11-14
word: hello
list: [a, 2, "3", false]
empty: []
`
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	got, problem, err := parseParams(doc.Content[0])
	if err != nil || problem != "" {
		t.Fatalf("parseParams = %v, %q", err, problem)
	}
	want := map[string]any{
		"port":   float64(5432),
		"ratio":  1.5,
		"tls":    true,
		"off":    false,
		"quoted": "5432",
		"single": "1.5",
		"name":   "12",
		"huge":   float64(18446744073709551615),
		"neg":    float64(-12),
		"plus":   float64(5),
		"exp":    float64(1000),
		"negexp": -0.015,
		"dot":    0.5,
		"trail":  float64(1),
		"zero":   float64(0),
		"zerof":  0.25,
		"clock":  "1:30", // no sexagesimal numbers
		"yes":    "yes",  // no YAML 1.1 booleans
		"when":   "2026-11-14",
		"word":   "hello",
		"list":   []any{"a", float64(2), "3", false},
		"empty":  []any{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseParams =\n%#v\nwant\n%#v", got, want)
	}
}

// TestParamTypingReachesThePlugin: numbers and bools typed in the file pass
// the plugin's own validation, as the same values from the manager would.
func TestParamTypingReachesThePlugin(t *testing.T) {
	src := "checks:\n" +
		"  local-script:\n    plugin: script\n    params:\n      commands: [a, !!str 12]\n      timeout_seconds: 30\n" +
		"  local-nginx:\n    plugin: nginx\n    params:\n      verify_tls: false\n"
	set, err := parse([]byte(src))
	if err != nil {
		t.Fatalf("parse = %v", err)
	}
	if p := set.LocalCheckProblems(); len(p) != 0 {
		t.Fatalf("problems = %q", p)
	}
	got := set.LocalChecks()
	if len(got) != 2 || got[0].ParamsJSON != `{"verify_tls":false}` ||
		got[1].ParamsJSON != `{"commands":["a","12"],"timeout_seconds":30}` {
		t.Fatalf("LocalChecks = %#v", got)
	}
}

// TestParseRefusesMalformedChecks: a structural fault anywhere under checks
// refuses the whole file — no secrets, no commands, no local checks — with
// the place and a fixed reason.
func TestParseRefusesMalformedChecks(t *testing.T) {
	head := "secrets:\n  pg:\n    password: " + secretValue + "\ncommands:\n  a: [" + quoted(program()) + "]\n"
	const at = 6 // the line of "checks:"
	line := func(n int) string { return strconv.Itoa(at + n) }
	tests := []struct {
		name string
		body string // after "checks:"
		want string
	}{
		{"checks a sequence", "\n  - " + paramValue + "\n",
			"line " + line(1) + ", column 3: checks must map check keys to their plugin, interval_seconds and params"},
		{"checks a scalar", " " + paramValue + "\n",
			"line " + line(0) + ", column 9: checks must map check keys to their plugin, interval_seconds and params"},
		{"entry a scalar", "\n  local-a: " + paramValue + "\n",
			"line " + line(1) + ", column 12: a check must map plugin, interval_seconds and params"},
		{"entry a sequence", "\n  local-a: [" + paramValue + "]\n",
			"line " + line(1) + ", column 12: a check must map plugin, interval_seconds and params"},
		{"entry null", "\n  local-a:\n  local-b:\n    plugin: cpu\n",
			"line " + line(1) + ", column 3: a check must map plugin, interval_seconds and params"},
		{"params a sequence", "\n  local-a:\n    plugin: cpu\n    params:\n      - " + paramValue + "\n",
			"line " + line(4) + ", column 7: params must map parameter names to values; write {} for none"},
		{"params a scalar", "\n  local-a:\n    plugin: cpu\n    params: " + paramValue + "\n",
			"line " + line(3) + ", column 13: params must map parameter names to values; write {} for none"},
		{"params null", "\n  local-a:\n    plugin: cpu\n    params:\n",
			"line " + line(3) + ", column 5: params must map parameter names to values; write {} for none"},
		{"params value a mapping", "\n  local-a:\n    plugin: script\n    params:\n      format:\n        x: " + paramValue + "\n",
			"line " + line(5) + ", column 9: a parameter value must be a single value or a list of single values"},
		{"params nested sequence", "\n  local-a:\n    plugin: script\n    params:\n      commands: [[" + paramValue + "]]\n",
			"line " + line(4) + ", column 18: a list entry must be a single value"},
		{"params list mapping entry", "\n  local-a:\n    plugin: script\n    params:\n      commands: [{x: " + paramValue + "}]\n",
			"line " + line(4) + ", column 18: a list entry must be a single value"},
		{"params value null", "\n  local-a:\n    plugin: script\n    params:\n      format:\n",
			"line " + line(4) + `, column 14: a parameter has no value; write "" for an empty one`},
		{"params list entry null", "\n  local-a:\n    plugin: script\n    params:\n      commands: [a, ~]\n",
			"line " + line(4) + `, column 21: a list entry has no value; write "" for an empty one`},
		{"params name not a single value", "\n  local-a:\n    plugin: script\n    params:\n      ? [a]\n      : b\n",
			"line " + line(4) + ", column 9: a parameter name must be a single value"},
		{"plugin null", "\n  local-a:\n    plugin:\n",
			"line " + line(2) + `, column 12: an entry field has no value`},
		{"interval null", "\n  local-a:\n    plugin: cpu\n    interval_seconds: ~\n",
			"line " + line(3) + `, column 23: an entry field has no value`},
		{"tag other than !!str", "\n  local-a:\n    plugin: cpu\n    interval_seconds: !!int 60\n",
			"line " + line(3) + ", column 23: " + badTag},
		{"tag on params", "\n  local-a:\n    plugin: cpu\n    params: !!map {}\n",
			"line " + line(3) + ", column 13: " + badTag},
		{"anchor in params", "\n  local-a:\n    plugin: script\n    params:\n      format: &" + paramValue + " json\n",
			"line " + line(4) + ", column 15: " + badAnchor},
		{"duplicate check", "\n  local-a:\n    plugin: cpu\n  local-a:\n    plugin: cpu\n",
			"line " + line(3) + ", column 3: this check is already defined at line " + line(1)},
		{"duplicate field", "\n  local-a:\n    plugin: cpu\n    plugin: memory\n",
			"line " + line(3) + ", column 5: this field is already set at line " + line(2)},
		{"duplicate param", "\n  local-a:\n    plugin: script\n    params:\n      format: json\n      format: nagios\n",
			"line " + line(5) + ", column 7: this parameter is already set at line " + line(4)},
		{"check key not a single value", "\n  ? [" + paramValue + "]\n  : {plugin: cpu}\n",
			"line " + line(1) + ", column 5: a check key must be a single value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set, err := parse([]byte(head + "checks:" + tt.body))
			if err == nil {
				t.Fatalf("parse accepted it: %v, checks %v", set, set.LocalChecks())
			}
			if got := err.Error(); got != tt.want {
				t.Errorf("error = %q\nwant    %q", got, tt.want)
			}
			if msg := err.Error(); strings.Contains(msg, paramValue) || strings.Contains(msg, secretValue) {
				t.Errorf("error quotes the file: %q", msg)
			}
			if set.Len() != 0 || len(set.CommandNames()) != 0 || len(set.LocalChecks()) != 0 || len(set.LocalCheckProblems()) != 0 {
				t.Errorf("a refused file left %v", set)
			}
		})
	}
}

// serverPlugin is a plugin the manager runs (a probe): never a local check.
func serverPlugin(t *testing.T) string {
	t.Helper()
	for _, d := range checkplugins.All() {
		if d.Runs == checkplugins.Server {
			return d.Name
		}
	}
	t.Fatal("no server-run plugin")
	return ""
}

// TestParseRefusesOneLocalCheck: what an entry says refuses that entry only,
// with the entry's line; the file and its other checks stay.
func TestParseRefusesOneLocalCheck(t *testing.T) {
	head := "secrets:\n  pg:\n    password: x\nchecks:\n  local-good:\n    plugin: cpu\n"
	const at = 7 // the bad entry's line
	long := make([]string, 40)
	for i := range long {
		long[i] = fmt.Sprintf("d%02d%s", i, strings.Repeat("x", 57))
	}
	tests := []struct {
		name string
		body string // the bad entry, starting at line at
		want string // the problem after "line N: "
	}{
		{"no prefix", "  backup:\n    plugin: cpu\n",
			"a local check key is local- followed by lowercase letters, digits and dashes, 63 characters in all at most"},
		{"upper case", "  local-Bad:\n    plugin: cpu\n",
			"a local check key is local- followed by lowercase letters, digits and dashes, 63 characters in all at most"},
		{"too long", "  local-" + strings.Repeat("a", 60) + ":\n    plugin: cpu\n",
			"a local check key is local- followed by lowercase letters, digits and dashes, 63 characters in all at most"},
		{"prefix only", "  local-:\n    plugin: cpu\n",
			"a local check key is local- followed by lowercase letters, digits and dashes, 63 characters in all at most"},
		{"unknown field", "  local-bad:\n    plugin: cpu\n    intervall: 60\n",
			"unknown entry field; an entry has plugin, interval_seconds and params"},
		{"unknown field with any value", "  local-bad:\n    plugin: cpu\n    " + paramValue + ":\n      - x: [" + paramValue + "]\n",
			"unknown entry field; an entry has plugin, interval_seconds and params"},
		{"plugin missing", "  local-bad:\n    interval_seconds: 60\n",
			"plugin is required"},
		{"plugin unknown", "  local-bad:\n    plugin: nope\n",
			"unknown plugin"},
		{"plugin a list", "  local-bad:\n    plugin: [cpu]\n",
			"plugin must be a plugin name"},
		{"plugin run by the server", "  local-bad:\n    plugin: " + serverPlugin(t) + "\n",
			"the plugin runs on the server, not on the agent"},
		{"interval too short", "  local-bad:\n    plugin: cpu\n    interval_seconds: 5\n",
			"interval_seconds must be a whole number from 10 to 86400"},
		{"interval too long", "  local-bad:\n    plugin: cpu\n    interval_seconds: 90000\n",
			"interval_seconds must be a whole number from 10 to 86400"},
		{"interval quoted", "  local-bad:\n    plugin: cpu\n    interval_seconds: \"60\"\n",
			"interval_seconds must be a whole number from 10 to 86400"},
		{"interval a float", "  local-bad:\n    plugin: cpu\n    interval_seconds: 60.5\n",
			"interval_seconds must be a whole number from 10 to 86400"},
		{"interval a list", "  local-bad:\n    plugin: cpu\n    interval_seconds: [60]\n",
			"interval_seconds must be a whole number from 10 to 86400"},
		{"interval huge", "  local-bad:\n    plugin: cpu\n    interval_seconds: 99999999999999999999\n",
			"interval_seconds must be a whole number from 10 to 86400"},
		{"params refused by the plugin", "  local-bad:\n    plugin: script\n    params:\n      commands: [a]\n      timeout_seconds: 99999\n",
			"script: timeout_seconds must be "},
		{"params value refused by the plugin", "  local-bad:\n    plugin: script\n    params:\n      commands: [a]\n      format: " + paramValue + "\n",
			"script: format must be one of nagios, json"},
		{"params unknown", "  local-bad:\n    plugin: cpu\n    params:\n      x: " + paramValue + "\n",
			`plugin "cpu" takes no parameters`},
		{"params name malformed", "  local-bad:\n    plugin: script\n    params:\n      \"" + paramValue + " x\": y\n",
			"a parameter name is 1 to 64 characters of A-Z a-z 0-9 _ . -"},
		{"params not finite", "  local-bad:\n    plugin: script\n    params:\n      commands: [a]\n      timeout_seconds: .inf\n",
			"params: numbers must be finite"},
		// A NUL (YAML's "\0") anywhere in params refuses the entry: the
		// manager cannot store one, and a value never shows in a problem.
		{"params value with a NUL", "  local-bad:\n    plugin: docker\n    params:\n      label: \"" + paramValue + "\\0=1\"\n",
			"params: a value must not contain a NUL character"},
		{"params list item with a NUL", "  local-bad:\n    plugin: disk\n    params:\n      mounts: [/a, \"/" + paramValue + "\\0\"]\n",
			"params: a value must not contain a NUL character"},
		{"params name with a NUL", "  local-bad:\n    plugin: disk\n    params:\n      \"mounts\\0" + paramValue + "\": [/a]\n",
			"a parameter name is 1 to 64 characters of A-Z a-z 0-9 _ . -"},
		{"order: a NUL before the plugin's validation", "  local-bad:\n    plugin: script\n    params:\n      format: \"json\\0\"\n",
			"params: a value must not contain a NUL character"},
		{"interval 9", "  local-bad:\n    plugin: cpu\n    interval_seconds: 9\n",
			"interval_seconds must be a whole number from 10 to 86400"},
		{"interval 86401", "  local-bad:\n    plugin: cpu\n    interval_seconds: 86401\n",
			"interval_seconds must be a whole number from 10 to 86400"},
		{"params 2049 bytes as JSON", "  local-bad:\n    plugin: postgres\n    params:\n" + paramsOfJSONLength(t, 2049),
			"params are longer than 2048 bytes as JSON"},
		// An entry with two faults reports the first in the documented
		// order: key, fields, plugin, interval, params, JSON size.
		{"order: key before plugin", "  bad:\n    plugin: nope\n",
			"a local check key is local- followed by"},
		{"order: field before plugin", "  local-bad:\n    plugin: " + serverPlugin(t) + "\n    intervall: 60\n",
			"unknown entry field"},
		{"order: plugin before interval", "  local-bad:\n    plugin: nope\n    interval_seconds: 5\n",
			"unknown plugin"},
		{"order: missing plugin before interval", "  local-bad:\n    interval_seconds: 5\n",
			"plugin is required"},
		{"order: interval before params", "  local-bad:\n    plugin: script\n    interval_seconds: 5\n    params:\n      format: " + paramValue + "\n",
			"interval_seconds must be"},
		{"order: number form before the plugin's validation", "  local-bad:\n    plugin: script\n    params:\n      timeout_seconds: 010\n",
			"params: " + wantNumberForm},
		{"order: the plugin's validation before JSON size", "  local-bad:\n    plugin: postgres\n    params:\n      databases: [" + strings.Join(long, ", ") + "]\n",
			"postgres: secret is required"},
		{"params over 2048 bytes as JSON", "  local-bad:\n    plugin: postgres\n    params:\n      secret: pg\n      databases: [" + strings.Join(long, ", ") + "]\n",
			"params are longer than 2048 bytes as JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set, err := parse([]byte(head + tt.body + "  local-zz:\n    plugin: memory\n"))
			if err != nil {
				t.Fatalf("parse refused the file: %v", err)
			}
			if !reflect.DeepEqual(set.Names(), []string{"pg"}) {
				t.Errorf("Names = %v", set.Names())
			}
			var keys []string
			for _, c := range set.LocalChecks() {
				keys = append(keys, c.Key)
			}
			if !reflect.DeepEqual(keys, []string{"local-good", "local-zz"}) {
				t.Errorf("kept %v, want the two good checks", keys)
			}
			problems := set.LocalCheckProblems()
			prefix := "line " + strconv.Itoa(at) + ": "
			if len(problems) != 1 || !strings.HasPrefix(problems[0], prefix+tt.want) {
				t.Fatalf("problems = %q, want one starting %q", problems, prefix+tt.want)
			}
			if strings.Contains(problems[0], paramValue) {
				t.Errorf("problem quotes a param value: %q", problems[0])
			}
		})
	}
}

// TestParseKeepsAtMost32LocalChecks: the 33rd valid entry in key order is
// refused, wherever it stands in the file.
func TestParseKeepsAtMost32LocalChecks(t *testing.T) {
	var b strings.Builder
	b.WriteString("checks:\n")
	for i := 32; i >= 0; i-- { // reverse key order: the last key comes first
		fmt.Fprintf(&b, "  local-c%02d:\n    plugin: cpu\n", i)
	}
	b.WriteString("  bad:\n    plugin: cpu\n") // refused, so not counted
	set, err := parse([]byte(b.String()))
	if err != nil {
		t.Fatalf("parse = %v", err)
	}
	got := set.LocalChecks()
	if len(got) != 32 || got[0].Key != "local-c00" || got[31].Key != "local-c31" {
		t.Fatalf("LocalChecks = %d checks, %v … %v", len(got), got[0].Key, got[len(got)-1].Key)
	}
	want := []string{"line 2: more than 32 local checks", "line 68: a local check key"}
	problems := set.LocalCheckProblems()
	if len(problems) != 2 || problems[0] != want[0] || !strings.HasPrefix(problems[1], want[1]) {
		t.Fatalf("problems = %q, want %q (line order)", problems, want)
	}
}

func TestLocalChecksIsACopy(t *testing.T) {
	set, err := parse([]byte("checks:\n  local-s:\n    plugin: script\n    params:\n      commands: [a]\n"))
	if err != nil {
		t.Fatal(err)
	}
	first := set.LocalChecks()
	first[0].Params["commands"].([]any)[0] = "changed"
	first[0].Params["new"] = 1
	first[0].Key = "changed"
	again := set.LocalChecks()
	if again[0].Key != "local-s" || !reflect.DeepEqual(again[0].Params, map[string]any{"commands": []any{"a"}}) {
		t.Fatalf("LocalChecks shares its state: %#v", again[0])
	}
	set2, err := parse([]byte("checks:\n  bad:\n    plugin: cpu\n"))
	if err != nil {
		t.Fatal(err)
	}
	problems := set2.LocalCheckProblems()
	problems[0] = "changed"
	if set2.LocalCheckProblems()[0] == "changed" {
		t.Fatal("LocalCheckProblems shares its slice")
	}
}

func TestSetNeverPrintsALocalCheckParam(t *testing.T) {
	set, err := parse([]byte("checks:\n" +
		"  local-pg:\n    plugin: postgres\n    params:\n      secret: " + paramValue + "\n" +
		"  bad:\n    plugin: postgres\n    params:\n      secret: " + paramValue + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		got := fmt.Sprintf(format, set)
		if strings.Contains(got, paramValue) || !strings.Contains(got, "local-pg") || !strings.Contains(got, "check_problems=1") {
			t.Errorf("Sprintf(%s) = %q, want the keys and the problem count only", format, got)
		}
	}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("loaded", "names", set)
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("loaded", "names", set)
	got := buf.String()
	if strings.Contains(got, paramValue) {
		t.Errorf("log = %q, want no param value", got)
	}
	for _, want := range []string{"names.checks=[local-pg]", "names.check_problems=1", `"checks":["local-pg"]`, `"check_problems":1`} {
		if !strings.Contains(got, want) {
			t.Errorf("log = %q, want %s", got, want)
		}
	}
}

// wantNumberForm is the reason for a number YAML 1.1 would read differently
// from what an operator means (010 is 8 there) or that no JSON push carries.
const wantNumberForm = "numbers are written in plain decimal, without leading zeros, underscores or base prefixes"

// TestParseRefusesNonDecimalNumbers: a plain number yaml would read in a
// YAML 1.1 form (octal 010, 1_000, 0b11, 0x1F) refuses its entry rather
// than silently meaning something else; .inf and .nan refuse it too.
func TestParseRefusesNonDecimalNumbers(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"interval octal", "    plugin: cpu\n    interval_seconds: 0300\n", "interval_seconds: " + wantNumberForm},
		{"interval underscore", "    plugin: cpu\n    interval_seconds: 1_000\n", "interval_seconds: " + wantNumberForm},
		{"interval binary", "    plugin: cpu\n    interval_seconds: 0b111100\n", "interval_seconds: " + wantNumberForm},
		{"interval hex", "    plugin: cpu\n    interval_seconds: 0x3c\n", "interval_seconds: " + wantNumberForm},
		{"interval 0o octal", "    plugin: cpu\n    interval_seconds: 0o74\n", "interval_seconds: " + wantNumberForm},
		{"param octal", "    plugin: script\n    params:\n      commands: [a]\n      timeout_seconds: 010\n", "params: " + wantNumberForm},
		{"param underscore", "    plugin: script\n    params:\n      commands: [a]\n      timeout_seconds: 1_0\n", "params: " + wantNumberForm},
		{"param binary", "    plugin: script\n    params:\n      commands: [a]\n      timeout_seconds: 0b11\n", "params: " + wantNumberForm},
		{"param hex", "    plugin: script\n    params:\n      commands: [a]\n      timeout_seconds: 0x1F\n", "params: " + wantNumberForm},
		{"param leading zero float", "    plugin: script\n    params:\n      commands: [a]\n      timeout_seconds: 00.5\n", "params: " + wantNumberForm},
		{"param octal in a list", "    plugin: script\n    params:\n      commands: [a, 010]\n", "params: " + wantNumberForm},
		{"param inf", "    plugin: script\n    params:\n      commands: [a]\n      timeout_seconds: .inf\n", "params: numbers must be finite"},
		{"param negative inf", "    plugin: script\n    params:\n      commands: [a]\n      timeout_seconds: -.Inf\n", "params: numbers must be finite"},
		{"param nan", "    plugin: script\n    params:\n      commands: [a]\n      timeout_seconds: .nan\n", "params: numbers must be finite"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set, err := parse([]byte("checks:\n  local-bad:\n" + tt.body + "  local-ok:\n    plugin: cpu\n"))
			if err != nil {
				t.Fatalf("parse refused the file: %v", err)
			}
			if got := set.LocalChecks(); len(got) != 1 || got[0].Key != "local-ok" {
				t.Errorf("LocalChecks = %#v, want local-ok only", got)
			}
			if got, want := set.LocalCheckProblems(), []string{"line 2: " + tt.want}; !reflect.DeepEqual(got, want) {
				t.Errorf("problems = %q, want %q", got, want)
			}
		})
	}
}

// TestParseAcceptsTheBounds: the interval's bounds and the params' JSON size
// limit are inclusive; a signed decimal interval is fine.
func TestParseAcceptsTheBounds(t *testing.T) {
	tests := []struct {
		name, body string
		interval   time.Duration
	}{
		{"interval 10", "    plugin: cpu\n    interval_seconds: 10\n", 10 * time.Second},
		{"interval 86400", "    plugin: cpu\n    interval_seconds: 86400\n", 24 * time.Hour},
		{"interval +60", "    plugin: cpu\n    interval_seconds: +60\n", time.Minute},
		{"params 2048 bytes as JSON", "    plugin: postgres\n    params:\n" + paramsOfJSONLength(t, 2048), time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set, err := parse([]byte("checks:\n  local-ok:\n" + tt.body))
			if err != nil {
				t.Fatalf("parse = %v", err)
			}
			got := set.LocalChecks()
			if len(got) != 1 || got[0].Interval != tt.interval || len(set.LocalCheckProblems()) != 0 {
				t.Fatalf("LocalChecks = %#v, problems %q", got, set.LocalCheckProblems())
			}
		})
	}
}

// paramsOfJSONLength is the YAML of postgres params (a secret and a list of
// databases, indented for an entry's params) whose canonical JSON is
// exactly n bytes.
func paramsOfJSONLength(t *testing.T, n int) string {
	t.Helper()
	params := map[string]any{"secret": "pg", "databases": []any{}}
	size := func() int {
		raw, err := checkplugins.CanonicalParams(params)
		if err != nil {
			t.Fatal(err)
		}
		return len(raw)
	}
	var dbs []any
	for i := 0; n-size() > 66; i++ { // a 60-byte entry costs at most 63 bytes
		dbs = append(dbs, fmt.Sprintf("d%02d%s", i, strings.Repeat("x", 57)))
		params["databases"] = dbs
	}
	last := n - size() - 2 // its quotes
	if len(dbs) > 0 {
		last-- // its comma
	}
	if last < 1 || last > 63 {
		t.Fatalf("cannot reach %d bytes (last entry %d)", n, last)
	}
	dbs = append(dbs, strings.Repeat("z", last))
	params["databases"] = dbs
	if got := size(); got != n {
		t.Fatalf("params JSON is %d bytes, want %d", got, n)
	}
	names := make([]string, len(dbs))
	for i, d := range dbs {
		names[i] = d.(string)
	}
	return "      secret: pg\n      databases: [" + strings.Join(names, ", ") + "]\n"
}
