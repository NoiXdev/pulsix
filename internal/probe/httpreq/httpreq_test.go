package httpreq_test

import (
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/probe/httpreq"
)

func TestHeaderProblem(t *testing.T) {
	cases := []struct {
		entry string
		want  string
	}{
		{"X-Api-Version: 2", ""},
		{"x-request-id:abc", ""},
		{"Accept: application/json; q=0.9", ""},
		{"X-Empty:", ""},
		{"X-Tab: a\tb", ""},
		{"X-Umlaut: grüße", ""},
		{"X-Colon: a:b", ""},
		{"!#$%&'*+.^_`|~09az-AZ: v", ""},
		{"", httpreq.ProblemHeaderShape},
		{"NoColon", httpreq.ProblemHeaderShape},
		{": value", httpreq.ProblemHeaderShape},
		{"X Space: v", httpreq.ProblemHeaderShape},
		{" X-Lead: v", httpreq.ProblemHeaderShape},
		{"X-Trail : v", httpreq.ProblemHeaderShape},
		{"X-(paren): v", httpreq.ProblemHeaderShape},
		{"Ä: v", httpreq.ProblemHeaderShape},
		{"X-CR: a\rb", httpreq.ProblemHeaderValue},
		{"X-LF: a\nX-Other: b", httpreq.ProblemHeaderValue},
		{"X-NUL: a\x00b", httpreq.ProblemHeaderValue},
		{"X-DEL: a\x7fb", httpreq.ProblemHeaderValue},
		{"X-Name-" + strings.Repeat("n", 128) + ": v", httpreq.ProblemHeaderShape},
		{"Host: example.com", httpreq.ProblemHeaderReserved},
		{"content-length: 3", httpreq.ProblemHeaderReserved},
		{"Transfer-Encoding: chunked", httpreq.ProblemHeaderReserved},
		{"Connection: close", httpreq.ProblemHeaderReserved},
		{"User-Agent: me", httpreq.ProblemHeaderReserved},
		{"Keep-Alive: 1", httpreq.ProblemHeaderReserved},
		{"Proxy-Connection: x", httpreq.ProblemHeaderReserved},
		{"TE: trailers", httpreq.ProblemHeaderReserved},
		{"Trailer: x", httpreq.ProblemHeaderReserved},
		{"Upgrade: h2c", httpreq.ProblemHeaderReserved},
		{"Expect: 100-continue", httpreq.ProblemHeaderReserved},
		{"Accept-Encoding: gzip", httpreq.ProblemHeaderReserved},
		{"Content-Type: text/plain", httpreq.ProblemHeaderContentType},
		{"Authorization: Bearer x", httpreq.ProblemHeaderCredentials},
		{"proxy-authorization: Basic x", httpreq.ProblemHeaderCredentials},
		{"COOKIE: a=b", httpreq.ProblemHeaderCredentials},
		// Names that suggest a credential (fix round 1, M1): the secret
		// sends any one header of the operator's choice.
		{"X-Api-Key: k", httpreq.ProblemHeaderCredentials},
		{"api-key: k", httpreq.ProblemHeaderCredentials},
		{"X-APIKEY: k", httpreq.ProblemHeaderCredentials},
		{"X-Auth-Token: t", httpreq.ProblemHeaderCredentials},
		{"Private-Token: t", httpreq.ProblemHeaderCredentials},
		{"X-Vault-Token: t", httpreq.ProblemHeaderCredentials},
		{"X-Client-Secret: s", httpreq.ProblemHeaderCredentials},
		{"X-Password: p", httpreq.ProblemHeaderCredentials},
		{"X-Key-Id: 1", ""},
		{"X-Author: me", ""},
		// Routing and override headers a target may trust (fix round 1, I1).
		{"X-Forwarded-For: 127.0.0.1", httpreq.ProblemHeaderRouting},
		{"x-forwarded-host: a", httpreq.ProblemHeaderRouting},
		{"X-Forwarded-Proto: https", httpreq.ProblemHeaderRouting},
		{"Forwarded: for=1.2.3.4", httpreq.ProblemHeaderRouting},
		{"X-Real-IP: 1.2.3.4", httpreq.ProblemHeaderRouting},
		{"X-Original-URL: /admin", httpreq.ProblemHeaderRouting},
		{"X-Rewrite-URL: /admin", httpreq.ProblemHeaderRouting},
		{"X-HTTP-Method-Override: DELETE", httpreq.ProblemHeaderRouting},
		{"X-HTTP-Method: DELETE", httpreq.ProblemHeaderRouting},
		{"X-Method-Override: DELETE", httpreq.ProblemHeaderRouting},
		{"X-Forwarded: x", httpreq.ProblemHeaderRouting},
		{"X-Forward-Me: x", ""},
		// Not UTF-8 (fix round 1, M3): only a Go caller can pass it.
		{"X-Bin: a\xffb", httpreq.ProblemHeaderValue},
	}
	for _, c := range cases {
		if got := httpreq.HeaderProblem(c.entry); got != c.want {
			t.Errorf("HeaderProblem(%q) = %q, want %q", c.entry, got, c.want)
		}
	}
}

