package rules

import "fmt"

// Form is the editor's shape: one comparison against a literal, plus an
// optional clear condition for hysteresis. It is a deliberately narrower
// shape than Node — the metric always sits on the left and a literal on
// the right, never two metrics, an arithmetic expression or a window — the
// price the simple editor pays for never needing to parse or format
// anything. Compile and AsForm are how a rule crosses between this shape
// and the general tree the evaluator and the expression editor both use.
type Form struct {
	Metric     string
	Op         string   // numeric: > >= < <= == != ; discrete: == != in
	Value      float64  // numeric metrics
	Labels     []string // discrete metrics
	ClearOp    string   // "" when the rule has no clear condition
	ClearValue float64
}

// Compile turns f into its trigger tree and its clear tree. The clear tree
// is the zero Node when ClearOp is empty — callers distinguish "no clear
// condition" by that zero value rather than a separate boolean, the same
// way the rest of this package treats an absent Node.
//
// Compile refuses: a metric outside the catalogue, an operator that does
// not fit the metric's kind, an "in" list with no labels, and a clear
// condition on a discrete metric — a label does not straddle a threshold,
// so hysteresis is meaningless there.
func (f Form) Compile() (trigger, clear Node, err error) {
	entry, ok := Catalogue()[f.Metric]
	if !ok {
		return Node{}, Node{}, fmt.Errorf("form: unknown metric %q: %w", f.Metric, ErrInvalidRule)
	}

	trigger, err = buildComparison(f.Metric, entry, f.Op, f.Value, f.Labels)
	if err != nil {
		return Node{}, Node{}, err
	}

	if f.ClearOp == "" {
		return trigger, Node{}, nil
	}
	if entry.Kind == MetricDiscrete {
		return Node{}, Node{}, fmt.Errorf("form: clear condition on discrete metric %q: a label does not straddle a threshold: %w", f.Metric, ErrInvalidRule)
	}
	clear, err = buildComparison(f.Metric, entry, f.ClearOp, f.ClearValue, nil)
	if err != nil {
		return Node{}, Node{}, err
	}
	return trigger, clear, nil
}

// buildComparison builds the one node Compile ever produces for a single
// side (trigger or clear) of a Form: a numeric metric compared against a
// literal, or (via buildLabel) a discrete metric compared against one or
// several labels.
func buildComparison(name string, entry CatalogueEntry, op string, value float64, labels []string) (Node, error) {
	if entry.Kind == MetricDiscrete {
		return buildLabel(name, op, labels)
	}
	switch op {
	case ">", ">=", "<", "<=", "==", "!=":
	default:
		return Node{}, fmt.Errorf("form: operator %q does not fit numeric metric %q: %w", op, name, ErrInvalidRule)
	}
	return Node{
		Kind: KindCompare,
		Op:   op,
		Args: []Node{
			{Kind: KindMetric, Name: name},
			{Kind: KindNumber, Num: value},
		},
	}, nil
}

// buildLabel builds the KindLabel node for a discrete metric's "==", "!="
// or "in" operator. "==" and "!=" need exactly one label (Str carries the
// single value); "in" needs at least one (Values carries the set) — an
// empty "in" list would compile to a label condition no state could ever
// satisfy, which is the brief's own rejection case, not a rule anyone
// meant to write.
func buildLabel(name, op string, labels []string) (Node, error) {
	switch op {
	case "==", "!=":
		if len(labels) != 1 {
			return Node{}, fmt.Errorf("form: operator %q on discrete metric %q needs exactly one label, got %d: %w", op, name, len(labels), ErrInvalidRule)
		}
		return Node{Kind: KindLabel, Name: name, Op: op, Str: labels[0]}, nil
	case "in":
		if len(labels) == 0 {
			return Node{}, fmt.Errorf("form: operator \"in\" on discrete metric %q needs at least one label: %w", name, ErrInvalidRule)
		}
		return Node{Kind: KindLabel, Name: name, Op: "in", Values: append([]string(nil), labels...)}, nil
	default:
		return Node{}, fmt.Errorf("form: operator %q does not fit discrete metric %q: %w", op, name, ErrInvalidRule)
	}
}

// AsForm recovers the Form a tree pair encodes, reporting false when the
// trees are outside the form subset — an "or" at the root, a window or
// fact node, an arithmetic expression, a comparison of two metrics, an
// unknown metric, or a clear condition that does not itself compare the
// same metric against a literal. That false is what makes the editor's
// one-way switch to expression mode visible rather than lossy: once a rule
// leaves the form's shape, AsForm says so instead of guessing.
func AsForm(trigger, clear Node) (Form, bool) {
	metric, op, value, labels, ok := recognizeComparison(trigger)
	if !ok {
		return Form{}, false
	}
	f := Form{Metric: metric, Op: op, Value: value, Labels: labels}

	if clear.Kind == "" {
		return f, true
	}
	clearMetric, clearOp, clearValue, clearLabels, ok := recognizeComparison(clear)
	if !ok || clearMetric != metric || clearLabels != nil {
		return Form{}, false
	}
	f.ClearOp = clearOp
	f.ClearValue = clearValue
	return f, true
}

// recognizeComparison reports the Form fields one side of a tree pair
// encodes, when — and only when — it has exactly the shape Compile builds:
// a metric compared against a number literal, or a label compared against
// one or several string literals, both against a metric the catalogue
// still knows and of the matching kind. Anything else (an "or", a window,
// a fact, an arithmetic node, two metrics compared against each other, an
// unrecognized operator) reports ok = false.
func recognizeComparison(n Node) (metric, op string, value float64, labels []string, ok bool) {
	switch n.Kind {
	case KindCompare:
		if len(n.Args) != 2 {
			return "", "", 0, nil, false
		}
		left, right := n.Args[0], n.Args[1]
		if left.Kind != KindMetric || right.Kind != KindNumber {
			return "", "", 0, nil, false
		}
		entry, known := Catalogue()[left.Name]
		if !known || entry.Kind != MetricNumeric {
			return "", "", 0, nil, false
		}
		switch n.Op {
		case ">", ">=", "<", "<=", "==", "!=":
		default:
			return "", "", 0, nil, false
		}
		return left.Name, n.Op, right.Num, nil, true
	case KindLabel:
		entry, known := Catalogue()[n.Name]
		if !known || entry.Kind != MetricDiscrete {
			return "", "", 0, nil, false
		}
		switch n.Op {
		case "==", "!=":
			return n.Name, n.Op, 0, []string{n.Str}, true
		case "in":
			if len(n.Values) == 0 {
				return "", "", 0, nil, false
			}
			return n.Name, "in", 0, append([]string(nil), n.Values...), true
		default:
			return "", "", 0, nil, false
		}
	default:
		return "", "", 0, nil, false
	}
}
