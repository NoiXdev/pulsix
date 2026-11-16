package assert_test

import (
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/probe/assert"
)

// TestParseAccepts: every subject with every op it takes, the operands,
// the severity and the canonical expression and entry each parses to.
func TestParseAccepts(t *testing.T) {
	cases := []struct {
		entry    string
		subject  assert.Subject
		arg      string
		op       assert.Op
		operand  string
		severity assert.Severity
		expr     string
	}{
		// Numeric subjects with every numeric op.
		{"ok=status == 200", assert.SubjectStatus, "", assert.OpEq, "200", assert.Crit, "status == 200"},
		{"ok=status != 503", assert.SubjectStatus, "", assert.OpNe, "503", assert.Crit, "status != 503"},
		{"ok=status < 400", assert.SubjectStatus, "", assert.OpLt, "400", assert.Crit, "status < 400"},
		{"ok=status <= 399", assert.SubjectStatus, "", assert.OpLe, "399", assert.Crit, "status <= 399"},
		{"ok=status > 199", assert.SubjectStatus, "", assert.OpGt, "199", assert.Crit, "status > 199"},
		{"ok=status >= 200", assert.SubjectStatus, "", assert.OpGe, "200", assert.Crit, "status >= 200"},
		{"ok=status in 200..299", assert.SubjectStatus, "", assert.OpIn, "200..299", assert.Crit, "status in 200..299"},
		{"fast=response_ms < 1500 @warn", assert.SubjectResponseMS, "", assert.OpLt, "1500", assert.Warn, "response_ms < 1500"},
		{"ttfb=ttfb_ms <= 800.5", assert.SubjectTTFBMS, "", assert.OpLe, "800.5", assert.Crit, "ttfb_ms <= 800.5"},
		{"small=size < 1048576", assert.SubjectSize, "", assert.OpLt, "1048576", assert.Crit, "size < 1048576"},
		{"direct=redirects == 0", assert.SubjectRedirects, "", assert.OpEq, "0", assert.Crit, "redirects == 0"},
		{"cert=cert_days > 14 @warn", assert.SubjectCertDays, "", assert.OpGt, "14", assert.Warn, "cert_days > 14"},
		{"cert=cert_days in -1.5..+30", assert.SubjectCertDays, "", assert.OpIn, "-1.5..30", assert.Crit, "cert_days in -1.5..30"},
		{"one=html(\"h1\") count == 1", assert.SubjectHTMLCount, "h1", assert.OpEq, "1", assert.Crit, `html("h1") count == 1`},
		{"rows=html(\"tr\") count in 1..10", assert.SubjectHTMLCount, "tr", assert.OpIn, "1..10", assert.Crit, `html("tr") count in 1..10`},
		// Text subjects with every text op.
		{`ca=cert_issuer == "R3"`, assert.SubjectCertIssuer, "", assert.OpEq, `"R3"`, assert.Crit, `cert_issuer == "R3"`},
		{`ca=cert_issuer != "evil"`, assert.SubjectCertIssuer, "", assert.OpNe, `"evil"`, assert.Crit, `cert_issuer != "evil"`},
		{`b=body contains "Welcome"`, assert.SubjectBody, "", assert.OpContains, `"Welcome"`, assert.Crit, `body contains "Welcome"`},
		{`b=body !contains "Exception"`, assert.SubjectBody, "", assert.OpNotContains, `"Exception"`, assert.Crit, `body !contains "Exception"`},
		{`b=body matches "v[0-9]+"`, assert.SubjectBody, "", assert.OpMatches, `"v[0-9]+"`, assert.Crit, `body matches "v[0-9]+"`},
		{`b=body !matches "(?i)error"`, assert.SubjectBody, "", assert.OpNotMatches, `"(?i)error"`, assert.Crit, `body !matches "(?i)error"`},
		{`b=body == ""`, assert.SubjectBody, "", assert.OpEq, `""`, assert.Crit, `body == ""`},
		{`ct=header("Content-Type") contains "json"`, assert.SubjectHeader, "Content-Type", assert.OpContains, `"json"`, assert.Crit, `header("Content-Type") contains "json"`},
		{`hsts=header("strict-transport-security") exists`, assert.SubjectHeader, "strict-transport-security", assert.OpExists, "", assert.Crit, `header("strict-transport-security") exists`},
		{`nopow=header("X-Powered-By") !exists @warn`, assert.SubjectHeader, "X-Powered-By", assert.OpNotExists, "", assert.Warn, `header("X-Powered-By") !exists`},
		{`c=header("Set-Cookie") !contains "debug"`, assert.SubjectHeader, "Set-Cookie", assert.OpNotContains, `"debug"`, assert.Crit, `header("Set-Cookie") !contains "debug"`},
		{`st=json("$.status") == "ok"`, assert.SubjectJSON, "$.status", assert.OpEq, `"ok"`, assert.Crit, `json("$.status") == "ok"`},
		{`st=json("$.checks[?@.name=='db'].status") != "down"`, assert.SubjectJSON, "$.checks[?@.name=='db'].status", assert.OpNe, `"down"`, assert.Crit, `json("$.checks[?@.name=='db'].status") != "down"`},
		{`v=json("$.version") matches "^2\\."`, assert.SubjectJSON, "$.version", assert.OpMatches, `"^2\\."`, assert.Crit, `json("$.version") matches "^2\\."`},
		{`v=json("$.data") exists`, assert.SubjectJSON, "$.data", assert.OpExists, "", assert.Crit, `json("$.data") exists`},
		{`v=json("$.error") !exists`, assert.SubjectJSON, "$.error", assert.OpNotExists, "", assert.Crit, `json("$.error") !exists`},
		{`t=html("title") == "Shop"`, assert.SubjectHTML, "title", assert.OpEq, `"Shop"`, assert.Crit, `html("title") == "Shop"`},
		{`t=html("div.alert, #error") !exists`, assert.SubjectHTML, "div.alert, #error", assert.OpNotExists, "", assert.Crit, `html("div.alert, #error") !exists`},
		{`t=html("h1") !matches "(?i)maintenance"`, assert.SubjectHTML, "h1", assert.OpNotMatches, `"(?i)maintenance"`, assert.Crit, `html("h1") !matches "(?i)maintenance"`},
		// Spacing, the canonical number and @crit canonicalised away.
		{"ok=  status   ==  +200.0  ", assert.SubjectStatus, "", assert.OpEq, "200", assert.Crit, "status == 200"},
		{"ok=status == 200 @crit", assert.SubjectStatus, "", assert.OpEq, "200", assert.Crit, "status == 200"},
		{"ok=status==200", assert.SubjectStatus, "", assert.OpEq, "200", assert.Crit, "status == 200"},
		{"x=status in 5..5", assert.SubjectStatus, "", assert.OpIn, "5..5", assert.Crit, "status in 5..5"},
		{"x=size > -0", assert.SubjectSize, "", assert.OpGt, "0", assert.Crit, "size > 0"},
		{"A_b.c-9=redirects < 3", assert.SubjectRedirects, "", assert.OpLt, "3", assert.Crit, "redirects < 3"},
		// JSON string escapes: the operand is the decoded text, printed back
		// canonically (no HTML escaping, \u only where JSON needs it).
		{`e=body contains "a\"b\\cé\n<&>"`, assert.SubjectBody, "", assert.OpContains, `"a\"b\\cé\n<&>"`, assert.Crit, `body contains "a\"b\\cé\n<&>"`},
		{`e=body contains "@warn"`, assert.SubjectBody, "", assert.OpContains, `"@warn"`, assert.Crit, `body contains "@warn"`},
		{`e=body contains " @warn" @warn`, assert.SubjectBody, "", assert.OpContains, `" @warn"`, assert.Warn, `body contains " @warn"`},
		{`e=html("a[title=\"x y\"]") count >= 2`, assert.SubjectHTMLCount, `a[title="x y"]`, assert.OpGe, "2", assert.Crit, `html("a[title=\"x y\"]") count >= 2`},
	}
	for _, tc := range cases {
		t.Run(tc.entry, func(t *testing.T) {
			a, err := assert.Parse(tc.entry)
			if err != nil {
				t.Fatalf("Parse(%q) = %v", tc.entry, err)
			}
			name, _, _ := strings.Cut(tc.entry, "=")
			if a.Name() != name || a.Subject() != tc.subject || a.Arg() != tc.arg || a.Op() != tc.op ||
				a.Operand() != tc.operand || a.Severity() != tc.severity || a.Expression() != tc.expr {
				t.Fatalf("Parse(%q) = name %q subject %q arg %q op %q operand %q severity %q expr %q",
					tc.entry, a.Name(), a.Subject(), a.Arg(), a.Op(), a.Operand(), a.Severity(), a.Expression())
			}
			// The canonical entry parses back to itself.
			entry := a.Entry()
			again, err := assert.Parse(entry)
			if err != nil || again.Entry() != entry {
				t.Fatalf("Parse(Entry() %q) = %q, %v", entry, again.Entry(), err)
			}
			wantEntry := name + "=" + tc.expr
			if tc.severity == assert.Warn {
				wantEntry += " @warn"
			}
			if entry != wantEntry {
				t.Fatalf("Entry() = %q, want %q", entry, wantEntry)
			}
		})
	}
}

