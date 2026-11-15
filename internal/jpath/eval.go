package jpath

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
)

// ErrTooMuch is Select's error when an evaluation would visit more nodes
// than its budget.
var ErrTooMuch = errors.New("the path selected too much")

// MaxNumberLen bounds the text of a document number the subset compares:
// a longer one (a million digits fit in a body) is not comparable, so a
// comparison costs a fixed amount whatever the document. internal/probe/http
// reports such a number, when a path selects it, as out of range.
const MaxNumberLen = 64

// ctxEvery is how many node visits Select makes between two looks at its
// context: a few milliseconds of work at most.
const ctxEvery = 1 << 16

// evaluator charges every node a segment reaches, every child a filter or
// a descendant segment looks at, every step of an @ path and every node
// of a filter expression it evaluates to a budget. Every unit of work is
// one of these or a fixed amount next to one, so the work is a constant
// times the budget whatever the document; ops counts the units for the
// tests that pin this. Every ctxEvery visits it looks at ctx and, once
// ctx has ended, stops as if the budget had run out, keeping ctx's error.
type evaluator struct {
	ctx  context.Context
	left int
	// tick is the visits left until the next look at ctx.
	tick int
	err  error
	ops  int
}

func newEvaluator(ctx context.Context, budget int) *evaluator {
	return &evaluator{ctx: ctx, left: budget, tick: ctxEvery}
}

// visit charges one node and reports whether the budget still holds and
// ctx has not ended.
func (e *evaluator) visit() bool {
	e.left--
	if e.tick--; e.tick == 0 {
		e.tick = ctxEvery
		if err := e.ctx.Err(); err != nil && e.err == nil {
			e.err = err
			e.left = min(e.left, -1)
		}
	}
	return e.left >= 0
}

// Select returns the nodes path selects in doc, a value as encoding/json
// decodes it (numbers as json.Number or float64), as RFC 9535 defines the
// node list, and the node visits it charged, at most budget. It stops
// with ErrTooMuch after budget node visits, and with ctx's error once ctx
// ends: it looks at ctx before it starts and every 65 536 visits.
func (p *Path) Select(ctx context.Context, doc any, budget int) (nodes []any, used int, err error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	e := newEvaluator(ctx, budget)
	nodes, err = e.run(p, doc)
	return nodes, budget - max(e.left, 0), err
}

func (e *evaluator) run(p *Path, doc any) ([]any, error) {
	nodes := []any{doc}
	for _, s := range p.segs {
		var out []any
		for _, n := range nodes {
			if s.descendant {
				out = e.descend(n, s.sel, out)
			} else {
				out = e.apply(n, s.sel, out)
			}
			if e.left < 0 {
				if e.err != nil {
					return nil, e.err
				}
				return nil, ErrTooMuch
			}
		}
		nodes = out
	}
	return nodes, nil
}

// descend applies sel to n and to each of n's descendants, in document
// order.
func (e *evaluator) descend(n any, sel selector, out []any) []any {
	e.ops++
	out = e.apply(n, sel, out)
	switch v := n.(type) {
	case []any:
		for _, c := range v {
			if e.left < 0 || !e.visit() {
				return out
			}
			out = e.descend(c, sel, out)
		}
	case map[string]any:
		for _, c := range v {
			if e.left < 0 || !e.visit() {
				return out
			}
			out = e.descend(c, sel, out)
		}
	}
	return out
}

// apply appends what sel selects from n.
func (e *evaluator) apply(n any, sel selector, out []any) []any {
	e.ops++
	switch sel.kind {
	case selName:
		if obj, ok := n.(map[string]any); ok {
			if v, ok := obj[sel.name]; ok && e.visit() {
				out = append(out, v)
			}
		}
	case selIndex:
		if arr, ok := n.([]any); ok {
			if v, ok := index(arr, sel.index); ok && e.visit() {
				out = append(out, v)
			}
		}
	case selWildcard, selFilter:
		keep := func(c any) bool {
			return e.visit() && (sel.kind == selWildcard || e.test(sel.filter, c))
		}
		switch v := n.(type) {
		case []any:
			for _, c := range v {
				if keep(c) {
					out = append(out, c)
				}
				if e.left < 0 {
					return out
				}
			}
		case map[string]any:
			for _, c := range v {
				if keep(c) {
					out = append(out, c)
				}
				if e.left < 0 {
					return out
				}
			}
		}
	}
	return out
}

