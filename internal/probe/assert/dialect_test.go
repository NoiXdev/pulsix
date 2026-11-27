package assert_test

import (
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/probe/assert"
)

// TestDialects (backlog 3.87): each plugin's dialect knows its own
// subjects and only those; http's is what Parse has always parsed.
func TestDialects(t *testing.T) {
	cases := []struct {
		plugin string
		want   []assert.Subject
	}{
		{"http", []assert.Subject{
			assert.SubjectStatus, assert.SubjectResponseMS, assert.SubjectTTFBMS, assert.SubjectSize,
			assert.SubjectRedirects, assert.SubjectCertDays, assert.SubjectCertIssuer, assert.SubjectBody,
			assert.SubjectHeader, assert.SubjectJSON, assert.SubjectHTML,
		}},
		{"tcp", []assert.Subject{assert.SubjectConnectMS, assert.SubjectResponseMS, assert.SubjectResponse}},
		{"dns", []assert.Subject{assert.SubjectResolveMS, assert.SubjectAnswerCount, assert.SubjectAnswers, assert.SubjectAnswer}},
		{"tls", []assert.Subject{
			assert.SubjectHandshakeMS, assert.SubjectCertValid, assert.SubjectCertDays, assert.SubjectCertIssuer,
			assert.SubjectCertSubject, assert.SubjectCertSANs, assert.SubjectVersion, assert.SubjectCipher,
		}},
		{"snmp", []assert.Subject{assert.SubjectValue, assert.SubjectResponseMS, assert.SubjectUptime}},
	}
	for _, tc := range cases {
		d, ok := assert.DialectOf(tc.plugin)
		if !ok {
			t.Fatalf("DialectOf(%s) = false", tc.plugin)
		}
		got := d.Subjects()
		if strings.Join(subjectNames(got), ",") != strings.Join(subjectNames(tc.want), ",") {
			t.Errorf("%s subjects = %v, want %v", tc.plugin, got, tc.want)
		}
		if d.Plugin() != tc.plugin {
			t.Errorf("Plugin() = %q, want %q", d.Plugin(), tc.plugin)
		}
	}
	for _, plugin := range []string{"icmp", "cpu", ""} {
		if _, ok := assert.DialectOf(plugin); ok {
			t.Errorf("DialectOf(%q) = true, want no dialect", plugin)
		}
	}
	if got := assert.Plugins(); strings.Join(got, ",") != "http,tcp,dns,tls,snmp" {
		t.Errorf("Plugins() = %v", got)
	}
}

func subjectNames(ss []assert.Subject) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = string(s)
	}
	return out
}

