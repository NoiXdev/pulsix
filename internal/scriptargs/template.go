package scriptargs

import (
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Param is one placeholder's rule and, when HasDefault, the value bound
// when a check supplies none.
type Param struct {
	Rule       Rule
	Default    string
	HasDefault bool
}

// Template is a command line whose whole-argument placeholders take
// values a check supplies. Bind is the only way to a runnable argv. The
// zero Template has no program. A Template is immutable and safe for
// concurrent use.
type Template struct {
	argv    []string
	params  map[string]Param
	batch   bool // the program re-parses its command line (batchProgram)
	windows bool // the program is a Windows path (windowsProgram)
}

// Fixed is a command line that takes no values: every entry is literal,
// "{x}" included (agent.yaml's list form, backlog 3.6g).
func Fixed(argv []string) Template {
	return Template{argv: slices.Clone(argv)}
}

// TemplateError is a fault in a command line or its rules. Arg is the
// index of the argv entry at fault, or -1; Param the placeholder at
// fault, or "". Reason never quotes an argument, a pattern or a default.
type TemplateError struct {
	Arg    int
	Param  string
	Reason string
}

func (e *TemplateError) Error() string { return e.Reason }

// NewTemplate is argv whose entries "{name}" are placeholders, each with
// its rule in params. The program (argv[0]) is never a placeholder and
// holds none; an argument holding a placeholder among other text is
// refused (placeholders are whole arguments); every placeholder must have
// a rule and every rule must be used; at most MaxPlaceholders; a default
// must pass the shape, the rule and, for a batch program, the batch rule.
func NewTemplate(argv []string, params map[string]Param) (Template, error) {
	if len(argv) == 0 {
		return Template{}, &TemplateError{Arg: -1, Reason: "a command needs at least its program"}
	}
	if _, ok := Placeholder(argv[0]); ok || EmbedsPlaceholder(argv[0]) {
		return Template{}, &TemplateError{Arg: 0, Reason: "the program cannot be or hold a placeholder"}
	}
	used := map[string]bool{}
	for i, arg := range argv[1:] {
		if name, ok := Placeholder(arg); ok {
			if _, declared := params[name]; !declared {
				return Template{}, &TemplateError{Arg: i + 1, Reason: "this placeholder has no rule under args"}
			}
			used[name] = true
			continue
		}
		if EmbedsPlaceholder(arg) {
			return Template{}, &TemplateError{Arg: i + 1, Reason: "a placeholder must be a whole argument"}
		}
	}
	if len(params) > MaxPlaceholders {
		return Template{}, &TemplateError{Arg: -1, Reason: "a command has more than " + strconv.Itoa(MaxPlaceholders) + " placeholders"}
	}
	t := Template{argv: slices.Clone(argv), params: maps.Clone(params), batch: batchProgram(argv[0]), windows: windowsProgram(argv[0])}
	for _, name := range slices.Sorted(maps.Keys(params)) {
		p := params[name]
		switch {
		case !ValidName(name):
			return Template{}, &TemplateError{Arg: -1, Param: name, Reason: "a placeholder name is a lower-case letter followed by up to 31 lower-case letters, digits or _"}
		case !used[name]:
			return Template{}, &TemplateError{Arg: -1, Param: name, Reason: "this placeholder is not used in run"}
		case p.Rule.kind == kindNone:
			return Template{}, &TemplateError{Arg: -1, Param: name, Reason: "this placeholder has no rule"}
		}
		if p.HasDefault {
			if _, reason := t.bindOne(p, p.Default); reason != "" {
				return Template{}, &TemplateError{Arg: -1, Param: name, Reason: "the default " + reason}
			}
		}
	}
	return t, nil
}

// Program is the command's program, argv[0]; "" for the zero Template.
func (t Template) Program() string {
	if len(t.argv) == 0 {
		return ""
	}
	return t.argv[0]
}

// Argv is a copy of the command line with its placeholders unbound.
func (t Template) Argv() []string { return slices.Clone(t.argv) }

// Placeholders are the names the command takes values for, sorted.
func (t Template) Placeholders() []string { return slices.Sorted(maps.Keys(t.params)) }

var errNoArguments = errors.New("this command takes no arguments on this host")

// Bind returns the command line with values bound to its placeholders: a
// value given for each placeholder, its default otherwise. It refuses
// values for a command without placeholders, a value for a placeholder
// the command does not take, a value failing the shape, its rule or the
// batch rule, and a placeholder with neither value nor default. Errors
// name the placeholder and a fixed reason, never a value; names are
// judged in sorted order, so the error is the same every time.
func (t Template) Bind(values map[string]string) ([]string, error) {
	argv, _, err := t.BindNamed(values)
	return argv, err
}

// BindNamed is Bind that also returns, for every entry of argv, the name
// of the placeholder it was bound from, or "" for a literal entry, so a
// later refusal of a bound argument (a file check) can name the
// placeholder instead of the value.
func (t Template) BindNamed(values map[string]string) (argv, names []string, err error) {
	names = make([]string, len(t.argv))
	if len(t.params) == 0 {
		if len(values) > 0 {
			return nil, nil, errNoArguments
		}
		return slices.Clone(t.argv), names, nil
	}
	for _, name := range slices.Sorted(maps.Keys(values)) {
		if !ValidName(name) {
			return nil, nil, errors.New("an argument name is invalid")
		}
		if _, ok := t.params[name]; !ok {
			return nil, nil, errors.New(argument(name) + " is not one this command takes on this host")
		}
	}
	bound := make(map[string]string, len(t.params))
	for _, name := range slices.Sorted(maps.Keys(t.params)) {
		p := t.params[name]
		v, given := values[name]
		if !given {
			if !p.HasDefault {
				return nil, nil, errors.New(argument(name) + " needs a value (this host sets no default)")
			}
			v = p.Default
		}
		arg, reason := t.bindOne(p, v)
		if reason != "" {
			return nil, nil, errors.New(argument(name) + " " + reason)
		}
		bound[name] = arg
	}
	argv = slices.Clone(t.argv)
	for i, arg := range argv[1:] {
		if name, ok := Placeholder(arg); ok {
			argv[i+1], names[i+1] = bound[name], name
		}
	}
	return argv, names, nil
}

// bindOne checks v against the shape, printable ASCII (unless the rule
// allows more), the batch rule and p's rule and returns the argument to
// pass, or the reason it is refused.
func (t Template) bindOne(p Param, v string) (arg, reason string) {
	if reason := ValueProblem(v); reason != "" {
		return "", reason
	}
	if !p.Rule.unicode && !printableASCII(v) {
		return "", "must be printable ASCII (this host's rule does not set unicode: true)"
	}
	if t.batch && strings.ContainsAny(v, batchUnsafe) {
		return "", `must not contain any of " % ^ & | < > ! ( ) , ; = for a batch program`
	}
	return p.Rule.check(v, t.windows)
}

func argument(name string) string { return `argument "` + name + `"` }

// batchUnsafe are the characters cmd.exe gives a meaning on a command
// line it re-parses, and the delimiters (',', ';', '=') at which it splits
// a batch file's %1 … %9 outside quotes; Go's quoting of an argument
// (only for space, tab and '"') escapes none of them. CR and LF fail the
// shape already.
const batchUnsafe = `"%^&|<>!(),;=`

// batchProgram reports a program Windows runs through cmd.exe, or cmd.exe
// itself: its base name, case and trailing dots and spaces ignored (as
// Windows ignores them), ends in .bat or .cmd or is cmd.exe or cmd. It is
// judged on every OS; a Unix file of that name is held to the same rule.
func batchProgram(prog string) bool {
	prog = withoutStream(prog)
	base := prog[strings.LastIndexAny(prog, `/\`)+1:]
	base = strings.ToLower(strings.TrimRight(base, ". "))
	return strings.HasSuffix(base, ".bat") || strings.HasSuffix(base, ".cmd") || base == "cmd.exe" || base == "cmd"
}

// withoutStream cuts a Windows program path at a ':' after its drive
// (check.bat::$DATA names check.bat's data stream), so the name is judged
// without it. agent.yaml refuses such a program at load; this keeps the
// batch rule right whatever a Template is built from.
func withoutStream(prog string) string {
	rest := prog
	if len(prog) >= 2 && prog[1] == ':' && isLetter(prog[0]) {
		rest = prog[2:]
	}
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		return prog[:len(prog)-len(rest)+i]
	}
	return prog
}

// windowsProgram reports a program written as a Windows path: a drive
// letter, ':' and a separator, or a path starting with two backslashes.
// The agent accepts only absolute paths of its own OS, so on a Unix agent
// this is never true.
func windowsProgram(prog string) bool {
	return (len(prog) >= 3 && isLetter(prog[0]) && prog[1] == ':' && (prog[2] == '\\' || prog[2] == '/')) ||
		strings.HasPrefix(prog, `\\`)
}

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
