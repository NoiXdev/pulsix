package secrets

import (
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// argValue is planted in every malformed command below: an argument may
// hold a password, so no error text may ever contain it.
const argValue = "arg-Pa55word-77"

// program is an absolute program path on the agent's OS, single-quoted for
// YAML so a Windows path's backslashes are taken as written.
func program() string {
	if runtime.GOOS == "windows" {
		return `C:\Program Files\Pulsix\check_disk.exe`
	}
	return "/usr/lib/nagios/plugins/check_disk"
}

func quoted(s string) string { return "'" + s + "'" }

func TestParseAcceptsCommands(t *testing.T) {
	p := quoted(program())
	src := "secrets:\n  pg:\n    password: x\n" +
		"commands:\n" +
		"  disk_root: [" + p + ", \"-w\", \"20%\", \"-c\", \"10%\", \"-p\", \"/\"]\n" +
		"  backup_age:\n    - " + p + "\n" +
		"  with.empty-arg_1:\n    - " + p + "\n    - \"\"\n    - !!str 5\n    - 30\n    - \"a\\tb\\nc\\x7F\"\n"
	set, err := parse([]byte(src))
	if err != nil {
		t.Fatalf("parse = %v", err)
	}
	if got, want := set.Names(), []string{"pg"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Names = %v, want %v", got, want)
	}
	if got, want := set.CommandNames(), []string{"backup_age", "disk_root", "with.empty-arg_1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("CommandNames = %v, want %v", got, want)
	}
	tests := []struct {
		name string
		want []string
	}{
		{"disk_root", []string{program(), "-w", "20%", "-c", "10%", "-p", "/"}},
		{"backup_age", []string{program()}},
		// Numbers are taken as written, like secret field values.
		// Control characters are refused in the program only.
		{"with.empty-arg_1", []string{program(), "", "5", "30", "a\tb\nc\x7f"}},
	}
	for _, tt := range tests {
		tmpl, ok := set.Command(tt.name)
		got := tmpl.Argv()
		if !ok || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Command(%q) = %q, %v; want %q", tt.name, got, ok, tt.want)
		}
	}
	if _, ok := set.Command("nope"); ok {
		t.Error("Command of an undefined command reported ok")
	}
}

func TestParseAcceptsCommandsAlone(t *testing.T) {
	set, err := parse([]byte("commands:\n  a: [" + quoted(program()) + "]\n"))
	if err != nil {
		t.Fatalf("parse = %v", err)
	}
	if set.Len() != 0 || !reflect.DeepEqual(set.CommandNames(), []string{"a"}) {
		t.Fatalf("set = %v", set)
	}
}

func TestParseEmptyCommandsHoldNone(t *testing.T) {
	for _, src := range []string{"commands:\n", "commands: {}\n", "secrets: {}\ncommands:\n", "commands: ~\nsecrets:\n"} {
		set, err := parse([]byte(src))
		if err != nil {
			t.Errorf("parse(%q) = %v", src, err)
			continue
		}
		if n := len(set.CommandNames()); n != 0 {
			t.Errorf("parse(%q) has %d commands", src, n)
		}
	}
}

