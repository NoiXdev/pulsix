package secrets

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestParseAcceptsCommandArgs(t *testing.T) {
	p := quoted(program())
	src := "commands:\n" +
		"  tcp:\n" +
		"    run: [" + p + ", -H, 127.0.0.1, -p, '{port}', -m, '{mode}', '{path}', '{offset}']\n" +
		"    args:\n" +
		"      port:\n        min: 1\n        max: 65535\n        default: 5432\n" +
		"      mode:\n        values: [fast, full]\n" +
		"      path:\n        pattern: '/[a-z/]+'\n        default: /var\n" +
		"      offset:\n        pattern: '-?[0-9]+'\n        allow_leading_dash: true\n        default: '0'\n" +
		"  fixed:\n    run: [" + p + ", x]\n" +
		"  list_form: [" + p + ", '{port}']\n"
	set, err := parse([]byte(src))
	if err != nil {
		t.Fatalf("parse = %v", err)
	}
	tmpl, ok := set.Command("tcp")
	if !ok || !reflect.DeepEqual(tmpl.Placeholders(), []string{"mode", "offset", "path", "port"}) {
		t.Fatalf("tcp = %v, %v", tmpl.Placeholders(), ok)
	}
	got, err := tmpl.Bind(map[string]string{"mode": "full", "offset": "-2"})
	want := []string{program(), "-H", "127.0.0.1", "-p", "5432", "-m", "full", "/var", "-2"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Bind = %q, %v; want %q", got, err, want)
	}
	if _, err := tmpl.Bind(map[string]string{"mode": "full", "path": "/etc/../x"}); err == nil {
		t.Fatal("Bind accepted a value the pattern refuses")
	}
	// The list form is literal, as in 3.6g; a mapping without args takes
	// no values and keeps an entry that is no placeholder.
	for _, name := range []string{"list_form", "fixed"} {
		c, _ := set.Command(name)
		if _, err := c.Bind(map[string]string{"port": "1"}); err == nil {
			t.Errorf("%s accepted a value", name)
		}
	}
	if c, _ := set.Command("list_form"); c.Argv()[1] != "{port}" {
		t.Errorf("list form argv = %q", c.Argv())
	}
}

