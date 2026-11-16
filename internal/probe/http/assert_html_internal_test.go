package http

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/cascadia"
	"github.com/noixdev/pulsix/internal/probe/assert"
	"golang.org/x/net/html"
)

// htmlOf parses s.
func htmlOf(t *testing.T, s string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func selectorOf(t *testing.T, sel string) assert.Assertion {
	t.Helper()
	a, err := assert.Parse(`x=html("` + strings.ReplaceAll(sel, `"`, `\"`) + `") exists`)
	if err != nil {
		t.Fatalf("selector %q: %v", sel, err)
	}
	return a
}

// shopPage is a realistic page of about 800 KiB: a navigation list, a
// table of 2 000 rows and 2 500 product cards.
func shopPage() string {
	var r strings.Builder
	r.WriteString(`<!doctype html><html><head><title>Shop</title></head><body><header class="top"><nav class="main-nav"><ul>`)
	for i := range 50 {
		fmt.Fprintf(&r, `<li class="item"><a href="/c/%d" class="link">Category %d</a></li>`, i, i)
	}
	r.WriteString(`</ul></nav></header><main id="content"><div class="content"><table class="list"><tbody>`)
	for i := range 2000 {
		fmt.Fprintf(&r, `<tr class="row"><td class="name">Product %d</td><td class="price">%d.99</td><td><a href="/p/%d">view</a></td></tr>`, i, i, i)
	}
	r.WriteString(`</tbody></table>`)
	for i := range 2500 {
		fmt.Fprintf(&r, `<div class="card"><div class="body"><div class="inner"><h3 class="title">Product %d</h3><p class="desc">Lorem ipsum</p><span class="price">%d.99</span><button class="buy" data-id="%d" disabled>Buy</button></div></div></div>`, i, i, i)
	}
	r.WriteString(`</div></main></body></html>`)
	return r.String()
}

// TestSelectAllMatchesCascadia: on a realistic page, the walk finds what
// cascadia.QueryAll finds, in the same order, each well within an
// assertion's budget.
func TestSelectAllMatchesCascadia(t *testing.T) {
	doc := htmlOf(t, shopPage())
	// The most common shapes: descendant chains of tags and classes.
	ordinary := map[string]bool{"main div p": true, "nav ul li a": true, "table tbody tr td": true, ".content .price": true, "div.card .inner .price": true}
	left := int64(htmlCheckBudget)
	for _, sel := range []string{
		"main div p", "nav ul li a", "table tbody tr td", ".content .price", "div.card .inner .price",
		"tr:nth-child(odd) td", "li:first-child a", "h3 + p", "h3 ~ span", "#content td:last-child", "td:last-child a",
		"button:disabled", "td, h3", "p:not(.desc)", "*",
	} {
		a := selectorOf(t, sel)
		before := left
		got, problem := selectAll(context.Background(), doc, a, htmlSelectBudget, &left)
		charged := before - left
		if ordinary[sel] && charged > htmlSelectBudget/100 {
			t.Errorf("%q charged %d, over 1 %% of an assertion's budget", sel, charged)
		}
		want := cascadia.QueryAll(doc, a.Selector())
		if problem != "" || len(got) != len(want) {
			t.Fatalf("%q: selectAll = %d nodes, %q; cascadia found %d", sel, len(got), problem, len(want))
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%q: node %d differs", sel, i)
			}
		}
	}
}

// hostile is a page built to make one kind of match expensive, and the
// selector that would pay for it.
type hostile struct {
	name, page, sel string
}