// TestParseRefuses: every refusal of the grammar and the bounds, each
// with a reason that names what is wrong.
func TestParseRefuses(t *testing.T) {
	cases := []struct {
		entry string
		want  string
	}{
		{"", "name"},
		{"status == 200", "name"},
		{"=status == 200", "name"},
		{strings.Repeat("n", 65) + "=status == 200", "name"},
		{"a b=status == 200", "name"},
		{"ä=status == 200", "name"},
		{"ok=", "subject"},
		{"ok=   ", "subject"},
		{"ok=code == 200", `unknown subject "code"`},
		{"ok=Status == 200", `unknown subject "Status"`},
		{"ok=status", "operator"},
		{"ok=status = 200", `"=" is not an operator of status`},
		{"ok=status contains \"2\"", `"contains" is not an operator of status`},
		{"ok=status exists", `"exists" is not an operator of status`},
		{`ok=body < 3`, `"<" is not an operator of body`},
		{`ok=body exists`, `"exists" is not an operator of body`},
		{`ok=cert_issuer !exists`, `"!exists" is not an operator of cert_issuer`},
		{`ok=header("X") in 1..2`, `"in" is not an operator of header`},
		{`ok=html("p") count contains "x"`, `"contains" is not an operator of html count`},
		{"ok=status ==", "number"},
		{"ok=status == \"200\"", "number"},
		{"ok=status == 2e2", "number"},
		{"ok=status == .5", "number"},
		{"ok=status == 5.", "number"},
		{"ok=status == 0x10", "number"},
		{"ok=status == 1" + strings.Repeat("0", 400), "number"},
		{"ok=status in 200", "a..b"},
		{"ok=status in 300..200", "a..b with a <= b"},
		{"ok=status in 1..2..3", "a..b"},
		{"ok=status == 200 300", "after the operand"},
		{`ok=body contains`, "string"},
		{`ok=body contains Welcome`, "string"},
		{`ok=body contains "open`, "string"},
		{`ok=body contains "\x"`, "string"},
		{`ok=body contains "a" "b"`, "after the operand"},
		{`ok=header("X") exists "x"`, "no operand"},
		{`ok=status == 200 @info`, "after the operand"},
		{`ok=status == 200 @warn @warn`, "after the operand"},
		{`ok=status == 200 @Warn`, "after the operand"},
		{"ok=header(X) exists", "string"},
		{`ok=header("X" exists`, ")"},
		{`ok=header("") exists`, "header name"},
		{`ok=header("X Y") exists`, "header name"},
		{`ok=header("X:") exists`, "header name"},
		{`ok=header exists`, "header takes"},
		{`ok=status("x") == 1`, "takes no argument"},
		{`ok=json("status") exists`, "JSONPath"},
		{`ok=json("$[") exists`, "JSONPath"},
		{`ok=json("$..a..b") exists`, "JSONPath"},
		{`ok=html("") exists`, "selector"},
		{`ok=html("div[") exists`, "selector"},
		{`ok=html("p:nope") exists`, "selector"},
		{`ok=html("` + strings.Repeat("p", 257) + `") exists`, "256 bytes"},
		{`ok=body matches "("`, "regular expression"},
		{`ok=body matches "a{2,1}"`, "regular expression"},
		{`ok=body matches "` + strings.Repeat("a", 257) + `"`, "256 bytes"},
		{"ok=body contains \"a\tb\"", "control"},
		{"ok=status == 200\n", "control"},
		{"ok=body contains \"\xff\"", "UTF-8"},
		{"ok=status == 200 " + strings.Repeat(" ", 512), "512 bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.entry, func(t *testing.T) {
			_, err := assert.Parse(tc.entry)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse(%q) = %v, want an error containing %q", tc.entry, err, tc.want)
			}
		})
	}
}

