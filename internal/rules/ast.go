// Package rules holds the rule language's AST, the catalogue of metric
// names a rule may reference, and the pure evaluation of one rule tree
// against one item's values. Nothing here touches a database, HTTP or the
// clock — later tasks in the v0.3b-2 plan (the parser, the store, the
// evaluation function) build on these types without this package ever
// needing to change.
package rules

// Kind names one node's role in a rule tree.
type Kind string

const (
	KindMetric  Kind = "metric"
	KindWindow  Kind = "window"
	KindFact    Kind = "fact"
	KindNumber  Kind = "number"
	KindString  Kind = "string"
	KindArith   Kind = "arith"
	KindCompare Kind = "compare"
	KindLabel   Kind = "label"
	KindAnd     Kind = "and"
	KindOr      Kind = "or"
	KindNot     Kind = "not"
)

// Node is one node of a rule tree. One struct rather than an interface per
// kind: the tree is stored as jsonb and read back by a switch, and a single
// shape keeps encoding, decoding and equality trivial. Not every field
// applies to every Kind; the ones that do are:
//
//   - KindMetric: Name (the metric, e.g. "cpu.used_pct").
//   - KindWindow: Name (the metric), Agg ("avg"|"min"|"max"|"rate"),
//     Seconds (the window length).
//   - KindFact: Name (the fact key, e.g. "cpu_count").
//   - KindNumber: Num, the literal.
//   - KindString: Str, the literal.
//   - KindArith: Op ("+"|"-"|"*"|"/"), Args[0] and Args[1], both numeric.
//   - KindCompare: Op (">"|">="|"<"|"<="|"=="|"!="), Args[0] and Args[1],
//     both numeric.
//   - KindLabel: Name (the discrete metric), Op ("=="|"!="|"in"). "=="
//     and "!=" compare against Str (one value); "in" compares against
//     Values (several).
//   - KindAnd, KindOr: Args, each itself a boolean node.
//   - KindNot: Args[0], a boolean node.
type Node struct {
	Kind    Kind     `json:"kind"`
	Name    string   `json:"name,omitempty"`     // metric, window and fact names
	Agg     string   `json:"agg,omitempty"`      // avg | min | max | rate
	Seconds int      `json:"window_s,omitempty"` // window length
	Op      string   `json:"op,omitempty"`
	Num     float64  `json:"num,omitempty"`
	Str     string   `json:"str,omitempty"`
	Values  []string `json:"values,omitempty"` // label … in (…)
	Args    []Node   `json:"args,omitempty"`
	Pos     int      `json:"-"` // source offset, parser only
}

// Input is everything one item's evaluation may read: the current values of
// every metric of that (check_key, item), the current labels of every
// discrete metric, the window aggregates asked for, and the host's facts.
type Input struct {
	Metrics map[string]float64
	Labels  map[string]string
	Windows map[WindowKey]float64
	Facts   map[string]string
}

// WindowKey identifies one window aggregate: a metric, an aggregation
// function and a window length in seconds. The store resolves these ahead
// of evaluation (WindowKeys lists what a tree needs) and hands the results
// back pre-computed — Match and Number never see a sample history.
type WindowKey struct {
	Metric  string
	Agg     string
	Seconds int
}
