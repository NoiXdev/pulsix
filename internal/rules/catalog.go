package rules

import (
	"errors"
	"fmt"
	"time"
)

// MetricKind reports what a metric name carries: a number a threshold can
// compare against, or a discrete label a set of states compares against.
type MetricKind int

const (
	MetricUnknown MetricKind = iota
	MetricNumeric
	MetricDiscrete
)

// CatalogueEntry names the check_key a metric belongs to and what kind of
// value it carries.
type CatalogueEntry struct {
	CheckKey string
	Kind     MetricKind
}

// Catalogue is the set of metric names the shipped plugins report, with
// the check key each belongs to. It is a table in code for the same
// reason the plugin list in internal/checks is: an unknown metric must be
// refused when the rule is written, not silently never match.
//
// OBLIGATION: a new plugin metric is added here in the same change that
// adds the plugin, or no rule can ever name it. The entries below are
// exactly what internal/agent/checks's four plugins emit today
// (cpu.go, memory.go, disk.go, service.go) — cross-check any edit here
// against those files, not the other way around.
func Catalogue() map[string]CatalogueEntry {
	return map[string]CatalogueEntry{
		"cpu.used_pct":         {CheckKey: "cpu", Kind: MetricNumeric},
		"cpu.load1":            {CheckKey: "cpu", Kind: MetricNumeric},
		"cpu.load5":            {CheckKey: "cpu", Kind: MetricNumeric},
		"cpu.load15":           {CheckKey: "cpu", Kind: MetricNumeric},
		"mem.used_pct":         {CheckKey: "memory", Kind: MetricNumeric},
		"mem.available_bytes":  {CheckKey: "memory", Kind: MetricNumeric},
		"swap.used_pct":        {CheckKey: "memory", Kind: MetricNumeric},
		"disk.used_pct":        {CheckKey: "disk", Kind: MetricNumeric},
		"disk.free_bytes":      {CheckKey: "disk", Kind: MetricNumeric},
		"disk.inodes_used_pct": {CheckKey: "disk", Kind: MetricNumeric},
		"service.state":        {CheckKey: "service", Kind: MetricDiscrete},
	}
}

// The rule language's window bounds: below MinWindow a window is not worth
// aggregating, above MaxWindow it no longer fits the bounded scan the
// design spec commits to (raw check_samples, one or two daily chunks).
const (
	MinWindow = time.Minute
	MaxWindow = 24 * time.Hour
)

// MaxWindowsPerRule bounds how many distinct window aggregates one tree
// may name (backlog 7.58). Each one is another aggregate the ticker's
// window statement computes over up to MaxWindow of raw samples, for every
// matching item of every host the rule reaches, on every evaluation. The
// expression's byte bound did not limit that: 4 KiB holds over a hundred
// 24-hour windows, enough for one org-scope rule to make each host's
// evaluation outlast the batch deadline. Distinct means WindowRequests'
// own identity — metric, aggregate and length in seconds — so a window
// repeated in the tree, however it is spelled, is fetched once and counts
// once. TypeCheck counts per tree, which is per rule: only an expression
// rule's trigger can hold a window (Form.Compile never emits one, and an
// expression rule has no clear tree). A rule already stored with more is
// not re-checked: rule loading reads the stored tree and never runs
// TypeCheck again, so such a rule keeps evaluating exactly as before.
const MaxWindowsPerRule = 16

// ErrInvalidRule is the sentinel every TypeCheck failure wraps. Callers
// that only care whether a tree is valid use errors.Is(err, ErrInvalidRule);
// callers that need the offending position (the parser, the API's error
// response) use errors.As into *TypeCheckError.
var ErrInvalidRule = errors.New("invalid rule")

// TypeCheckError locates a TypeCheck failure at the offending node's Pos
// (see Node.Pos), so a parser can point straight at the piece of source
// text the failing node came from.
type TypeCheckError struct {
	Pos int
	Msg string
}

func (e *TypeCheckError) Error() string {
	return fmt.Sprintf("invalid rule at %d: %s", e.Pos, e.Msg)
}

func (e *TypeCheckError) Unwrap() error {
	return ErrInvalidRule
}