func TestParseRefusesBadCommandsWithoutQuotingThem(t *testing.T) {
	p := quoted(program())
	long := strings.Repeat("x", maxValueBytes+1)
	many := make([]string, maxCommandArgs+1)
	for i := range many {
		many[i] = p
	}
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"commands not a mapping", "commands:\n  - " + argValue + "\n",
			"line 2, column 3: commands must map command names to their program and arguments"},
		{"commands a sequence", "commands: [" + argValue + "]\n",
			"line 1, column 11: commands must map command names to their program and arguments"},
		{"command a scalar", "commands:\n  a: " + argValue + "\n",
			"line 2, column 6: a command must be a list of its program and arguments"},
		{"command a mapping without run", "commands:\n  a:\n    x: " + argValue + "\n",
			"line 3, column 5: a command mapping takes only run and args"},
		{"command null", "commands:\n  a:\n",
			"line 2, column 3: a command must be a list of its program and arguments"},
		{"empty sequence", "commands:\n  a: []\n",
			"line 2, column 6: a command needs at least its program"},
		{"relative program", "commands:\n  a: [bin/" + argValue + "]\n",
			"line 2, column 7: the program must be an absolute path"},
		{"NUL in an argument", "commands:\n  a: [" + p + ", \"" + argValue + "\\0x\"]\n",
			"line 2, column " + strconv.Itoa(9+len(p)) + ": a command entry must not contain a NUL character"},
		{"NUL in the program", "commands:\n  a: [\"/" + argValue + "\\0\"]\n",
			"line 2, column 7: a command entry must not contain a NUL character"},
		{"NUL in a relative program", "commands:\n  a: [\"" + argValue + "\\0\"]\n",
			"line 2, column 7: a command entry must not contain a NUL character"},
		{"tab in the program", "commands:\n  a: [\"/" + argValue + "\\tx\"]\n",
			"line 2, column 7: the program path must not contain control characters"},
		{"newline in the program", "commands:\n  a: [\"/" + argValue + "\\nx\"]\n",
			"line 2, column 7: the program path must not contain control characters"},
		{"DEL in the program", "commands:\n  a: [\"/" + argValue + "\\x7Fx\"]\n",
			"line 2, column 7: the program path must not contain control characters"},
		{"control character in a relative program", "commands:\n  a: [\"" + argValue + "\\x01\"]\n",
			"line 2, column 7: the program path must not contain control characters"},
		{"empty program", "commands:\n  a: [\"\", " + argValue + "]\n",
			"line 2, column 7: the program must be an absolute path"},
		{"too many entries", "commands:\n  a: [" + strings.Join(many, ", ") + "]\n",
			"line 2, column " + strconv.Itoa(7+maxCommandArgs*(len(p)+2)) + ": a command has more than 64 entries"},
		{"entry too long", "commands:\n  a: [" + p + ", " + argValue + long + "]\n",
			"line 2, column " + strconv.Itoa(9+len(p)) + ": a command entry is longer than 4096 bytes"},
		{"program too long", "commands:\n  a: [/" + argValue + long + "]\n",
			"line 2, column 7: a command entry is longer than 4096 bytes"},
		{"bad name", "commands:\n  \"" + argValue + " x\": [" + p + "]\n",
			"line 2, column 3: a command name is 1 to 64 characters of A-Z a-z 0-9 _ . -"},
		{"empty name", "commands:\n  \"\": [" + p + "]\n",
			"line 2, column 3: a command name is 1 to 64 characters of A-Z a-z 0-9 _ . -"},
		{"long name", "commands:\n  " + strings.Repeat("a", 65) + ": [" + p + "]\n",
			"line 2, column 3: a command name is 1 to 64 characters of A-Z a-z 0-9 _ . -"},
		{"complex name", "commands:\n  ? [a, b]\n  : [" + p + "]\n",
			"line 2, column 5: a command name is 1 to 64 characters of A-Z a-z 0-9 _ . -"},
		{"duplicate name", "commands:\n  a: [" + p + "]\n  a: [" + p + ", " + argValue + "]\n",
			"line 3, column 3: this command is already defined at line 2"},
		{"commands twice", "commands: {}\nsecrets: {}\ncommands:\n  a: [" + p + ", " + argValue + "]\n",
			"line 3, column 1: commands is already set at line 1"},
		{"mapping entry", "commands:\n  a:\n    - " + p + "\n    - x: " + argValue + "\n",
			"line 4, column 7: a command entry must be a single value, not a list or mapping"},
		{"nested sequence entry", "commands:\n  a: [" + p + ", [" + argValue + "]]\n",
			"line 2, column " + strconv.Itoa(9+len(p)) + ": a command entry must be a single value, not a list or mapping"},
		{"null entry", "commands:\n  a:\n    - " + p + "\n    -\n    - " + argValue + "\n",
			`line 4, column 6: a command entry has no value; write "" for an empty one`},
		{"null program", "commands:\n  a: [~, " + argValue + "]\n",
			`line 2, column 7: a command entry has no value; write "" for an empty one`},
		{"tag: int", "commands:\n  a: [" + p + ", !!int 5]\n",
			"line 2, column " + strconv.Itoa(9+len(p)) + ": " + badTag},
		{"tag: custom", "commands:\n  a: [" + p + ", !vault " + argValue + "]\n",
			"line 2, column " + strconv.Itoa(9+len(p)) + ": " + badTag},
		{"tag: on a command", "commands:\n  a: !!seq [" + p + ", " + argValue + "]\n",
			"line 2, column 6: " + badTag},
		{"tag: on commands", "commands: !!map\n  a: [" + p + ", " + argValue + "]\n",
			"line 1, column 11: " + badTag},
		{"anchor on an entry", "commands:\n  a: [" + p + ", &x " + argValue + "]\n  b: [" + p + ", *x]\n",
			"line 2, column " + strconv.Itoa(9+len(p)) + ": " + badAnchor},
		{"anchor on a command", "commands:\n  a: &" + argValue + " [" + p + "]\n",
			"line 2, column 6: " + badAnchor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set, err := parse([]byte(tt.src))
			if err == nil {
				t.Fatalf("parse accepted %q: %v", tt.src, set)
			}
			if strings.Contains(err.Error(), argValue) || strings.Contains(err.Error(), "xxxxxxxx") {
				t.Fatalf("error quotes the file: %q", err)
			}
			if err.Error() != tt.want {
				t.Fatalf("error = %q, want %q", err, tt.want)
			}
		})
	}
}

