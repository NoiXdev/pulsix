package http

import (
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
	// take (selectAll: one per node, child and condition, plus attribute
	// bytes). The walk is linear in the page; a step took at most about
	// 17 ns (measured: 15 comma parts of 8 compounds over 500-deep
	// chains 16 ns, 150 000 siblings 17 ns, a 900 KB class list 11 ns),
	// so this is about a quarter of a second of CPU. Ordinary selectors
	// on a real 800 KiB page (20 000 elements) take 140 000 to 420 000.
	htmlSelectBudget = 15_000_000
	// htmlCheckBudget bounds all of a check's html assertions together:
	// about a second of CPU, like the JSON stage's budget.
	htmlCheckBudget = 60_000_000
)

// htmlDoc parses the body as HTML once (parseHTML); a page nested deeper
// than the parser takes (512 elements) does not parse.
func (d *parsedDocs) htmlDoc(ctx context.Context) (*html.Node, string) {
	if !d.htmlDone {
		d.htmlDone = true
		d.html, d.htmlErr = parseHTML(ctx, d.body)
	}
	return d.html, d.htmlErr
}

// judgeHTML: the selector's matches; count is how many, the text subject
// the first match's text content with its whitespace collapsed, absent
// without a match.
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
	return d.work.text(a, strings.Join(strings.Fields(textContent(matches[0])), " "), true)
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