func TestParseRefusesCommandArgs(t *testing.T) {
	p := quoted(program())
	head := "commands:\n  a:\n"
	run := "    run: [" + p + ", '{x}']\n"
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"no run", head + "    args: {}\n",
			"line 2, column 3: a command mapping needs run, the list of its program and arguments"},
		{"run not a list", head + "    run: " + argValue + "\n",
			"line 3, column 10: a command must be a list of its program and arguments"},
		{"unknown key", head + run + "    env: " + argValue + "\n",
			"line 4, column 5: a command mapping takes only run and args"},
		{"run twice", head + run + run,
			"line 4, column 5: run is already set at line 3"},
		{"args not a mapping", head + run + "    args: [" + argValue + "]\n",
			"line 4, column 11: args must map placeholder names to their rules"},
		{"undeclared placeholder", head + run,
			"line 3, column " + strconv.Itoa(13+len(p)) + ": this placeholder has no rule under args"},
		{"embedded placeholder", head + "    run: [" + p + ", '--x={x}']\n    args:\n      x: {values: [a]}\n",
			"line 3, column " + strconv.Itoa(13+len(p)) + ": a placeholder must be a whole argument"},
		{"program placeholder", head + "    run: [" + quoted(program()+"{x}") + "]\n    args:\n      x: {values: [a]}\n",
			"line 3, column 11: the program cannot be or hold a placeholder"},
		{"unused rule", head + run + "    args:\n      x: {values: [a]}\n      y: {values: [b]}\n",
			"line 6, column 7: this placeholder is not used in run"},
		{"bad placeholder name", head + run + "    args:\n      x: {values: [a]}\n      Bad" + argValue + ": {values: [b]}\n",
			"line 6, column 7: a placeholder name is a lower-case letter followed by up to 31 lower-case letters, digits or _"},
		{"placeholder twice", head + run + "    args:\n      x: {values: [a]}\n      x: {values: [b]}\n",
			"line 6, column 7: this placeholder is already set at line 5"},
		{"rule not a mapping", head + run + "    args:\n      x: " + argValue + "\n",
			"line 5, column 10: a placeholder's rule must be a mapping of pattern, min and max, or values"},
		{"no rule", head + run + "    args:\n      x: {default: a}\n",
			"line 5, column 7: a placeholder needs exactly one rule: pattern, min and max, or values"},
		{"two rules", head + run + "    args:\n      x: {values: [a], pattern: a}\n",
			"line 5, column 7: a placeholder needs exactly one rule: pattern, min and max, or values"},
		{"min alone", head + run + "    args:\n      x: {min: 1}\n",
			"line 5, column 7: a range needs both min and max"},
		{"min not a number", head + run + "    args:\n      x: {min: " + argValue + ", max: 2}\n",
			"line 5, column 16: min must be a whole number"},
		{"max a float", head + run + "    args:\n      x: {min: 1, max: 2.5}\n",
			"line 5, column 24: max must be a whole number"},
		{"min over max", head + run + "    args:\n      x: {min: 3, max: 2}\n",
			"line 5, column 10: a range's min is greater than its max"},
		{"bad pattern", head + run + "    args:\n      x: {pattern: '(" + argValue + "'}\n",
			"line 5, column 20: a pattern is not a valid regular expression"},
		{"long pattern", head + run + "    args:\n      x: {pattern: " + strings.Repeat("a", 513) + "}\n",
			"line 5, column 20: a pattern is longer than 512 bytes"},
		{"pattern a list", head + run + "    args:\n      x: {pattern: [a]}\n",
			"line 5, column 20: pattern must be a single value"},
		{"dash flag without pattern", head + run + "    args:\n      x: {values: [a], allow_leading_dash: true}\n",
			"line 5, column 7: allow_leading_dash goes only with a pattern"},
		{"dash flag not a boolean", head + run + "    args:\n      x: {pattern: a, allow_leading_dash: " + argValue + "}\n",
			"line 5, column 43: allow_leading_dash must be true or false"},
		{"values empty", head + run + "    args:\n      x: {values: []}\n",
			"line 5, column 19: a values list must not be empty"},
		{"values twice", head + run + "    args:\n      x: {values: [" + argValue + ", " + argValue + "]}\n",
			"line 5, column 19: a values list holds a value twice"},
		{"values nested", head + run + "    args:\n      x: {values: [[a]]}\n",
			"line 5, column 20: a listed value must be a single value"},
		{"values null", head + run + "    args:\n      x: {values: [~]}\n",
			"line 5, column 20: a listed value must be a single value"},
		{"unknown rule key", head + run + "    args:\n      x: {values: [a], label: " + argValue + "}\n",
			"line 5, column 24: a rule takes only pattern, allow_leading_dash, min, max, values and default"},
		{"rule key twice", head + run + "    args:\n      x: {values: [a], values: [b]}\n",
			"line 5, column 24: values is already set at line 5"},
		{"default fails the rule", head + run + "    args:\n      x: {values: [a], default: " + argValue + "}\n",
			"line 5, column 7: the default is not one of the values this host allows"},
		{"default null", head + run + "    args:\n      x: {values: [a], default: ~}\n",
			"line 5, column 33: default must be a single value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set, err := parse([]byte(tt.src))
			if err == nil {
				t.Fatalf("parse accepted %q: %v", tt.src, set)
			}
			if strings.Contains(err.Error(), argValue) {
				t.Fatalf("error quotes the file: %q", err)
			}
			if err.Error() != tt.want {
				t.Fatalf("error = %q, want %q", err, tt.want)
			}
		})
	}
}

func TestParseLimitsPlaceholders(t *testing.T) {
	p := quoted(program())
	var run, args strings.Builder
	for i := range 17 {
		name := "p" + strconv.Itoa(i)
		run.WriteString(", '{" + name + "}'")
		args.WriteString("      " + name + ": {values: [a]}\n")
	}
	_, err := parse([]byte("commands:\n  a:\n    run: [" + p + run.String() + "]\n    args:\n" + args.String()))
	if err == nil || err.Error() != "line 21, column 7: a command has more than 16 placeholders" {
		t.Fatalf("parse = %v", err)
	}
}
