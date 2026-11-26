package scriptargs_test

import (
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/scriptargs"
)

func TestValidName(t *testing.T) {
	for _, s := range []string{"p", "port", "mount_point", "a1", "a" + strings.Repeat("b", 31)} {
		if !scriptargs.ValidName(s) {
			t.Errorf("ValidName(%q) = false", s)
		}
	}
	for _, s := range []string{"", "1a", "_a", "Port", "a-b", "a.b", "a b", "a" + strings.Repeat("b", 32), "ä"} {
		if scriptargs.ValidName(s) {
			t.Errorf("ValidName(%q) = true", s)
		}
	}
}

func TestValueProblem(t *testing.T) {
	cases := []struct {
		v    string
		want string
	}{
		{"8080", ""},
		{"/var/lib/data", ""},
		{"a b", ""},
		{";rm -rf /", ""},
		{"grüße", ""},
		{strings.Repeat("x", scriptargs.MaxValueBytes), ""},
		{"", scriptargs.ProblemEmpty},
		{strings.Repeat("x", scriptargs.MaxValueBytes+1), scriptargs.ProblemLong},
		{"a\xffb", scriptargs.ProblemUTF8},
		{"a\nb", scriptargs.ProblemControl},
		{"a\tb", scriptargs.ProblemControl},
		{"a\x00b", scriptargs.ProblemControl},
		{"a\x7fb", scriptargs.ProblemControl},
		{"a\u0085b", scriptargs.ProblemControl},
	}
	for _, c := range cases {
		if got := scriptargs.ValueProblem(c.v); got != c.want {
			t.Errorf("ValueProblem(%q) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestParseEntry(t *testing.T) {
	cases := []struct {
		entry                string
		command, name, value string
		ok                   bool
	}{
		{"tcp:port=8080", "tcp", "port", "8080", true},
		{"check.disk-1:mount=/var/a=b:c", "check.disk-1", "mount", "/var/a=b:c", true},
		{"tcp:port=", "tcp", "port", "", true},
		{"tcp-port=8080", "", "", "", false},
		{"tcp:port", "", "", "", false},
	}
	for _, c := range cases {
		command, name, value, ok := scriptargs.ParseEntry(c.entry)
		if command != c.command || name != c.name || value != c.value || ok != c.ok {
			t.Errorf("ParseEntry(%q) = %q, %q, %q, %v", c.entry, command, name, value, ok)
		}
	}
}

func TestEntryProblem(t *testing.T) {
	cases := []struct {
		entry string
		want  string
	}{
		{"tcp:port=8080", ""},
		{"a.b_c-d:p_1=/x y", ""},
		{"tcp:port=-5", ""},
		{"tcp:port=;rm -rf /", ""},
		{"tcp port=8080", scriptargs.ProblemEntryShape},
		{"tcp:port", scriptargs.ProblemEntryShape},
		{"", scriptargs.ProblemEntryShape},
		{":port=1", scriptargs.ProblemEntryCommand},
		{"a/b:port=1", scriptargs.ProblemEntryCommand},
		{strings.Repeat("c", 65) + ":port=1", scriptargs.ProblemEntryCommand},
		{"tcp:Port=1", scriptargs.ProblemEntryName},
		{"tcp:=1", scriptargs.ProblemEntryName},
		{"tcp:port=", scriptargs.ProblemEntryValue},
		{"tcp:port=a\nb", scriptargs.ProblemEntryValue},
		{"tcp:port=" + strings.Repeat("x", scriptargs.MaxValueBytes+1), scriptargs.ProblemEntryValue},
	}
	for _, c := range cases {
		if got := scriptargs.EntryProblem(c.entry); got != c.want {
			t.Errorf("EntryProblem(%q) = %q, want %q", c.entry, got, c.want)
		}
	}
}

func TestPlaceholder(t *testing.T) {
	if name, ok := scriptargs.Placeholder("{port}"); !ok || name != "port" {
		t.Errorf("Placeholder({port}) = %q, %v", name, ok)
	}
	for _, s := range []string{"port", "{Port}", "{port", "{port}x", "x{port}", "--port={port}", "{}", "{1}", `{"a":1}`} {
		if _, ok := scriptargs.Placeholder(s); ok {
			t.Errorf("Placeholder(%q) ok", s)
		}
	}
	for _, s := range []string{"--port={port}", "x{a}", "{a}{b}", "{a}x"} {
		if !scriptargs.EmbedsPlaceholder(s) {
			t.Errorf("EmbedsPlaceholder(%q) = false", s)
		}
	}
	for _, s := range []string{"{port}", "{Port}", `{"a":1}`, "{}", "{ a }", "plain", "%{"} {
		if scriptargs.EmbedsPlaceholder(s) {
			t.Errorf("EmbedsPlaceholder(%q) = true", s)
		}
	}
}
