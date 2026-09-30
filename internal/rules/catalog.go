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

// TypeCheck validates a tree for storage: known metrics, matching kinds
// (a numeric operator never touches a discrete metric and vice versa),
// window bounds, and a boolean root. The evaluator (Match) never meets a
// tree TypeCheck has rejected, but it still treats one as "does not
// match" rather than trusting that TypeCheck was always called first.
func TypeCheck(n Node) error {
	return checkBool(n)
}

// checkBool validates n as a boolean-producing node — the shape every
// rule's root, and every argument of and/or/not, must have.
func checkBool(n Node) error {
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
			if err := checkNumber(a); err != nil {
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
			if err := checkBool(a); err != nil {
				return err
			}
		}
		return nil
	case KindNot:
		if len(n.Args) != 1 {
			return typeErr(n.Pos, "not: needs exactly one operand, got %d", len(n.Args))
		}
		return checkBool(n.Args[0])
	default:
		return typeErr(n.Pos, "%s: not a boolean expression", n.Kind)
	}
}

// checkNumber validates n as a numeric-producing node — the shape every
// argument of arith and compare, and a window's underlying metric, must
// have.
func checkNumber(n Node) error {
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
			if err := checkNumber(a); err != nil {
				return err
			}
		}
		return nil
	default:
		return typeErr(n.Pos, "%s: not a numeric expression", n.Kind)
	}
}