// index is arr[i], i counted from the end when negative.
func index(arr []any, i int64) (any, bool) {
	if i < 0 {
		i += int64(len(arr))
	}
	if i < 0 || i >= int64(len(arr)) {
		return nil, false
	}
	return arr[i], true
}

// query evaluates an @ path at node: the one node it reaches, or false
// for Nothing.
func (e *evaluator) query(node any, q []step) (any, bool) {
	for _, s := range q {
		e.ops++
		var ok bool
		if s.isName {
			obj, isObj := node.(map[string]any)
			if !isObj {
				return nil, false
			}
			node, ok = obj[s.name]
		} else {
			arr, isArr := node.([]any)
			if !isArr {
				return nil, false
			}
			node, ok = index(arr, s.index)
		}
		if !ok || !e.visit() {
			return nil, false
		}
	}
	return node, true
}

// test evaluates the filter expression x at node. It counts the
// evaluation as one unit of work here, apart from the visit x charges
// itself, so a test notices an expression kind that goes uncharged.
func (e *evaluator) test(x expr, node any) bool {
	e.ops++
	return x.eval(e, node)
}

func (x orExpr) eval(e *evaluator, node any) bool {
	if !e.visit() {
		return false
	}
	for _, t := range x {
		if e.test(t, node) {
			return true
		}
	}
	return false
}

func (x andExpr) eval(e *evaluator, node any) bool {
	if !e.visit() {
		return false
	}
	for _, t := range x {
		if !e.test(t, node) {
			return false
		}
	}
	return true
}

func (x notExpr) eval(e *evaluator, node any) bool {
	return e.visit() && !e.test(x.x, node)
}

func (x existExpr) eval(e *evaluator, node any) bool {
	if !e.visit() {
		return false
	}
	_, ok := e.query(node, x.q)
	return ok
}

// eval compares as RFC 9535 section 2.3.5.2.2 does: Nothing equals no
// literal; numbers compare as numbers, strings by code point; true, false
// and null only for equality; values of different types are unequal and
// unordered.
func (x cmpExpr) eval(e *evaluator, node any) bool {
	if !e.visit() {
		return false
	}
	v, ok := e.query(node, x.q)
	eq := func() bool { return ok && equal(v, x.lit) }
	lt := func() bool { return ok && less(v, x.lit) }
	gt := func() bool { return ok && greater(v, x.lit) }
	switch x.op {
	case "==":
		return eq()
	case "!=":
		return !eq()
	case "<":
		return lt()
	case "<=":
		return lt() || eq()
	case ">":
		return gt()
	case ">=":
		return gt() || eq()
	}
	return false
}

// number reads a JSON number of the document; one longer than
// MaxNumberLen is not comparable.
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		if len(n) > MaxNumberLen {
			return 0, false
		}
		// A valid number out of range is ±Inf, still ordered.
		f, _ := strconv.ParseFloat(string(n), 64)
		return f, true
	case float64:
		return n, true
	}
	return 0, false
}

func equal(v any, lit literal) bool {
	switch lit.kind {
	case litNumber:
		f, ok := number(v)
		return ok && f == lit.num
	case litString:
		s, ok := v.(string)
		return ok && s == lit.str
	case litBool:
		b, ok := v.(bool)
		return ok && b == lit.b
	case litNull:
		return v == nil
	}
	return false
}

func less(v any, lit literal) bool {
	switch lit.kind {
	case litNumber:
		f, ok := number(v)
		return ok && f < lit.num
	case litString:
		s, ok := v.(string)
		return ok && s < lit.str
	}
	return false
}

func greater(v any, lit literal) bool {
	switch lit.kind {
	case litNumber:
		f, ok := number(v)
		return ok && f > lit.num
	case litString:
		s, ok := v.(string)
		return ok && s > lit.str
	}
	return false
}