func TestParseHeader(t *testing.T) {
	cases := []struct {
		entry       string
		name, value string
		ok          bool
	}{
		{"x-api-version:  2 ", "X-Api-Version", "2", true},
		{"X-Empty:", "X-Empty", "", true},
		{"X-Tabs:\t a b \t", "X-Tabs", "a b", true},
		{"X-Colon: a: b", "X-Colon", "a: b", true},
		{"Authorization: x", "", "", false},
		{"X-LF: a\nb", "", "", false},
		{"bad name: v", "", "", false},
	}
	for _, c := range cases {
		name, value, ok := httpreq.ParseHeader(c.entry)
		if name != c.name || value != c.value || ok != c.ok {
			t.Errorf("ParseHeader(%q) = %q, %q, %v; want %q, %q, %v", c.entry, name, value, ok, c.name, c.value, c.ok)
		}
	}
}

func TestMediaTypeProblem(t *testing.T) {
	valid := []string{
		"application/json", "text/plain; charset=utf-8", "application/x-www-form-urlencoded",
		"application/vnd.api+json", `multipart/form-data;boundary="a b"`, "text/plain;charset=UTF-8;format=flowed",
	}
	for _, s := range valid {
		if p := httpreq.MediaTypeProblem(s); p != "" {
			t.Errorf("MediaTypeProblem(%q) = %q, want none", s, p)
		}
	}
	invalid := []string{
		"", "json", "application/", "/json", "application/json;", "application/json; charset",
		"text/plain\r\nX-Evil: 1", "text/plain; charset=\"a\nb\"", "text/pl ain", "application/json ",
		`text/plain; a="unterminated`,
	}
	for _, s := range invalid {
		if p := httpreq.MediaTypeProblem(s); p != httpreq.ProblemMediaType {
			t.Errorf("MediaTypeProblem(%q) = %q, want %q", s, p, httpreq.ProblemMediaType)
		}
	}
}

func TestTakesBodyAndMethods(t *testing.T) {
	want := map[string]bool{"GET": false, "HEAD": false, "POST": true, "PUT": true, "PATCH": true, "DELETE": false, "OPTIONS": false}
	if len(httpreq.Methods) != len(want) {
		t.Fatalf("Methods = %v", httpreq.Methods)
	}
	for _, m := range httpreq.Methods {
		takes, known := want[m]
		if !known {
			t.Fatalf("unexpected method %q", m)
		}
		if httpreq.TakesBody(m) != takes {
			t.Errorf("TakesBody(%q) = %v", m, !takes)
		}
		if !httpreq.KnownMethod(m) {
			t.Errorf("KnownMethod(%q) = false", m)
		}
	}
	for _, m := range []string{"get", "TRACE", "CONNECT", ""} {
		if httpreq.KnownMethod(m) || httpreq.TakesBody(m) {
			t.Errorf("%q is known or takes a body", m)
		}
	}
}

func TestWrites(t *testing.T) {
	for _, c := range []struct {
		method  string
		hasBody bool
		want    bool
	}{
		{"GET", false, false}, {"HEAD", false, false}, {"OPTIONS", false, false},
		{"POST", false, true}, {"PUT", false, true}, {"PATCH", false, true}, {"DELETE", false, true},
		{"GET", true, true},
	} {
		if got := httpreq.Writes(c.method, c.hasBody); got != c.want {
			t.Errorf("Writes(%q, %v) = %v", c.method, c.hasBody, got)
		}
	}
}

func TestBodyProblem(t *testing.T) {
	for _, ok := range []string{"x", "{\"a\": 1}\r\n", "grüße\ttab"} {
		if p := httpreq.BodyProblem(ok); p != "" {
			t.Errorf("BodyProblem(%q) = %q", ok, p)
		}
	}
	for _, bad := range []string{"a\x00b", "a\xffb", "\x00"} {
		if p := httpreq.BodyProblem(bad); p != httpreq.ProblemBodyText {
			t.Errorf("BodyProblem(%q) = %q, want %q", bad, p, httpreq.ProblemBodyText)
		}
	}
}
