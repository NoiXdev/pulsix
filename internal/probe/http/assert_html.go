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
	// htmlSelectBudget bounds the cost one html assertion's walk may
	// charge: a node visit costs one per part of the selector, plus the
	// node's depth when a part walks its ancestors and its siblings when
	// a part walks those. A real 1 MiB page (some 20 000 elements, a few
	// dozen deep) costs well under a tenth of it with a descendant
	// combinator; a sibling walk fits lists of up to about 2 000 entries.
	htmlSelectBudget = 4_000_000
	// htmlCheckBudget bounds all of a check's html assertions together.
	htmlCheckBudget = 16_000_000
	// htmlCtxEvery is how many node visits pass between looks at the
	// probe's context.
	htmlCtxEvery = 64
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
// stop: each visit is charged to the assertion's budget (at most
// perSelector) and to *left, the check's; past either it stops with
// errHTMLTooMuch, and once ctx ends with errOutOfTime. A match of one node
// is bounded by the selector's shape (assert.Parse): at most one walk of
// its ancestors or siblings per part.
func selectAll(ctx context.Context, doc *html.Node, a assert.Assertion, perSelector int, left *int) ([]*html.Node, string) {
	parts, ancestors, siblings := a.SelectorShape()
	m := a.Selector()
	budget := min(perSelector, *left)
	type visit struct {
		n               *html.Node
		depth, siblings int
	}
	var matches []*html.Node
	stack := []visit{{n: doc}}
	for visits := 0; len(stack) > 0; visits++ {
		if visits%htmlCtxEvery == 0 && ctx.Err() != nil {
			return nil, errOutOfTime
		}
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		walk := 1
		if ancestors {
			walk += v.depth
		}
		if siblings {
			walk += v.siblings
		}
		cost := parts * walk
		budget -= cost
		*left -= cost
		if budget < 0 || *left < 0 {
			return nil, errHTMLTooMuch
		}
		if v.n != doc && m.Match(v.n) {
			matches = append(matches, v.n)
		}
		// Children pushed last-first, so the first is visited first; a
		// sibling walk may cover all of them.
		n := 0
		for c := v.n.FirstChild; c != nil; c = c.NextSibling {
			n++
		}
		for c := v.n.LastChild; c != nil; c = c.PrevSibling {
			stack = append(stack, visit{n: c, depth: v.depth + 1, siblings: n})
		}
	}
	return matches, ""
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