// TestParseBounds: the longest entry, regular expression and selector
// still accepted.
func TestParseBounds(t *testing.T) {
	ok := []string{
		`ok=body matches "` + strings.Repeat("a", 256) + `"`,
		`ok=html("` + strings.Repeat("p", 256) + `") exists`,
		"ok=status == 200" + strings.Repeat(" ", 512-len("ok=status == 200")),
	}
	for _, entry := range ok {
		if _, err := assert.Parse(entry); err != nil {
			t.Errorf("Parse(%d bytes) = %v", len(entry), err)
		}
	}
}

// TestSubjectKinds: which subjects compare numbers, read the body at all
// (refused with HEAD) and need its first MiB kept.
func TestSubjectKinds(t *testing.T) {
	cases := []struct {
		s                   assert.Subject
		numeric, body, kept bool
	}{
		{assert.SubjectStatus, true, false, false},
		{assert.SubjectResponseMS, true, false, false},
		{assert.SubjectTTFBMS, true, false, false},
		{assert.SubjectSize, true, true, false},
		{assert.SubjectRedirects, true, false, false},
		{assert.SubjectCertDays, true, false, false},
		{assert.SubjectCertIssuer, false, false, false},
		{assert.SubjectBody, false, true, true},
		{assert.SubjectHeader, false, false, false},
		{assert.SubjectJSON, false, true, true},
		{assert.SubjectHTML, false, true, true},
		{assert.SubjectHTMLCount, true, true, true},
	}
	for _, tc := range cases {
		if tc.s.Numeric() != tc.numeric || tc.s.ReadsBody() != tc.body || tc.s.KeepsBody() != tc.kept {
			t.Errorf("%s: numeric %v body %v kept %v", tc.s, tc.s.Numeric(), tc.s.ReadsBody(), tc.s.KeepsBody())
		}
	}
}

// TestEntryName is the item an entry reports on, whether it parses or not.
func TestEntryName(t *testing.T) {
	cases := map[string]string{
		"ok=status == 200": "ok",
		"ok=nonsense":      "ok",
		"no-equals":        "no-equals",
		"=x":               "",
	}
	for entry, want := range cases {
		if got := assert.EntryName(entry); got != want {
			t.Errorf("EntryName(%q) = %q, want %q", entry, got, want)
		}
	}
}