// TestParseRefusesAPathOfTheOtherOS: the program must be absolute on the
// agent's own OS, so a Unix path is relative on Windows and a drive path is
// relative elsewhere.
func TestParseRefusesAPathOfTheOtherOS(t *testing.T) {
	other := `'C:\Windows\x.exe'`
	if runtime.GOOS == "windows" {
		other = "/usr/bin/x"
	}
	_, err := parse([]byte("commands:\n  a: [" + other + "]\n"))
	if err == nil || err.Error() != "line 2, column 7: the program must be an absolute path" {
		t.Fatalf("parse = %v", err)
	}
}

func TestParseLimitsTheNumberOfCommands(t *testing.T) {
	p := quoted(program())
	var b strings.Builder
	b.WriteString("commands:\n")
	for i := 0; i < maxCommands; i++ {
		b.WriteString("  c" + strconv.Itoa(i) + ": [" + p + "]\n")
	}
	if set, err := parse([]byte(b.String())); err != nil || len(set.CommandNames()) != maxCommands {
		t.Fatalf("parse(%d commands) = %d, %v", maxCommands, len(set.CommandNames()), err)
	}
	b.WriteString("  one_more: [" + p + "]\n")
	_, err := parse([]byte(b.String()))
	if err == nil || err.Error() != "line 258, column 3: more than 256 commands" {
		t.Fatalf("parse(%d commands) = %v", maxCommands+1, err)
	}
}

func TestParseAcceptsCommandsAtTheLimits(t *testing.T) {
	p := quoted(program())
	entries := make([]string, maxCommandArgs)
	entries[0] = p
	for i := 1; i < maxCommandArgs; i++ {
		entries[i] = "a" + strconv.Itoa(i)
	}
	entries[1] = strings.Repeat("y", maxValueBytes)
	set, err := parse([]byte("commands:\n  a: [" + strings.Join(entries, ", ") + "]\n"))
	if err != nil {
		t.Fatalf("parse = %v", err)
	}
	tmpl, _ := set.Command("a")
	argv := tmpl.Argv()
	if len(argv) != maxCommandArgs || argv[1] != entries[1] {
		t.Fatalf("argv has %d entries", len(argv))
	}
}

func TestCommandIsACopy(t *testing.T) {
	set, err := parse([]byte("commands:\n  a: [" + quoted(program()) + ", x]\n"))
	if err != nil {
		t.Fatal(err)
	}
	tmpl, _ := set.Command("a")
	argv := tmpl.Argv()
	argv[1] = "changed"
	if again, _ := set.Command("a"); again.Argv()[1] != "x" {
		t.Fatalf("Command shares its slice: %q", again.Argv())
	}
	names := set.CommandNames()
	names[0] = "changed"
	if set.CommandNames()[0] != "a" {
		t.Fatal("CommandNames shares its slice")
	}
}