func typeErr(pos int, format string, args ...any) error {
	return &TypeCheckError{Pos: pos, Msg: fmt.Sprintf(format, args...)}
}

// maxRuleDepth bounds how deeply a rule tree may nest. Every walker in
// this package is a plain recursion over Node.Args — TypeCheck here,
// Match and Number in eval_node.go, Format, and encoding/json in both
// directions — so tree depth is stack depth, and an unbounded tree is a
// `fatal error: stack overflow`. That is a runtime fatal, not a panic:
// neither the HTTP recover middleware nor Ticker.tickSafely can catch it,
// and the manager dies taking every tenant with it.
//
// TypeCheck is where the bound belongs because it is the gate every tree
// an operator can author passes: Parse ends in it, and buildRule is the
// only writer of ast/clear_ast. Form.Compile does NOT call TypeCheck — it
// does not need to, because it emits a tree of depth two at most (one
// KindCompare over two leaves, or one bare KindLabel), so no form rule can
// approach this bound by construction. Between the two, a tree read back
// from jsonb is bounded: no deeper one could ever have been written.
//
// 64 is far past anything a rule a human writes needs — nesting is two or
// three levels, and and/or are flat argument lists rather than nested
// pairs, so a long conjunction costs one level, not one per term. The one
// shape that reaches it is a chain the parser builds iteratively: "1+1+1+…"
// left-folds into a tree one level deeper per term (see parseArith), which
// is exactly why the parser's own frame counter cannot stand in for this
// one.
const maxRuleDepth = 64

// checkTreeBelongsTo refuses any node in n naming a catalogue metric that
// belongs to a check other than checkKey. It is not part of TypeCheck,
// which validates a tree on its own terms and has no rule to read a check
// key off; a caller that knows the binding runs this as a second pass.
//
// Why it has to exist: the evaluator builds one Input per
// ItemKey{CheckKey, Item}, populated only from that item's own tuples
// (buildInputs). A rule bound to (disk, /var, disk.used_pct) whose
// expression reads mem.used_pct therefore finds nothing in Input.Metrics,
// Number returns ok=false, and Match returns false — for ever, silently.
// The operator gets a 201, a green tuple and no page when memory fills.
// That is the precise failure Catalogue's own doc comment says the
// catalogue exists to prevent ("an unknown metric must be refused when the
// rule is written, not silently never match"), reached by a different
// door: the metric is known, it is just known to belong elsewhere.
//
// Labels are checked alongside metrics and windows. The mechanism is
// identical — Input.Labels is filled from the same item's tuples — and
// leaving them out would only mean one of the three node kinds that names
// a catalogue metric could still never match.
//
// Depth is bounded by TypeCheck, which every caller runs first.
func checkTreeBelongsTo(n Node, checkKey string) error {
	switch n.Kind {
	case KindMetric, KindWindow, KindLabel:
		if entry, ok := Catalogue()[n.Name]; ok && entry.CheckKey != checkKey {
			return typeErr(n.Pos, "metric %q belongs to check %q, not %q — a rule can only read the metrics of its own check",
				n.Name, entry.CheckKey, checkKey)
		}
	}
	for _, a := range n.Args {
		if err := checkTreeBelongsTo(a, checkKey); err != nil {
			return err
		}
	}
	return nil
}

// TypeCheck validates a tree for storage: known metrics, matching kinds
// (a numeric operator never touches a discrete metric and vice versa),
// window bounds, at most MaxWindowsPerRule distinct windows, nesting
// within maxRuleDepth, and a boolean root. The
// evaluator (Match) never meets a tree TypeCheck has rejected, but it
// still treats one as "does not match" rather than trusting that TypeCheck
// was always called first.
func TypeCheck(n Node) error {
	return checkBool(n, 0, make(map[WindowKey]bool))
}