// TestDialectParse: each dialect's subjects with their operators and
// operands, the canonical expression, and the refusals that keep the
// dialects apart.
func TestDialectParse(t *testing.T) {
	ok := []struct {
		plugin, entry, expr string
		subject             assert.Subject
		arg                 string
	}{
		{"tcp", "c=connect_ms < 200 @warn", "connect_ms < 200", assert.SubjectConnectMS, ""},
		{"tcp", "r=response_ms in 0..500", "response_ms in 0..500", assert.SubjectResponseMS, ""},
		{"tcp", `b=response matches "^SSH-2\\.0"`, `response matches "^SSH-2\\.0"`, assert.SubjectResponse, ""},
		{"tcp", `b=response !contains "error"`, `response !contains "error"`, assert.SubjectResponse, ""},
		{"dns", "t=resolve_ms <= 100", "resolve_ms <= 100", assert.SubjectResolveMS, ""},
		{"dns", "n=answer_count >= 2", "answer_count >= 2", assert.SubjectAnswerCount, ""},
		{"dns", `a=answers == "192.0.2.1, 192.0.2.2"`, `answers == "192.0.2.1, 192.0.2.2"`, assert.SubjectAnswers, ""},
		{"dns", `a=answer("192.0.2.1") exists`, `answer("192.0.2.1") exists`, assert.SubjectAnswer, "192.0.2.1"},
		{"dns", `p=answer("203.0.113.9") !exists @warn`, `answer("203.0.113.9") !exists`, assert.SubjectAnswer, "203.0.113.9"},
		{"tls", "h=handshake_ms < 300", "handshake_ms < 300", assert.SubjectHandshakeMS, ""},
		{"tls", "v=cert_valid == 1", "cert_valid == 1", assert.SubjectCertValid, ""},
		{"tls", "d=cert_days > 14", "cert_days > 14", assert.SubjectCertDays, ""},
		{"tls", `i=cert_issuer == "R3"`, `cert_issuer == "R3"`, assert.SubjectCertIssuer, ""},
		{"tls", `s=cert_subject == "example.com"`, `cert_subject == "example.com"`, assert.SubjectCertSubject, ""},
		{"tls", `n=cert_sans contains "www.example.com"`, `cert_sans contains "www.example.com"`, assert.SubjectCertSANs, ""},
		{"tls", "p=version >= 1.2", "version >= 1.2", assert.SubjectVersion, ""},
		{"tls", `c=cipher !matches "CBC"`, `cipher !matches "CBC"`, assert.SubjectCipher, ""},
		{"snmp", "r=response_ms < 200", "response_ms < 200", assert.SubjectResponseMS, ""},
		{"snmp", "u=uptime > 3600 @warn", "uptime > 3600", assert.SubjectUptime, ""},
		{"snmp", `l=value("load") < 80`, `value("load") < 80`, assert.SubjectValue, "load"},
		{"snmp", `l=value("load") in 0..80.5`, `value("load") in 0..80.5`, assert.SubjectValue, "load"},
		{"snmp", `s=value("state") == 2`, `value("state") == 2`, assert.SubjectValue, "state"},
		{"snmp", `s=value("state") == "online"`, `value("state") == "online"`, assert.SubjectValue, "state"},
		{"snmp", `s=value("state") != "on\tline"`, `value("state") != "on\tline"`, assert.SubjectValue, "state"},
		{"snmp", `d=value("sys.descr-1") contains "Linux"`, `value("sys.descr-1") contains "Linux"`, assert.SubjectValue, "sys.descr-1"},
		{"snmp", `d=value("descr") !matches "^Cisco"`, `value("descr") !matches "^Cisco"`, assert.SubjectValue, "descr"},
	}
	for _, tc := range ok {
		d, _ := assert.DialectOf(tc.plugin)
		a, err := d.Parse(tc.entry)
		if err != nil {
			t.Errorf("%s Parse(%q) = %v", tc.plugin, tc.entry, err)
			continue
		}
		if a.Expression() != tc.expr || a.Subject() != tc.subject || a.Arg() != tc.arg {
			t.Errorf("%s Parse(%q) = %q %s %q, want %q %s %q", tc.plugin, tc.entry, a.Expression(), a.Subject(), a.Arg(), tc.expr, tc.subject, tc.arg)
		}
	}

	refused := []struct{ plugin, entry, want string }{
		{"tcp", "ok=status == 200", `unknown subject "status" (tcp takes connect_ms, response_ms, response)`},
		{"tcp", `ok=body contains "x"`, `unknown subject "body"`},
		{"http", "ok=connect_ms < 2", `unknown subject "connect_ms" (http takes status, response_ms`},
		{"dns", `ok=answers exists`, `"exists" is not an operator of answers`},
		{"dns", `ok=answer("x") == "x"`, `"==" is not an operator of answer`},
		{"dns", `ok=answer("x") contains "x"`, `"contains" is not an operator of answer`},
		{"dns", `ok=answer exists`, `answer takes a quoted argument`},
		{"dns", `ok=answer("") exists`, "must not be empty"},
		{"dns", `ok=answer("  ") exists`, "must not be empty"},
		{"dns", `ok=answer("` + strings.Repeat("a", 257) + `") exists`, "at most 256 bytes"},
		{"dns", `ok=answers("x") contains "x"`, "answers takes no argument"},
		{"dns", "ok=answer_count == many", "number"},
		{"tls", `ok=version == "1.3"`, "number"},
		{"tls", `ok=cipher < 3`, `"<" is not an operator of cipher`},
		{"tls", "ok=ttfb_ms < 3", `unknown subject "ttfb_ms"`},
		{"tls", `ok=cert_sans exists`, `"exists" is not an operator of cert_sans`},
		{"snmp", "ok=status == 200", `unknown subject "status" (snmp takes value, response_ms, uptime)`},
		{"snmp", `ok=value exists`, "value takes a quoted argument"},
		{"snmp", `ok=value("x") exists`, `"exists" is not an operator of value`},
		{"snmp", `ok=value("x") !exists`, `"!exists" is not an operator of value`},
		{"snmp", `ok=value("") < 3`, "the name in value(…) must be an oids entry's name"},
		{"snmp", `ok=value("a b") < 3`, "the name in value(…) must be an oids entry's name"},
		{"snmp", `ok=value("x") < "3"`, "value takes a decimal number after"},
		{"snmp", `ok=value("x") contains 3`, "value takes a JSON string after"},
		{"snmp", `ok=value("x") == up`, `value takes a decimal number or a JSON string after "=="`},
		{"snmp", `ok=uptime == "3"`, "number"},
		{"snmp", `ok=uptime("x") > 3`, "uptime takes no argument"},
		{"http", `ok=value("x") < 3`, `unknown subject "value"`},
	}
	for _, tc := range refused {
		d, _ := assert.DialectOf(tc.plugin)
		if _, err := d.Parse(tc.entry); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s Parse(%q) = %v, want an error containing %q", tc.plugin, tc.entry, err, tc.want)
		}
	}
}