// TestSelectAllStopsHostilePages: every walk cascadia's Match makes is
// charged before it runs, so a page built to make one costly stops with
// errHTMLTooMuch at the real budgets, long before a probe's deadline.
func TestSelectAllStopsHostilePages(t *testing.T) {
	var firstChild strings.Builder
	for range 20 {
		firstChild.WriteString(strings.Repeat("<!---->", 5000) + "<div>")
	}
	firstChild.WriteString(strings.Repeat("<a></a>", 5000))
	var comb strings.Builder
	for range 126 {
		comb.WriteString(strings.Repeat("<i></i>", 126) + "<div>")
	}
	comb.WriteString(strings.Repeat("<p></p>", 5000))
	chain := strings.Repeat("<div>", 500)
	pages := []hostile{
		// :first-child walks the comments before each ancestor's element.
		{"first-child over comments", firstChild.String(), "div:first-child a"},
		// + walks back over text and comments to the nearest element.
		{"adjacent over comments", "<b></b>" + strings.Repeat("<!---->", 100_000) + strings.Repeat("<a></a>", 1000), "b + a"},
		// ~ walks every earlier sibling.
		{"general sibling", strings.Repeat("<p></p>", 100_000), "h1 ~ p"},
		// nth-last-child walks every sibling.
		{"nth over siblings", strings.Repeat("<p></p>", 100_000), "p:nth-last-child(2)"},
		// A sibling pseudo on each ancestor of each node.
		{"descendant of last-child", comb.String(), "div:last-child p"},
		// Three descendant combinators over a deep chain.
		{"deep descendants", strings.Repeat(chain+strings.Repeat("</div>", 500), 20), "x div div div"},
		// A class read on an ancestor with 40 000 attributes.
		{"attribute-heavy ancestor", "<div " + manyAttributes(40_000) + ">" + strings.Repeat("<span>", 200) + strings.Repeat("<b></b>", 2000), ".x b"},
	}
	for _, h := range pages {
		t.Run(h.name, func(t *testing.T) {
			doc := htmlOf(t, h.page)
			left := int64(htmlCheckBudget)
			start := time.Now()
			_, problem := selectAll(context.Background(), doc, selectorOf(t, h.sel), htmlSelectBudget, &left)
			if problem != errHTMLTooMuch {
				t.Fatalf("%q = %q, want %q", h.sel, problem, errHTMLTooMuch)
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Fatalf("stopping took %v", d)
			}
		})
	}
}

// manyAttributes is n distinct attributes (the parser drops repeated
// names).
func manyAttributes(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "a%d ", i)
	}
	return b.String()
}

// TestSelectAllChargesEveryWalk: a walk of ancestors (:lang, :enabled) or
// siblings costs per node what a plain selector does not, so the same
// small budget that a plain selector fits stops them.
func TestSelectAllChargesEveryWalk(t *testing.T) {
	deep := htmlOf(t, strings.Repeat("<fieldset>", 400)+strings.Repeat("<input>", 50))
	for _, sel := range []string{"fieldset:lang(en)", "input:enabled", "input:disabled", "fieldset input", "fieldset:first-child input"} {
		left := int64(1 << 40)
		if _, problem := selectAll(context.Background(), deep, selectorOf(t, sel), 20_000, &left); problem != errHTMLTooMuch {
			t.Errorf("%q over a deep page = %q, want %q", sel, problem, errHTMLTooMuch)
		}
	}
	left := int64(1 << 40)
	if nodes, problem := selectAll(context.Background(), deep, selectorOf(t, "input"), 20_000, &left); problem != "" || len(nodes) != 50 {
		t.Fatalf("a plain selector over the same page = %d nodes, %q", len(nodes), problem)
	}
}

// TestSelectAllSharesTheCheckBudgetAndStopsAtTheDeadline: the check's
// budget bounds the selectors together, and the walk looks at ctx before
// every node.
func TestSelectAllSharesTheCheckBudgetAndStopsAtTheDeadline(t *testing.T) {
	doc := htmlOf(t, strings.Repeat("<p></p>", 400))
	left := int64(100)
	if _, problem := selectAll(context.Background(), doc, selectorOf(t, "p"), htmlSelectBudget, &left); problem != errHTMLTooMuch {
		t.Fatalf("past the check's budget = %q, want %q", problem, errHTMLTooMuch)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	left = htmlCheckBudget
	if _, problem := selectAll(ctx, doc, selectorOf(t, "p"), htmlSelectBudget, &left); problem != errOutOfTime {
		t.Fatalf("an ended probe = %q, want %q", problem, errOutOfTime)
	}
}