// checkBool validates n as a boolean-producing node — the shape every
// rule's root, and every argument of and/or/not, must have. depth is how
// many ancestors n has; see maxRuleDepth for what it guards. windows is
// the set of distinct window aggregates seen so far in the whole tree,
// shared across the walk so the MaxWindowsPerRule bound is counted in the
// same pass that checks each window's length.
func checkBool(n Node, depth int, windows map[WindowKey]bool) error {
	if depth > maxRuleDepth {
		return typeErr(n.Pos, "nests deeper than %d levels", maxRuleDepth)
	}
	switch n.Kind {
	case KindCompare:
		switch n.Op {
		case ">", ">=", "<", "<=", "==", "!=":
		default:
			return typeErr(n.Pos, "compare: unknown operator %q", n.Op)
		}
		if len(n.Args) != 2 {
			return typeErr(n.Pos, "compare: needs exactly two operands, got %d", len(n.Args))
		}
		for _, a := range n.Args {
			if err := checkNumber(a, depth+1, windows); err != nil {
				return err
			}
		}
		return nil
	case KindLabel:
		switch n.Op {
		case "==", "!=", "in":
		default:
			return typeErr(n.Pos, "label: unknown operator %q", n.Op)
		}
		entry, ok := Catalogue()[n.Name]
		if !ok {
			return typeErr(n.Pos, "label: unknown metric %q", n.Name)
		}
		if entry.Kind != MetricDiscrete {
			return typeErr(n.Pos, "label: metric %q is not discrete", n.Name)
		}
		return nil
	case KindAnd, KindOr:
		if len(n.Args) == 0 {
			return typeErr(n.Pos, "%s: needs at least one operand", n.Kind)
		}
		for _, a := range n.Args {
			if err := checkBool(a, depth+1, windows); err != nil {
				return err
			}
		}
		return nil
	case KindNot:
		if len(n.Args) != 1 {
			return typeErr(n.Pos, "not: needs exactly one operand, got %d", len(n.Args))
		}
		return checkBool(n.Args[0], depth+1, windows)
	default:
		return typeErr(n.Pos, "%s: not a boolean expression", n.Kind)
	}
}

// checkNumber validates n as a numeric-producing node — the shape every
// argument of arith and compare, and a window's underlying metric, must
// have. depth is how many ancestors n has; see maxRuleDepth. windows is
// checkBool's set of distinct windows, see there.
func checkNumber(n Node, depth int, windows map[WindowKey]bool) error {
	if depth > maxRuleDepth {
		return typeErr(n.Pos, "nests deeper than %d levels", maxRuleDepth)
	}
	switch n.Kind {
	case KindNumber:
		return nil
	case KindMetric:
		entry, ok := Catalogue()[n.Name]
		if !ok {
			return typeErr(n.Pos, "metric: unknown metric %q", n.Name)
		}
		if entry.Kind != MetricNumeric {
			return typeErr(n.Pos, "metric: %q is not numeric", n.Name)
		}
		return nil
	case KindWindow:
		entry, ok := Catalogue()[n.Name]
		if !ok {
			return typeErr(n.Pos, "window: unknown metric %q", n.Name)
		}
		if entry.Kind != MetricNumeric {
			return typeErr(n.Pos, "window: %q is not numeric", n.Name)
		}
		if !knownAgg(n.Agg) {
			return typeErr(n.Pos, "window: unknown aggregate %q", n.Agg)
		}
		d := time.Duration(n.Seconds) * time.Second
		if d < MinWindow || d > MaxWindow {
			return typeErr(n.Pos, "window: length %s outside [%s, %s]", d, MinWindow, MaxWindow)
		}
		key := WindowKey{Metric: n.Name, Agg: n.Agg, Seconds: n.Seconds}
		if !windows[key] {
			if len(windows) >= MaxWindowsPerRule {
				return typeErr(n.Pos, "window: a rule may use at most %d distinct windows", MaxWindowsPerRule)
			}
			windows[key] = true
		}
		return nil
	case KindFact:
		if n.Name == "" {
			return typeErr(n.Pos, "fact: needs a name")
		}
		return nil
	case KindArith:
		switch n.Op {
		case "+", "-", "*", "/":
		default:
			return typeErr(n.Pos, "arith: unknown operator %q", n.Op)
		}
		if len(n.Args) != 2 {
			return typeErr(n.Pos, "arith: needs exactly two operands, got %d", len(n.Args))
		}
		for _, a := range n.Args {
			if err := checkNumber(a, depth+1, windows); err != nil {
				return err
			}
		}
		return nil
	default:
		return typeErr(n.Pos, "%s: not a numeric expression", n.Kind)
	}
}
