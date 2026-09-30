package rules

import (
	"fmt"
	"strconv"
	"strings"
)

// Format renders n back into expression source that Parse reads back into
// an equal tree (see the round-trip tests in parse_test.go). It favours
// unambiguous, generously parenthesised output over a minimal one:
// Format's contract is round-tripping, not pretty-printing, and a printer
// that has to prove every omitted paren correct is a printer with far more
// ways to be wrong. This is also what a form-mode rule's Source is
// regenerated from when it is converted to an expression (Task 3).
func Format(n Node) string {
	switch n.Kind {
	case KindCompare:
		return wrapFactor(n.Args[0]) + " " + n.Op + " " + wrapFactor(n.Args[1])
	case KindLabel:
		if n.Op == "in" {
			parts := make([]string, len(n.Values))
			for i, v := range n.Values {
				parts[i] = quoteString(v)
			}
			return n.Name + " in (" + strings.Join(parts, ", ") + ")"
		}
		return n.Name + " " + n.Op + " " + quoteString(n.Str)
	case KindAnd:
		return joinBoolArgs(n.Args, " and ")
	case KindOr:
		return joinBoolArgs(n.Args, " or ")
	case KindNot:
		return "not " + wrapBool(n.Args[0])
	default:
		return formatNumeric(n)
	}
}

// joinBoolArgs renders every argument of an and/or node, each wrapped
// exactly when leaving it bare would change its meaning on re-parsing.
func joinBoolArgs(args []Node, sep string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = wrapBool(a)
	}
	return strings.Join(parts, sep)
}

// wrapBool parenthesises a boolean sub-expression exactly when leaving it
// bare inside another and/or would either bind to the wrong precedence
// level (an "or" nested under "and") or silently flatten into the parent
// (an "and" nested under "and", an "or" nested under "or" — and/or is
// parsed as a flat argument list at each level, so an unparenthesised
// same-kind child would merge into its parent's list on re-parsing
// instead of staying its own node).
func wrapBool(n Node) string {
	if n.Kind == KindAnd || n.Kind == KindOr {
		return "(" + Format(n) + ")"
	}
	return Format(n)
}

// formatNumeric renders an arith-producing node (number, metric, window,
// fact, or an arithmetic operation over them).
func formatNumeric(n Node) string {
	switch n.Kind {
	case KindNumber:
		return strconv.FormatFloat(n.Num, 'f', -1, 64)
	case KindMetric:
		return n.Name
	case KindWindow:
		return fmt.Sprintf("%s(%s, %s)", n.Agg, n.Name, formatDuration(n.Seconds))
	case KindFact:
		return fmt.Sprintf("fact(%s)", quoteString(n.Name))
	case KindArith:
		if isUnaryMinus(n) {
			return "-" + wrapFactor(n.Args[1])
		}
		return wrapFactor(n.Args[0]) + " " + n.Op + " " + wrapFactor(n.Args[1])
	default:
		return "" // unreachable for a tree TypeCheck has accepted
	}
}

// wrapFactor parenthesises n exactly when it is a binary arithmetic node:
// a factor can only ever be an atom, a unary minus (self-delimiting: its
// own "-" prefix already marks where it starts) or a fully parenthesised
// arith, so any other arith operand needs explicit parens to survive
// re-parsing as the same factor rather than spilling into the
// surrounding term or arith.
func wrapFactor(n Node) string {
	if n.Kind == KindArith && !isUnaryMinus(n) {
		return "(" + formatNumeric(n) + ")"
	}
	return formatNumeric(n)
}

// isUnaryMinus reports whether n is the "0 - x" shape parseFactor builds
// for a unary minus, the one KindArith shape Format renders with prefix
// "-" syntax instead of an infix operator.
func isUnaryMinus(n Node) bool {
	return n.Kind == KindArith && n.Op == "-" && len(n.Args) == 2 &&
		n.Args[0].Kind == KindNumber && n.Args[0].Num == 0
}

// formatDuration renders a window length in the largest unit that divides
// it evenly, so "10m" round-trips as "10m" rather than the equally valid
// but less obviously-the-same "600s".
func formatDuration(seconds int) string {
	switch {
	case seconds%3600 == 0:
		return fmt.Sprintf("%dh", seconds/3600)
	case seconds%60 == 0:
		return fmt.Sprintf("%dm", seconds/60)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

// quoteString renders s as a double-quoted literal. The grammar's strings
// never carry an escape syntax (see lexString), so neither does this.
func quoteString(s string) string {
	return `"` + s + `"`
}
