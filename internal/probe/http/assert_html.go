package http

import (
	"bytes"
	"context"
	"strings"

	"github.com/noixdev/pulsix/internal/probe/assert"
	"golang.org/x/net/html"
)

// The html assertions' errors and bounds. None quotes the response.
const (
	errNotHTML     = "the response is not HTML"
	errHTMLTooMuch = "the selector took too much work"
	// htmlSelectBudget bounds the steps one html assertion's walk may
	// charge (MatchCost of every node visited). A charged step of a walk
	// that does what it is charged for took 1 to 6 ns (measured: three
	// descendant combinators over 100-deep chains 2 ns, :lang over 400-deep
	// chains 6 ns, ~ over 100 000 siblings 1 ns), so this is about a
	// quarter of a second of CPU at worst. Ordinary selectors on a real
	// 800 KiB page (20 000 elements) charge 50 000 to 400 000, an nth- or
	// first-child walk over a 2 000-row table up to 27 000 000.
	htmlSelectBudget = 40_000_000
	// htmlCheckBudget bounds all of a check's html assertions together:
	// about a second of CPU, like the JSON stage's budget.
	htmlCheckBudget = 160_000_000
)

// htmlDoc parses the body as HTML once; a page nested deeper than the
// parser takes (512 elements) does not parse.
func (d *parsedDocs) htmlDoc() (*html.Node, string) {
	if !d.htmlDone {
		d.htmlDone = true
		doc, err := html.Parse(bytes.NewReader(d.body))
		if err != nil {
			d.htmlErr = errNotHTML
		} else {
			d.html = doc
		}
	}
	return d.html, d.htmlErr
}

// judgeHTML: the selector's matches; count is how many, the text subject
// the first match's text content with its whitespace collapsed, absent
// without a match.
func (d *parsedDocs) judgeHTML(ctx context.Context, a assert.Assertion) verdict {
	doc, problem := d.htmlDoc()
	if problem != "" {
		return failed(problem)
	}
	matches, problem := selectAll(ctx, doc, a, htmlSelectBudget, &d.htmlLeft)
	if problem != "" {
		return failed(problem)
	}
	if a.Subject() == assert.SubjectHTMLCount {
		return number(a, float64(len(matches)))
	}
	if len(matches) == 0 {
		return text(a, "", false)
	}
	return text(a, strings.Join(strings.Fields(textContent(matches[0])), " "), true)
}

// selectAll is the nodes under doc that a's selector matches, in
// document order, like cascadia.QueryAll, but walked here so the walk can
// stop. cascadia's Match cannot be interrupted, so before each node is
// matched its worst case (assert's MatchCost, from the node's depth and
// the largest sibling or child count and attribute weight on its path) is
// charged to the assertion's budget (at most perSelector) and to *left,
// the check's: a node whose charge does not fit stops the walk with
// errHTMLTooMuch before it runs. The walk also stops with errOutOfTime
// once ctx ends, looking before every node.
func selectAll(ctx context.Context, doc *html.Node, a assert.Assertion, perSelector int64, left *int64) ([]*html.Node, string) {
	m := a.Selector()
	budget := min(perSelector, *left)
	// path is the node's place, without the node's own children.
	type visit struct {
		n    *html.Node
		path assert.Path
	}
	var matches []*html.Node
	stack := []visit{{n: doc}}
	for len(stack) > 0 {
		if ctx.Err() != nil {
			return nil, errOutOfTime
		}
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		children := 0
		for c := v.n.FirstChild; c != nil; c = c.NextSibling {
			children++
		}
		at := v.path
		at.Attrs = max(at.Attrs, attrWeight(v.n))
		withChildren := at
		withChildren.Siblings = max(at.Siblings, children)
		withChildren.OwnSiblings = max(at.OwnSiblings, children)
		cost, possible := a.MatchCost(v.n, withChildren)
		if cost > budget {
			return nil, errHTMLTooMuch
		}
		budget -= cost
		*left -= cost
		if v.n != doc && possible && m.Match(v.n) {
			matches = append(matches, v.n)
		}
		// Children pushed last-first, so the first is visited first.
		for c := v.n.LastChild; c != nil; c = c.PrevSibling {
			stack = append(stack, visit{n: c, path: assert.Path{
				Depth:       at.Depth + 1,
				Siblings:    max(at.Siblings, children),
				OwnSiblings: children,
				SiblingSum:  at.SiblingSum + 1 + children,
				Attrs:       at.Attrs,
			}})
		}
	}
	return matches, ""
}

// attrWeight is what reading n's attributes costs a selector: one per
// attribute and one per 64 bytes of names and values.
func attrWeight(n *html.Node) int {
	size := 0
	for _, at := range n.Attr {
		size += len(at.Namespace) + len(at.Key) + len(at.Val)
	}
	return len(n.Attr) + size/64
}

// textContent is the text of n and its descendants, in document order;
// it walks with its own stack, so a deeply nested page cannot exhaust the
// goroutine's.
func textContent(n *html.Node) string {
	var b strings.Builder
	stack := []*html.Node{n}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur.Type == html.TextNode {
			b.WriteString(cur.Data)
			continue
		}
		// Children pushed last-first, so the first is visited first.
		for c := cur.LastChild; c != nil; c = c.PrevSibling {
			stack = append(stack, c)
		}
	}
	return b.String()
}
