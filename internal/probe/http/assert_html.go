package http

import (
	"context"
	"strings"

	"github.com/noixdev/pulsix/internal/probe/assert"
	"golang.org/x/net/html"
)

// The html assertions' errors and bounds. None quotes the response.
const (
	errHTMLTooMuch = "the selector took too much work"
	// htmlSelectBudget bounds the steps one html assertion's walk may
	// take (selectAll: one per node, child and condition, plus attribute
	// bytes and, for an of-type pseudo-class, element names). The walk
	// is linear in the page. What a step costs depends on its kind
	// (measured without -race, backlog 3.90, on a machine shared with
	// other test runs): a condition or attribute byte 4 to 10 ns (15
	// parts of 8 compounds over 500-deep chains, 14 :nth-child parts
	// or 15 parts of 8-compound ~ chains over 150 000 siblings, a
	// 900 KB class list); an of-type count over 150 000 distinct
	// sibling names about 17 ns; a bare node visit (node, child and
	// one condition: "p" over 150 000 siblings) 20 to 30 ns, the
	// dearest — 3.13b's walk measured the same there. A whole budget
	// of the dearest kind would be about 0.45 s, but bare visits are
	// capped by the page: a walk visits at most htmlMaxNodes nodes,
	// about 1.2 M such steps (35 ms), and an assertion that spends its
	// budget does so mostly on conditions, about 0.15 s. Ordinary
	// selectors on a real 800 KiB page (20 000 elements) take 140 000
	// to 420 000.
	htmlSelectBudget = 15_000_000
	// htmlCheckBudget bounds all of a check's html assertions together,
	// their walks and the text they read (collapsedText). Spent at the
	// dearest step it would be about 1.8 s, but a check has at most
	// assert.MaxEntries (32) assertions, whose bare visits come to at
	// most about 38 M steps (1.2 s at 30 ns) and the rest of the budget
	// to conditions (0.2 s): about 1.4 s at worst, typically far less.
	// That is above the "about a second" of the JSON stage's budget but
	// well inside the probe's deadline (10 s by default, at most 60 s),
	// which the walk looks at before every node and stops at, so the
	// budget is kept.
	htmlCheckBudget = 60_000_000
)

// htmlDoc builds the body's HTML tree once (buildHTML).
func (d *parsedDocs) htmlDoc(ctx context.Context) (*html.Node, string) {
	if !d.htmlDone {
		d.htmlDone = true
		d.html, d.htmlErr = buildHTML(ctx, d.body)
	}
	return d.html, d.htmlErr
}

// judgeHTML: the selector's matches; count is how many, the text subject
// the first match's text content with its whitespace collapsed, absent
// without a match. The walk and reading the text both come out of the
// check's html budget.
func (d *parsedDocs) judgeHTML(ctx context.Context, a assert.Assertion) verdict {
	doc, problem := d.htmlDoc(ctx)
	if problem != "" {
		return failed(problem)
	}
	matches, problem := selectAll(ctx, doc, a.Selector(), htmlSelectBudget, &d.htmlLeft)
	if problem != "" {
		return failed(problem)
	}
	if a.Subject() == assert.SubjectHTMLCount {
		return number(a, float64(len(matches)))
	}
	if len(matches) == 0 {
		return d.work.text(a, "", false)
	}
	text, ok := collapsedText(matches[0], &d.htmlLeft)
	if !ok {
		return failed(errHTMLTooMuch)
	}
	return d.work.text(a, text, true)
}

// collapsedText is the text of n and its descendants, in document order,
// with its whitespace collapsed. It costs one step of left per node and
// one per byte of text, taken as it goes; false when left runs out. It
// walks with its own stack, so a deeply nested page cannot exhaust the
// goroutine's.
func collapsedText(n *html.Node, left *int64) (string, bool) {
	var b strings.Builder
	stack := []*html.Node{n}
	for len(stack) > 0 {
		if *left < 1 {
			return "", false
		}
		*left--
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
	text := b.String()
	if int64(len(text)) > *left {
		return "", false
	}
	*left -= int64(len(text))
	return strings.Join(strings.Fields(text), " "), true
}
