package scriptargs_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/scriptargs"
)

// Fix round 1 of the security review (plan R15–R21).

func oneArg(t *testing.T, prog string, rule scriptargs.Rule) scriptargs.Template {
	t.Helper()
	tmpl, err := scriptargs.NewTemplate([]string{prog, "{a}"}, map[string]scriptargs.Param{"a": {Rule: rule}})
	if err != nil {
		t.Fatalf("NewTemplate(%s) = %v", prog, err)
	}
	return tmpl
}

func pattern(t *testing.T, expr string, o scriptargs.PatternOptions) scriptargs.Rule {
	t.Helper()
	r, err := scriptargs.PatternRule(expr, o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func bindErr(tmpl scriptargs.Template, v string) error {
	_, err := tmpl.Bind(map[string]string{"a": v})
	return err
}

// TestValueProblemRefusesInvisibleCharacters: format characters (soft
// hyphen, bidi overrides, zero width), line and paragraph separators and
// non-characters fail the shape on both sides, whatever the rule.
func TestValueProblemRefusesInvisibleCharacters(t *testing.T) {
	for _, v := range []string{"a\u00adb", "a\u202eb", "a\u2066b", "a\u200bb", "a\ufeffb", "a\u2028b", "a\u2029b", "a\ufdd0b", "a\ufffeb", "a\U0001ffffb"} {
		if got := scriptargs.ValueProblem(v); got != scriptargs.ProblemControl {
			t.Errorf("ValueProblem(%q) = %q, want %q", v, got, scriptargs.ProblemControl)
		}
	}
	for _, v := range []string{"grüße", "日本", "a\u00a0b"} {
		if got := scriptargs.ValueProblem(v); got != "" {
			t.Errorf("ValueProblem(%q) = %q", v, got)
		}
	}
}

// TestBindPrintableASCIIByDefault: without unicode: true a value is
// printable ASCII, so no character a Windows code page best-fits into a
// quote, a dash or a backslash reaches a program.
func TestBindPrintableASCIIByDefault(t *testing.T) {
	ascii := oneArg(t, "/usr/bin/x", pattern(t, ".+", scriptargs.PatternOptions{}))
	for _, v := range []string{"grüße", "a\uff02 --evil \uff02b", "\uff0d-evil", "a\u00a5b", "a\u3000b"} {
		if err := bindErr(ascii, v); err == nil || !strings.Contains(err.Error(), `argument "a" must be printable ASCII`) {
			t.Errorf("Bind(%q) = %v, want the ASCII refusal", v, err)
		}
	}
	if err := bindErr(ascii, " ~!/a b"); err != nil {
		t.Errorf("Bind(printable ASCII) = %v", err)
	}
	wide := oneArg(t, "/usr/bin/x", pattern(t, ".+", scriptargs.PatternOptions{Unicode: true}))
	if err := bindErr(wide, "grüße 日本"); err != nil {
		t.Errorf("unicode: Bind = %v", err)
	}
	// unicode: true still refuses the invisible characters.
	for _, v := range []string{"\u00ad-evil", "a\u202eb", "a\u2028b", "a\ufffeb"} {
		if err := bindErr(wide, v); err == nil {
			t.Errorf("unicode: Bind(%q) accepted", v)
		}
	}
	if _, err := scriptargs.EnumRule([]string{"fast", "grüße"}, false); err == nil {
		t.Error("EnumRule accepted a non-ASCII value without unicode")
	}
	enum, err := scriptargs.EnumRule([]string{"fast", "grüße"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindErr(oneArg(t, "/usr/bin/x", enum), "grüße"); err != nil {
		t.Errorf("unicode enum: Bind = %v", err)
	}
	// A range is digits: a fullwidth digit is no number and no ASCII.
	r, _ := scriptargs.RangeRule(1, 9)
	if err := bindErr(oneArg(t, "/usr/bin/x", r), "\uff15"); err == nil {
		t.Error("range accepted a fullwidth digit")
	}
}

// TestBindBatchRefusesEveryMetacharacter: cmd.exe's own syntax and its
// argument delimiters, one character at a time.
func TestBindBatchRefusesEveryMetacharacter(t *testing.T) {
	for _, prog := range []string{`C:\checks\check.bat`, `C:\checks\check.bat::$DATA`, `C:\checks\x.cmd:stream`} {
		tmpl := oneArg(t, prog, pattern(t, ".+", scriptargs.PatternOptions{}))
		for _, c := range []string{`"`, "%", "^", "&", "|", "<", ">", "!", "(", ")", ",", ";", "="} {
			if err := bindErr(tmpl, "a"+c+"b"); err == nil || !strings.Contains(err.Error(), "batch") {
				t.Errorf("%s: Bind(a%sb) = %v, want the batch refusal", prog, c, err)
			}
		}
		for _, v := range []string{"a\rb", "a\nb"} {
			if err := bindErr(tmpl, v); err == nil {
				t.Errorf("%s: Bind(%q) accepted", prog, v)
			}
		}
	}
	exe := oneArg(t, `C:\checks\check.exe`, pattern(t, ".+", scriptargs.PatternOptions{}))
	if err := bindErr(exe, "a,b;c=d"); err != nil {
		t.Errorf("exe: Bind = %v", err)
	}
}

// TestBindLeadingSlashOnWindows: a value starting with '/' is a switch to
// many Windows programs, so a Windows program's pattern refuses it as it
// refuses '-'; a Unix program takes it as a path.
func TestBindLeadingSlashOnWindows(t *testing.T) {
	for _, prog := range []string{`C:\tools\x.exe`, `c:/tools/x.exe`, `\\server\share\x.exe`} {
		tmpl := oneArg(t, prog, pattern(t, ".+", scriptargs.PatternOptions{}))
		if err := bindErr(tmpl, "/delete"); err == nil || !strings.Contains(err.Error(), `argument "a" must not start with '/'`) {
			t.Errorf("%s: Bind(/delete) = %v", prog, err)
		}
		allowed := oneArg(t, prog, pattern(t, ".+", scriptargs.PatternOptions{AllowLeadingDash: true}))
		if err := bindErr(allowed, "/delete"); err != nil {
			t.Errorf("%s: allowed: Bind = %v", prog, err)
		}
	}
	if err := bindErr(oneArg(t, "/usr/bin/x", pattern(t, ".+", scriptargs.PatternOptions{})), "/srv/data"); err != nil {
		t.Errorf("unix: Bind(/srv/data) = %v", err)
	}
}

// TestBindDotDotSegments: a ".." path segment, written with either
// separator, is refused unless the rule allows it.
func TestBindDotDotSegments(t *testing.T) {
	strict := oneArg(t, "/usr/bin/x", pattern(t, ".+", scriptargs.PatternOptions{}))
	for _, v := range []string{"..", "../x", "/var/log/../../etc/shadow", "/var/..", `a\..\b`, `..\x`, `C:\a\..`, "a/../b",
		// Re-review N1: ':' separates too, and dots with spaces count.
		".. ", "...", "a/.../b", ". .", `C:..\x`, "..:stream", "x:..", "/a/ ../b"} {
		if err := bindErr(strict, v); err == nil || !strings.Contains(err.Error(), `argument "a" must not hold a ".." path segment`) {
			t.Errorf("Bind(%q) = %v", v, err)
		}
	}
	for _, v := range []string{"a..b", "/var/log/x..y", "..a", ".", " . ", "a.b:c", "/x/.hidden"} {
		if err := bindErr(strict, v); err != nil {
			t.Errorf("Bind(%q) = %v", v, err)
		}
	}
	allowed := oneArg(t, "/usr/bin/x", pattern(t, ".+", scriptargs.PatternOptions{AllowDotDot: true}))
	if err := bindErr(allowed, "../x"); err != nil {
		t.Errorf("allowed: Bind = %v", err)
	}
}

// TestBindNamed: which argument each placeholder became, so the caller
// can word a later refusal by the placeholder's name alone.
func TestBindNamed(t *testing.T) {
	tmpl, err := scriptargs.NewTemplate([]string{"/usr/bin/x", "-p", "{port}", "{path}", "{port}"}, map[string]scriptargs.Param{
		"port": {Rule: pattern(t, "[0-9]+", scriptargs.PatternOptions{})},
		"path": {Rule: pattern(t, "/.+", scriptargs.PatternOptions{})},
	})
	if err != nil {
		t.Fatal(err)
	}
	argv, names, err := tmpl.BindNamed(map[string]string{"port": "1", "path": "/a"})
	if err != nil || !reflect.DeepEqual(argv, []string{"/usr/bin/x", "-p", "1", "/a", "1"}) ||
		!reflect.DeepEqual(names, []string{"", "", "port", "path", "port"}) {
		t.Fatalf("BindNamed = %q, %q, %v", argv, names, err)
	}
	argv, names, err = scriptargs.Fixed([]string{"/usr/bin/x", "{port}"}).BindNamed(nil)
	if err != nil || !reflect.DeepEqual(names, []string{"", ""}) || argv[1] != "{port}" {
		t.Fatalf("fixed BindNamed = %q, %q, %v", argv, names, err)
	}
}

// TestBindLeadingDashAfterSpaces: the option check looks past leading
// spaces, which a program's own parsing may skip (re-review N2).
func TestBindLeadingDashAfterSpaces(t *testing.T) {
	tmpl := oneArg(t, "/usr/bin/x", pattern(t, ".+", scriptargs.PatternOptions{}))
	for _, v := range []string{" -x", "   --evil"} {
		if err := bindErr(tmpl, v); err == nil || !strings.Contains(err.Error(), "must not start with '-'") {
			t.Errorf("Bind(%q) = %v", v, err)
		}
	}
	win := oneArg(t, `C:\tools\x.exe`, pattern(t, ".+", scriptargs.PatternOptions{}))
	if err := bindErr(win, "  /delete"); err == nil || !strings.Contains(err.Error(), "must not start with '/'") {
		t.Errorf("windows: Bind(  /delete) = %v", err)
	}
	if err := bindErr(tmpl, " a-b"); err != nil {
		t.Errorf("Bind( a-b) = %v", err)
	}
}
