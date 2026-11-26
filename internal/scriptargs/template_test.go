package scriptargs_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/scriptargs"
)

func mustPattern(t *testing.T, expr string, allowDash bool) scriptargs.Rule {
	t.Helper()
	r, err := scriptargs.PatternRule(expr, allowDash)
	if err != nil {
		t.Fatalf("PatternRule(%q) = %v", expr, err)
	}
	return r
}

func mustRange(t *testing.T, lo, hi int64) scriptargs.Rule {
	t.Helper()
	r, err := scriptargs.RangeRule(lo, hi)
	if err != nil {
		t.Fatalf("RangeRule(%d, %d) = %v", lo, hi, err)
	}
	return r
}

func mustEnum(t *testing.T, values ...string) scriptargs.Rule {
	t.Helper()
	r, err := scriptargs.EnumRule(values)
	if err != nil {
		t.Fatalf("EnumRule(%q) = %v", values, err)
	}
	return r
}

func TestPatternRule(t *testing.T) {
	if _, err := scriptargs.PatternRule("(", false); err == nil || strings.Contains(err.Error(), "(") {
		t.Errorf("PatternRule(bad) = %v, want a fixed reason", err)
	}
	// An expression that would close the anchoring group is refused.
	if _, err := scriptargs.PatternRule("a)|(b", false); err == nil {
		t.Error("PatternRule(a)|(b) accepted")
	}
	r := mustPattern(t, "a|b", false)
	tmpl, err := scriptargs.NewTemplate([]string{"/bin/p", "{x}"}, map[string]scriptargs.Param{"x": {Rule: r}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmpl.Bind(map[string]string{"x": "ab"}); err == nil {
		t.Error("an alternation matched beyond its anchors")
	}
	if _, err := scriptargs.PatternRule("", false); err == nil {
		t.Error("PatternRule(empty) accepted")
	}
	if _, err := scriptargs.PatternRule(strings.Repeat("a", scriptargs.MaxPatternBytes+1), false); err == nil {
		t.Error("PatternRule(long) accepted")
	}
	if _, err := scriptargs.PatternRule(strings.Repeat("a", scriptargs.MaxPatternBytes), false); err != nil {
		t.Errorf("PatternRule(at the bound) = %v", err)
	}
}

func TestRangeRule(t *testing.T) {
	if _, err := scriptargs.RangeRule(5, 4); err == nil {
		t.Error("RangeRule(5, 4) accepted")
	}
	if _, err := scriptargs.RangeRule(4, 4); err != nil {
		t.Errorf("RangeRule(4, 4) = %v", err)
	}
}

func TestEnumRule(t *testing.T) {
	bad := [][]string{
		nil,
		{"a", "a"},
		{""},
		{"a\nb"},
		make([]string, scriptargs.MaxEnumValues+1),
	}
	for i := range bad[4] {
		bad[4][i] = "v" + strings.Repeat("x", i)
	}
	for _, values := range bad {
		if _, err := scriptargs.EnumRule(values); err == nil {
			t.Errorf("EnumRule(%d values) accepted", len(values))
		}
	}
}

func TestNewTemplate(t *testing.T) {
	port := scriptargs.Param{Rule: mustRange(t, 1, 65535)}
	cases := []struct {
		name   string
		argv   []string
		params map[string]scriptargs.Param
		arg    int
		param  string
	}{
		{"ok", []string{"/bin/p", "-p", "{port}"}, map[string]scriptargs.Param{"port": port}, 0, ""},
		{"used twice", []string{"/bin/p", "{port}", "{port}"}, map[string]scriptargs.Param{"port": port}, 0, ""},
		{"no program", nil, nil, -1, ""},
		{"program placeholder", []string{"{port}"}, map[string]scriptargs.Param{"port": port}, 0, ""},
		{"program embeds one", []string{"/bin/{port}"}, map[string]scriptargs.Param{"port": port}, 0, ""},
		{"undeclared", []string{"/bin/p", "{port}", "{mount}"}, map[string]scriptargs.Param{"port": port}, 2, ""},
		{"embedded", []string{"/bin/p", "--port={port}"}, map[string]scriptargs.Param{"port": port}, 1, ""},
		{"unused", []string{"/bin/p", "{port}"}, map[string]scriptargs.Param{"port": port, "mount": port}, -1, "mount"},
		{"bad name", []string{"/bin/p", "{port}"}, map[string]scriptargs.Param{"port": port, "Bad": port}, -1, "Bad"},
		{"default fails rule", []string{"/bin/p", "{port}"}, map[string]scriptargs.Param{"port": {Rule: port.Rule, Default: "0", HasDefault: true}}, -1, "port"},
		{"default fails shape", []string{"/bin/p", "{port}"}, map[string]scriptargs.Param{"port": {Rule: mustPattern(t, ".*", false), Default: "", HasDefault: true}}, -1, "port"},
	}
	for _, c := range cases {
		_, err := scriptargs.NewTemplate(c.argv, c.params)
		var te *scriptargs.TemplateError
		switch {
		case c.name == "ok" || c.name == "used twice":
			if err != nil {
				t.Errorf("%s: NewTemplate = %v", c.name, err)
			}
		case !errors.As(err, &te):
			t.Errorf("%s: NewTemplate = %v, want a TemplateError", c.name, err)
		case te.Arg != c.arg || te.Param != c.param:
			t.Errorf("%s: TemplateError = %+v, want arg %d param %q", c.name, te, c.arg, c.param)
		}
	}
	many := map[string]scriptargs.Param{}
	argv := []string{"/bin/p"}
	for i := range scriptargs.MaxPlaceholders + 1 {
		name := "p" + strings.Repeat("x", i)
		many[name] = port
		argv = append(argv, "{"+name+"}")
	}
	if _, err := scriptargs.NewTemplate(argv, many); err == nil {
		t.Error("NewTemplate with too many placeholders accepted")
	}
}

// secretValue is planted in every refused value: no error may quote it.
const secretValue = "Va1ue-Secret"

func TestBind(t *testing.T) {
	tmpl, err := scriptargs.NewTemplate(
		[]string{"/usr/lib/check_tcp", "-H", "127.0.0.1", "-p", "{port}", "-m", "{mode}", "{path}", "{offset}", "{signed}", "{port}"},
		map[string]scriptargs.Param{
			"port":   {Rule: mustRange(t, 1, 65535), Default: "5432", HasDefault: true},
			"mode":   {Rule: mustEnum(t, "fast", "full", "-x")},
			"path":   {Rule: mustPattern(t, "/[^/].*", false), Default: "/var", HasDefault: true},
			"offset": {Rule: mustPattern(t, "-?[0-9]+", true), Default: "0", HasDefault: true},
			"signed": {Rule: mustRange(t, -10, 10), Default: "0", HasDefault: true},
		})
	if err != nil {
		t.Fatalf("NewTemplate = %v", err)
	}
	cases := []struct {
		name   string
		values map[string]string
		want   []string // nil: refused
		reason string   // part of the refusal
	}{
		{"defaults", map[string]string{"mode": "fast"},
			[]string{"/usr/lib/check_tcp", "-H", "127.0.0.1", "-p", "5432", "-m", "fast", "/var", "0", "0", "5432"}, ""},
		{"all given", map[string]string{"port": "8080", "mode": "-x", "path": "/srv/a b;rm -rf /", "offset": "-3", "signed": "-10"},
			[]string{"/usr/lib/check_tcp", "-H", "127.0.0.1", "-p", "8080", "-m", "-x", "/srv/a b;rm -rf /", "-3", "-10", "8080"}, ""},
		{"canonical number", map[string]string{"mode": "fast", "port": "+080", "signed": "-0"},
			[]string{"/usr/lib/check_tcp", "-H", "127.0.0.1", "-p", "80", "-m", "fast", "/var", "0", "0", "80"}, ""},
		{"missing without default", map[string]string{}, nil, `argument "mode" needs a value`},
		{"range low", map[string]string{"mode": "fast", "port": "0"}, nil, `argument "port" must be a whole number from 1 to 65535`},
		{"range high", map[string]string{"mode": "fast", "port": "65536"}, nil, `from 1 to 65535`},
		{"range text", map[string]string{"mode": "fast", "port": "80" + secretValue}, nil, `from 1 to 65535`},
		{"range overflow", map[string]string{"mode": "fast", "port": "99999999999999999999"}, nil, `from 1 to 65535`},
		{"range negative not allowed", map[string]string{"mode": "fast", "port": "-1"}, nil, `from 1 to 65535`},
		{"range spaces", map[string]string{"mode": "fast", "port": " 80"}, nil, `from 1 to 65535`},
		{"enum", map[string]string{"mode": "Fast" + secretValue}, nil, `argument "mode" is not one of the values this host allows`},
		{"pattern", map[string]string{"mode": "fast", "path": "rel/" + secretValue}, nil, `argument "path" does not match the pattern this host allows`},
		{"pattern is anchored", map[string]string{"mode": "fast", "offset": "1x" + secretValue}, nil, `argument "offset" does not match`},
		{"option injection", map[string]string{"mode": "fast", "path": "--" + secretValue}, nil, `argument "path" must not start with '-'`},
		{"option injection a pattern would admit", map[string]string{"mode": "fast", "path": "-/x"}, nil, `must not start with '-'`},
		{"control", map[string]string{"mode": "fast", "path": "/a\n" + secretValue}, nil, `argument "path" must not contain control characters`},
		{"empty", map[string]string{"mode": "fast", "path": ""}, nil, `argument "path" must not be empty`},
		{"long", map[string]string{"mode": "fast", "path": "/" + strings.Repeat(secretValue, 60)}, nil, `argument "path" is longer than 512 bytes`},
		{"undeclared", map[string]string{"mode": "fast", "user": secretValue}, nil, `argument "user" is not one this command takes on this host`},
		{"bad name", map[string]string{"mode": "fast", "Us" + secretValue: "x"}, nil, `an argument name is invalid`},
	}
	for _, c := range cases {
		got, err := tmpl.Bind(c.values)
		switch {
		case c.want != nil && (err != nil || !reflect.DeepEqual(got, c.want)):
			t.Errorf("%s: Bind = %q, %v; want %q", c.name, got, err, c.want)
		case c.want == nil && (err == nil || got != nil):
			t.Errorf("%s: Bind = %q, %v; want a refusal", c.name, got, err)
		case c.want == nil && !strings.Contains(err.Error(), c.reason):
			t.Errorf("%s: Bind error = %q, want it to contain %q", c.name, err, c.reason)
		case c.want == nil && strings.Contains(err.Error(), secretValue):
			t.Errorf("%s: Bind error %q quotes the value", c.name, err)
		}
	}
}

func TestBindLeavesTheTemplateAlone(t *testing.T) {
	tmpl, err := scriptargs.NewTemplate([]string{"/bin/p", "{a}"}, map[string]scriptargs.Param{"a": {Rule: mustEnum(t, "x", "y")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmpl.Bind(map[string]string{"a": "x"}); err != nil {
		t.Fatal(err)
	}
	if got, err := tmpl.Bind(map[string]string{"a": "y"}); err != nil || got[1] != "y" {
		t.Fatalf("second Bind = %q, %v", got, err)
	}
	argv := tmpl.Argv()
	argv[1] = "changed"
	if tmpl.Argv()[1] != "{a}" {
		t.Fatal("Argv returned the template's own slice")
	}
	if got := tmpl.Placeholders(); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("Placeholders = %q", got)
	}
}

func TestBindFixedCommand(t *testing.T) {
	tmpl := scriptargs.Fixed([]string{"/bin/p", "{port}", "x"})
	if got, err := tmpl.Bind(nil); err != nil || !reflect.DeepEqual(got, []string{"/bin/p", "{port}", "x"}) {
		t.Fatalf("Bind(nil) = %q, %v", got, err)
	}
	_, err := tmpl.Bind(map[string]string{"port": secretValue})
	if err == nil || err.Error() != "this command takes no arguments on this host" {
		t.Fatalf("Bind(values) = %v", err)
	}
	if (scriptargs.Template{}).Program() != "" || tmpl.Program() != "/bin/p" {
		t.Fatal("Program")
	}
}

func TestBindBatchPrograms(t *testing.T) {
	param := map[string]scriptargs.Param{"a": {Rule: mustPattern(t, ".*", false)}}
	for _, prog := range []string{`C:\scripts\x.bat`, `C:\scripts\X.CMD`, `C:\Windows\System32\cmd.exe`, `C:\Windows\System32\CMD.EXE. `, "/opt/x.bat"} {
		tmpl, err := scriptargs.NewTemplate([]string{prog, "{a}"}, param)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range []string{`a"b`, "a%PATH%", "a^b", "a&calc", "a|b", "a<b", "a>b", "a!b", "(a)"} {
			if _, err := tmpl.Bind(map[string]string{"a": v}); err == nil || !strings.Contains(err.Error(), "batch") {
				t.Errorf("%s: Bind(%q) = %v, want a batch refusal", prog, v, err)
			}
		}
		if _, err := tmpl.Bind(map[string]string{"a": "C:/data/x y.txt"}); err != nil {
			t.Errorf("%s: Bind(plain) = %v", prog, err)
		}
	}
	tmpl, err := scriptargs.NewTemplate([]string{`C:\tools\check.exe`, "{a}"}, param)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmpl.Bind(map[string]string{"a": "a&b (x86)"}); err != nil {
		t.Errorf("exe: Bind = %v, want it passed", err)
	}
	// A default is held to the same rule at load.
	_, err = scriptargs.NewTemplate([]string{`C:\x.cmd`, "{a}"}, map[string]scriptargs.Param{"a": {Rule: mustPattern(t, ".*", false), Default: "a&b", HasDefault: true}})
	if err == nil {
		t.Error("a batch-unsafe default was accepted")
	}
}
