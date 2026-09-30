package rules

import "strconv"

// Match reports whether n holds for in. A tree that cannot be evaluated
// does not match: missing metric, absent or non-numeric fact, empty
// window, division by zero. Match never returns an error and never
// escalates — a rule engine that fails loudly into the worst severity is
// a rule engine that pages on its own bugs.
func Match(n Node, in Input) bool {
	switch n.Kind {
	case KindCompare:
		if len(n.Args) != 2 {
			return false
		}
		l, lok := Number(n.Args[0], in)
		r, rok := Number(n.Args[1], in)
		if !lok || !rok {
			return false
		}
		switch n.Op {
		case ">":
			return l > r
		case ">=":
			return l >= r
		case "<":
			return l < r
		case "<=":
			return l <= r
		case "==":
			return l == r
		case "!=":
			return l != r
		default:
			return false
		}
	case KindLabel:
		val, ok := in.Labels[n.Name]
		if !ok {
			return false
		}
		switch n.Op {
		case "==":
			return val == n.Str
		case "!=":
			return val != n.Str
		case "in":
			for _, v := range n.Values {
				if v == val {
					return true
				}
			}
			return false
		default:
			return false
		}
	case KindAnd:
		// Short-circuits: the first false argument decides the result
		// without evaluating any argument after it.
		for _, a := range n.Args {
			if !Match(a, in) {
				return false
			}
		}
		return true
	case KindOr:
		// Short-circuits: the first true argument decides the result
		// without evaluating any argument after it.
		for _, a := range n.Args {
			if Match(a, in) {
				return true
			}
		}
		return false
	case KindNot:
		if len(n.Args) != 1 {
			return false
		}
		return !Match(n.Args[0], in)
	default:
		return false
	}
}

// Number evaluates a numeric sub-tree. ok is false when any leaf is
// missing: a metric absent from in.Metrics, a fact absent from in.Facts
// or present but not parseable as a number, a window aggregate absent
// from in.Windows, or a division by zero anywhere under n.
func Number(n Node, in Input) (v float64, ok bool) {
	switch n.Kind {
	case KindNumber:
		return n.Num, true
	case KindMetric:
		v, ok := in.Metrics[n.Name]
		return v, ok
	case KindWindow:
		key := WindowKey{Metric: n.Name, Agg: n.Agg, Seconds: n.Seconds}
		v, ok := in.Windows[key]
		return v, ok
	case KindFact:
		s, ok := in.Facts[n.Name]
		if !ok {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	case KindArith:
		if len(n.Args) != 2 {
			return 0, false
		}
		l, lok := Number(n.Args[0], in)
		r, rok := Number(n.Args[1], in)
		if !lok || !rok {
			return 0, false
		}
		switch n.Op {
		case "+":
			return l + r, true
		case "-":
			return l - r, true
		case "*":
			return l * r, true
		case "/":
			if r == 0 {
				return 0, false
			}
			return l / r, true
		default:
			return 0, false
		}
	default:
		return 0, false
	}
}

// WindowKeys lists the window aggregates n needs, deduplicated, in the
// order they first appear in the tree. The store resolves each one ahead
// of evaluation (one statement per host, per the design's evaluation
// section) so Match and Number never need a sample history of their own.
func WindowKeys(n Node) []WindowKey {
	seen := make(map[WindowKey]bool)
	var keys []WindowKey
	collectWindowKeys(n, seen, &keys)
	return keys
}

func collectWindowKeys(n Node, seen map[WindowKey]bool, keys *[]WindowKey) {
	if n.Kind == KindWindow {
		key := WindowKey{Metric: n.Name, Agg: n.Agg, Seconds: n.Seconds}
		if !seen[key] {
			seen[key] = true
			*keys = append(*keys, key)
		}
	}
	for _, a := range n.Args {
		collectWindowKeys(a, seen, keys)
	}
}
