package script

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/scriptargs"
)

// portCommand is command "x": prog -p {port} {path}, port 1 to 65535
// with default 5432, path an absolute path below /srv (no leading '-').
func portCommand(t *testing.T) fakeCommands {
	t.Helper()
	port, err := scriptargs.RangeRule(1, 65535)
	if err != nil {
		t.Fatal(err)
	}
	path, err := scriptargs.PatternRule(`/srv/[a-z0-9/ ]+`, scriptargs.PatternOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := scriptargs.NewTemplate([]string{prog, "-p", "{port}", "{path}"}, map[string]scriptargs.Param{
		"port": {Rule: port, Default: "5432", HasDefault: true},
		"path": {Rule: path},
	})
	if err != nil {
		t.Fatal(err)
	}
	return fakeCommands{templates: map[string]scriptargs.Template{"x": tmpl}, argv: map[string][]string{"plain": {prog}}}
}

func TestScriptBindsArgs(t *testing.T) {
	r := exitedWith(0, "OK")
	c := progOK()
	got := collect(t, newPlugin(r, c), map[string]any{
		"commands": []any{"x"},
		"args":     []any{"x:port=+8080", "x:path=/srv/data"},
	}, portCommand(t))
	equalLines(t, got, []string{`x script.status "ok"`, `x script.output "OK"`, `x script.exit_code 0`, `x script.duration_seconds 0.25`})
	calls := r.called()
	if len(calls) != 1 || strings.Join(calls[0], " ") != prog+" -p 8080 /srv/data" {
		t.Fatalf("ran %q", calls)
	}
	// The bound path is checked like a configured argument.
	if !containsString(c.checked, "/srv/data") {
		t.Errorf("checked %q, want the bound path among them", c.checked)
	}
}

func containsString(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

func TestScriptArgsRefusals(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
		want   []string // per command: its output
	}{
		{"values for a command without placeholders",
			map[string]any{"commands": []any{"plain"}, "args": []any{"plain:port=1"}},
			[]string{"this command takes no arguments on this host"}},
		{"a placeholder the command does not take",
			map[string]any{"commands": []any{"x"}, "args": []any{"x:path=/srv/a", "x:user=root"}},
			[]string{`argument "user" is not one this command takes on this host`}},
		{"no value and no default",
			map[string]any{"commands": []any{"x"}},
			[]string{`argument "path" needs a value (this host sets no default)`}},
		{"out of range",
			map[string]any{"commands": []any{"x"}, "args": []any{"x:path=/srv/a", "x:port=70000"}},
			[]string{`argument "port" must be a whole number from 1 to 65535`}},
		{"option injection",
			map[string]any{"commands": []any{"x"}, "args": []any{"x:path=--config=/etc/shadow"}},
			[]string{`argument "path" must not start with '-'`}},
		{"path traversal",
			map[string]any{"commands": []any{"x"}, "args": []any{"x:path=/srv/../etc"}},
			[]string{`argument "path" must not hold a ".." path segment`}},
		{"outside the pattern",
			map[string]any{"commands": []any{"x"}, "args": []any{"x:path=/etc/passwd"}},
			[]string{`argument "path" does not match the pattern this host allows`}},
		{"not printable ASCII",
			map[string]any{"commands": []any{"x"}, "args": []any{"x:path=/srv/a＂b"}},
			[]string{`argument "path" must be printable ASCII (this host's rule does not set unicode: true)`}},
		{"malformed entry",
			map[string]any{"commands": []any{"x", "plain"}, "args": []any{"x-path=/srv/a"}},
			[]string{textBadArgs, textBadArgs}},
		{"entry for a command not listed",
			map[string]any{"commands": []any{"plain"}, "args": []any{"x:path=/srv/a"}},
			[]string{textBadArgs}},
		{"a placeholder twice",
			map[string]any{"commands": []any{"x"}, "args": []any{"x:path=/srv/a", "x:path=/srv/b"}},
			[]string{textBadArgs}},
		{"control character",
			map[string]any{"commands": []any{"x"}, "args": []any{"x:path=/srv/a\nb"}},
			[]string{textBadArgs}},
		{"not a list",
			map[string]any{"commands": []any{"x"}, "args": "x:path=/srv/a"},
			[]string{textBadArgs}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := exitedWith(0, "OK")
			got := collect(t, newPlugin(r, progOK()), c.params, portCommand(t))
			names, _ := commandNames(c.params)
			want := make([]string, 0, 2*len(names))
			for i, name := range names {
				want = append(want, name+` script.status "unknown"`, name+` script.output `+strconv.Quote(c.want[i]))
			}
			equalLines(t, got, want)
			if calls := r.called(); len(calls) != 0 {
				t.Fatalf("ran %q", calls)
			}
		})
	}
}

func TestScriptArgsRefusalNeverQuotesValue(t *testing.T) {
	for _, v := range []string{"/srv/" + marker + "/../x", "-" + marker, "/etc/" + marker} {
		got := collect(t, newPlugin(exitedWith(0, "OK"), progOK()), map[string]any{
			"commands": []any{"x"}, "args": []any{"x:path=" + v, "x:port=" + marker},
		}, portCommand(t))
		if strings.Contains(strings.Join(got, "\n"), marker) {
			t.Fatalf("observations quote a refused value: %q", got)
		}
	}
}

// TestScriptFileCheckOnValuesIsGeneric: a bound value the file check
// refuses, or cannot check, is reported by its placeholder alone, with one
// text that says neither whether the path exists nor who may write it
// (review fix round 1). A literal argument keeps 3.6g's wording.
func TestScriptFileCheckOnValuesIsGeneric(t *testing.T) {
	params := map[string]any{"commands": []any{"x"}, "args": []any{"x:path=/srv/" + strings.ToLower("secretdir")}}
	for name, err := range map[string]error{
		"refused":   refuse("writable by others"),
		"unchecked": errors.New("stat failed"),
	} {
		c := &fakeChecker{files: map[string]error{prog: nil, "/srv/secretdir": err}}
		r := exitedWith(0, "OK")
		got := collect(t, newPlugin(r, c), params, portCommand(t))
		want := []string{`x script.status "unknown"`, `x script.output "argument \"path\" was refused by the file check"`}
		equalLines(t, got, want)
		if len(r.called()) != 0 {
			t.Errorf("%s: the command ran", name)
		}
	}
	// A value naming nothing in a safe place passes, as a literal does.
	got := collect(t, newPlugin(exitedWith(0, "OK"), progOK()), params, portCommand(t))
	if got[0] != `x script.status "ok"` {
		t.Fatalf("missing path: %q", got)
	}
}
