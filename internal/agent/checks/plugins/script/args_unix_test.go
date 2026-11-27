//go:build !windows

package script

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/noixdev/pulsix/internal/scriptargs"
)

// TestRealRunPassesValuesLiterally: a real script receives each value as
// one whole argument, shell syntax included, and no shell ever reads it.
func TestRealRunPassesValuesLiterally(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "sentinel")
	if err := os.WriteFile(sentinel, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "args.sh")
	if err := os.WriteFile(script, []byte("printf '[%s]' \"$@\"\necho\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	anyText, err := scriptargs.PatternRule(`.+`, scriptargs.PatternOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := scriptargs.NewTemplate([]string{"/bin/sh", script, "{a}", "{b}", "{c}", "{d}"}, map[string]scriptargs.Param{
		"a": {Rule: anyText}, "b": {Rule: anyText}, "c": {Rule: anyText}, "d": {Rule: anyText},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmds := fakeCommands{templates: map[string]scriptargs.Template{"x": tmpl}}
	checker := &fakeChecker{files: map[string]error{"/bin/sh": nil, script: nil}}
	values := []any{"x:a=;rm -f " + sentinel, "x:b=$(touch " + sentinel + "2)", "x:c=a b", "x:d=`id` && echo pwned"}
	got := collect(t, newPlugin(newExecRunner(), checker), map[string]any{"commands": []any{"x"}, "args": values}, cmds)
	want := `x script.output "[;rm -f ` + sentinel + `][$(touch ` + sentinel + `2)][a b][` + "`id`" + ` && echo pwned]"`
	if len(got) < 2 || got[0] != `x script.status "ok"` || got[1] != want {
		t.Fatalf("observations %q, want output %s", got, want)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("the sentinel is gone: %v", err)
	}
	if _, err := os.Stat(sentinel + "2"); err == nil {
		t.Error("a command substitution ran")
	}
}