// TestNewSubjectKinds: the numeric subjects of the new dialects, and that
// none of them reads an http body.
func TestNewSubjectKinds(t *testing.T) {
	numeric := map[assert.Subject]bool{
		assert.SubjectConnectMS: true, assert.SubjectResolveMS: true, assert.SubjectAnswerCount: true,
		assert.SubjectHandshakeMS: true, assert.SubjectCertValid: true, assert.SubjectVersion: true,
		assert.SubjectResponse: false, assert.SubjectAnswers: false, assert.SubjectAnswer: false,
		assert.SubjectCertSubject: false, assert.SubjectCertSANs: false, assert.SubjectCipher: false,
	}
	for s, want := range numeric {
		if s.Numeric() != want || s.ReadsBody() || s.KeepsBody() {
			t.Errorf("%s: numeric %v (want %v), body %v, kept %v", s, s.Numeric(), want, s.ReadsBody(), s.KeepsBody())
		}
	}
}

// TestAssertionNumeric (backlog 3.134): whether an assertion compares
// numbers is its subject's kind, except for snmp's value, whose operator
// and operand decide.
func TestAssertionNumeric(t *testing.T) {
	for entry, want := range map[string]bool{
		`a=value("x") < 3`:            true,
		`a=value("x") in 1..2`:        true,
		`a=value("x") == 3`:           true,
		`a=value("x") != -3.5`:        true,
		`a=value("x") == "3"`:         false,
		`a=value("x") != "up"`:        false,
		`a=value("x") contains "u"`:   false,
		`a=value("x") !matches "^u"`:  false,
		`a=uptime > 3`:                true,
		`a=response_ms in 0..9 @warn`: true,
	} {
		a, err := assert.SNMP.Parse(entry)
		if err != nil {
			t.Fatalf("Parse(%q) = %v", entry, err)
		}
		if a.Numeric() != want {
			t.Errorf("%q Numeric() = %v, want %v", entry, a.Numeric(), want)
		}
	}
	if !assert.SubjectUptime.Numeric() || assert.SubjectValue.Numeric() {
		t.Error("uptime is numeric, value has no fixed kind")
	}
}

// TestValidName is the rule of an entry's name.
func TestValidName(t *testing.T) {
	for name, want := range map[string]bool{"ok": true, "a.b-c_1": true, "": false, "a b": false, strings.Repeat("n", 65): false, "ä": false} {
		if assert.ValidName(name) != want {
			t.Errorf("ValidName(%q) = %v, want %v", name, !want, want)
		}
	}
}
