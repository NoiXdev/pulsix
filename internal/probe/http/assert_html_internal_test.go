package http

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe/assert"
	"golang.org/x/net/html"
)

// htmlOf builds the tree of s as the probe does.
func htmlOf(t *testing.T, s string) *html.Node {
	t.Helper()
	doc, problem := buildHTML(context.Background(), []byte(s))
	if problem != "" {
		t.Fatal(problem)
	}
	return doc
}

func selectorOf(t *testing.T, sel string) *assert.Selector {
	t.Helper()
	s, err := assert.ParseSelector(sel)
	if err != nil {
		t.Fatalf("selector %q: %v", sel, err)
	}
	return s
}

// ids is the id attribute of each node.
func ids(nodes []*html.Node) []string {
	out := []string{}
	for _, n := range nodes {
		for _, a := range n.Attr {
			if a.Key == "id" {
				out = append(out, a.Val)
			}
		}
	}
	return out
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

// distinctSiblings is n sibling elements of n distinct names.
func distinctSiblings(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "<t%d></t%d>", i, i)
	}
	return b.String()
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

// TestSelectAllExpectations: what each selector selects on small pages,
// written out by hand, in document order — combinator mixes a greedy
// matcher gets wrong (a child step under a descendant one, nested
// elements of the same name), and every simple selector and pseudo-class.
func TestSelectAllExpectations(t *testing.T) {
	// The pages name html and body: the builder implies neither.
	nested := htmlOf(t, `<body><aa id=1><bb id=2><bb id=3><cc id=4></cc></bb></bb></aa>`+
		`<bb id=5><cc id=6></cc></bb><aa id=7><zz id=8><bb id=9><cc id=10></cc></bb></zz></aa></body>`)
	list := htmlOf(t, `<html><ul id=u>text<li id=l1 class="a b"></li><!-- c --><li id=l2 class=b></li>`+
		`<li id=l3 class=" a  c " title="x y" data-k="pre-mid-suf"></li></ul></html>`)
	svg := htmlOf(t, `<svg id=root viewBox="0 0 1 1"><linearGradient id=g></linearGradient>`+
		`<a id=s xlink:href="/x"></a><a id=t href="/y"></a></svg>`)
	cases := []struct {
		doc  *html.Node
		sel  string
		want []string
	}{
		// SVG keeps its camelCase names; xlink:href is namespaced.
		{svg, "lineargradient", []string{"g"}},
		{svg, "linearGradient", []string{"g"}},
		{svg, "svg > LINEARGRADIENT", []string{"g"}},
		{svg, "[viewbox]", []string{"root"}},
		{svg, `[viewBox^="0 0"]`, []string{"root"}},
		{svg, "[href]", []string{"t"}},
		{svg, "svg a", []string{"s", "t"}},
		{nested, "aa > bb cc", []string{"4"}},
		{nested, "aa bb > cc", []string{"4", "10"}},
		{nested, "aa bb cc", []string{"4", "10"}},
		{nested, "bb bb cc", []string{"4"}},
		{nested, "aa > bb > bb > cc", []string{"4"}},
		{nested, "aa > bb > cc", []string{}},
		{nested, "zz > bb", []string{"9"}},
		{nested, "bb", []string{"2", "3", "5", "9"}},
		{nested, "aa, cc", []string{"1", "4", "6", "7", "10"}},
		{nested, "cc, aa", []string{"1", "4", "6", "7", "10"}},
		{nested, "aa > * > bb cc", []string{"4", "10"}},
		{nested, "body > aa", []string{"1", "7"}},
		{list, "li:first-child", []string{"l1"}},
		{list, "li:last-child", []string{"l3"}},
		{list, "li:nth-child(2)", []string{"l2"}},
		{list, "li:nth-child(4)", []string{}},
		{list, "li:nth-child(odd)", []string{"l1", "l3"}},
		{list, "li:nth-child(even)", []string{"l2"}},
		{list, "ul:first-child:last-child", []string{"u"}},
		{list, ".a", []string{"l1", "l3"}},
		{list, ".a.b", []string{"l1"}},
		{list, "li.c", []string{"l3"}},
		{list, ".A", []string{}},
		{list, "[title]", []string{"l3"}},
		{list, `[title="x y"]`, []string{"l3"}},
		{list, "[title=x]", []string{}},
		{list, "[title~=y]", []string{"l3"}},
		{list, `[title~="x y"]`, []string{}},
		{list, "[data-k^=pre]", []string{"l3"}},
		{list, "[data-k$=suf]", []string{"l3"}},
		{list, `[data-k*="-mid-"]`, []string{"l3"}},
		{list, `[data-k^=""]`, []string{}},
		{list, "#l2", []string{"l2"}},
		{list, "#L2", []string{}},
		{list, "ul > #l2.b:nth-child(2)", []string{"l2"}},
		{list, "LI[TITLE]", []string{"l3"}},
		{list, "html li", []string{"l1", "l2", "l3"}},
		// The An+B forms and pseudo-classes of backlog 3.90.
		{list, "li:nth-child(-n+2)", []string{"l1", "l2"}},
		{list, "li:nth-child(n)", []string{"l1", "l2", "l3"}},
		{list, "li:nth-child(0)", []string{}},
		{list, "li:nth-child(-1)", []string{}},
		{list, "li:nth-child(2n+3)", []string{"l3"}},
		{list, "li:nth-child(-2n+3)", []string{"l1", "l3"}},
		{list, "li:nth-last-child(odd)", []string{"l1", "l3"}},
		{list, "li:nth-last-child(1)", []string{"l3"}},
		{list, "li:only-child", []string{}},
		{list, "ul:only-child", []string{"u"}},
		{list, "li:not(.a)", []string{"l2"}},
		{list, "li:not(.a.c)", []string{"l1", "l2"}},
		{list, "li:not(.c, :first-child)", []string{"l2"}},
		{list, "li:not([title])", []string{"l1", "l2"}},
		{list, "li:not(li)", []string{}},
		{list, "li:not(*)", []string{}},
		{list, "#u > :not(li)", []string{}},
	}
	for _, tc := range cases {
		left := int64(htmlCheckBudget)
		got, problem := selectAll(context.Background(), tc.doc, selectorOf(t, tc.sel), htmlSelectBudget, &left)
		if problem != "" || !slices.Equal(ids(got), tc.want) {
			t.Errorf("%q = %v, %q; want %v", tc.sel, ids(got), problem, tc.want)
		}
	}
}

// TestSelectAllSiblings: + takes the next element sibling only, ~ every
// later one; text between siblings neither takes nor breaks a state (the
// probe's tree keeps no comments); a sibling state never reaches a
// sibling's descendants, another parent's children or an earlier
// sibling.
func TestSelectAllSiblings(t *testing.T) {
	doc := htmlOf(t, `<body><h1 id=h></h1> text <p id=p1></p><!-- c --><p id=p2><span id=s1></span>`+
		`<span id=s2></span></p><div id=d><p id=p3></p></div><p id=p4></p>`+
		`<ul id=u><li id=l1></li><li id=l2><a id=a2></a></li><li id=l3><a id=a3></a></li></ul></body>`)
	cases := []struct {
		sel  string
		want []string
	}{
		{"h1 + p", []string{"p1"}},
		{"h1 ~ p", []string{"p1", "p2", "p4"}},
		{"p + p", []string{"p2"}},
		{"p ~ p", []string{"p2", "p4"}},
		{"h1 + p + p", []string{"p2"}},
		{"h1 + p ~ p", []string{"p2", "p4"}},
		{"h1 + p2", []string{}},
		{"h1 ~ div > p", []string{"p3"}},
		{"h1 ~ div p", []string{"p3"}},
		{"div + p", []string{"p4"}},
		{"div ~ h1", []string{}},
		{"p + h1", []string{}},
		{"span + span", []string{"s2"}},
		{"span ~ span", []string{"s2"}},
		{"h1 ~ span", []string{}},
		{"p + span", []string{}},
		{"p span + span", []string{"s2"}},
		{"p > span ~ span", []string{"s2"}},
		{"h1 ~ p > span + span", []string{"s2"}},
		{"h1 + span", []string{}},
		{"ul > li + li a", []string{"a2", "a3"}},
		{"ul > li + li > a", []string{"a2", "a3"}},
		{"li + li + li", []string{"l3"}},
		{"li ~ li > a", []string{"a2", "a3"}},
		{"li:first-child ~ li", []string{"l2", "l3"}},
		{"li:first-child + li a", []string{"a2"}},
		{"div ~ ul li", []string{"l1", "l2", "l3"}},
		{"body > h1 ~ ul > li:last-child > a", []string{"a3"}},
		{"h1 + p, li + li", []string{"p1", "l2", "l3"}},
		{"li + li, h1 + p", []string{"p1", "l2", "l3"}},
		{"* + *", []string{"p1", "p2", "s2", "d", "p4", "u", "l2", "l3"}},
		{"* ~ :not(p)", []string{"s2", "d", "u", "l2", "l3"}},
	}
	for _, tc := range cases {
		left := int64(htmlCheckBudget)
		got, problem := selectAll(context.Background(), doc, selectorOf(t, tc.sel), htmlSelectBudget, &left)
		if problem != "" || !slices.Equal(ids(got), tc.want) {
			t.Errorf("%q = %v, %q; want %v", tc.sel, ids(got), problem, tc.want)
		}
	}
}

// TestSelectAllPositionalPseudos: the of-type, :empty and :root
// pseudo-classes of backlog 3.90. The root's parent is the document; an
// element with only whitespace text is not empty.
func TestSelectAllPositionalPseudos(t *testing.T) {
	doc := htmlOf(t, `<html id=r><body id=b><div id=x1><p id=q1></p><span id=t1></span><p id=q2></p><p id=q3>x</p>`+
		`<span id=t2></span></div><div id=x2><em id=e1></em></div><div id=x3> </div><div id=x4><!-- c --></div></body></html>`)
	fragment := htmlOf(t, `<p id=f1></p><div id=f2><p id=f3></p></div>`)
	cases := []struct {
		doc  *html.Node
		sel  string
		want []string
	}{
		{doc, ":root", []string{"r"}},
		{doc, "body:root", []string{}},
		{doc, ":root > body", []string{"b"}},
		{fragment, ":root", []string{"f1", "f2"}},
		{doc, "p:first-of-type", []string{"q1"}},
		{doc, "p:last-of-type", []string{"q3"}},
		{doc, "span:first-of-type", []string{"t1"}},
		{doc, "span:last-of-type", []string{"t2"}},
		{doc, ":only-of-type", []string{"r", "b", "e1"}},
		{doc, ":only-child", []string{"r", "b", "e1"}},
		{doc, "p:nth-of-type(2)", []string{"q2"}},
		{doc, ":nth-of-type(2)", []string{"q2", "t2", "x2"}},
		{doc, "p:nth-of-type(odd)", []string{"q1", "q3"}},
		{doc, "p:nth-last-of-type(1)", []string{"q3"}},
		{doc, "#x1 > :nth-last-of-type(2)", []string{"t1", "q2"}},
		{doc, "#x1 > :nth-last-child(2)", []string{"q3"}},
		{doc, "div:nth-last-of-type(-n+2)", []string{"x3", "x4"}},
		{doc, "div:empty", []string{"x4"}},
		{doc, ":empty", []string{"q1", "t1", "q2", "t2", "e1", "x4"}},
		{doc, "p:not(:first-of-type)", []string{"q2", "q3"}},
		{doc, "#x1 > :not(p)", []string{"t1", "t2"}},
		{doc, "#x1 > *:not([id^=q], span)", []string{}},
		{doc, "#x1 > :not(:empty)", []string{"q3"}},
		{doc, "p:first-of-type + span + p", []string{"q2"}},
		{doc, "span:first-of-type ~ p:last-of-type", []string{"q3"}},
	}
	for _, tc := range cases {
		left := int64(htmlCheckBudget)
		got, problem := selectAll(context.Background(), tc.doc, selectorOf(t, tc.sel), htmlSelectBudget, &left)
		if problem != "" || !slices.Equal(ids(got), tc.want) {
			t.Errorf("%q = %v, %q; want %v", tc.sel, ids(got), problem, tc.want)
		}
	}
}

// TestSelectAllOnARealPage: ordinary selectors on a realistic page select
// what the page holds, each charging a small part of the budget.
func TestSelectAllOnARealPage(t *testing.T) {
	doc := htmlOf(t, shopPage())
	for _, tc := range []struct {
		sel  string
		want int
	}{
		{"main div p", 2500}, {"nav ul li a", 50}, {"table tbody tr td", 6000}, {".content .price", 4500},
		{"div.card .inner .price", 2500}, {"div.card > .inner", 0}, {"tr:nth-child(odd) td", 3000},
		{"ul > li:first-child a", 1}, {"td:last-child a", 2000}, {"#content td", 6000},
		{"button[disabled]", 2500}, {"td, h3", 8500}, {"[href^='/p/']", 2000},
		{"tr + tr", 1999}, {"tr ~ tr", 1999}, {"h3 + p", 2500}, {"table ~ .card", 2500}, {".card ~ .card", 2499},
		{"td:nth-last-child(2)", 2000}, {"div:empty", 0}, {"span:first-of-type", 2500},
		{"tr:not(:first-child) td.price", 1999}, {"li:nth-of-type(2n+1) a", 25}, {".card:nth-of-type(1)", 1},
	} {
		left := int64(htmlCheckBudget)
		got, problem := selectAll(context.Background(), doc, selectorOf(t, tc.sel), htmlSelectBudget, &left)
		if problem != "" || len(got) != tc.want {
			t.Errorf("%q = %d nodes, %q; want %d", tc.sel, len(got), problem, tc.want)
		}
		if charged := htmlCheckBudget - left; charged > htmlSelectBudget/10 {
			t.Errorf("%q charged %d, over a tenth of an assertion's budget", tc.sel, charged)
		}
	}
}

// TestSelectAllBoundsHostilePages: pages built to make matching costly —
// deep nesting under many compounds, many siblings, a huge class list
// read by repeated classes, a huge attribute compared again and again,
// tens of thousands of attributes, many comma parts — finish or stop
// with errHTMLTooMuch quickly at the real budgets: the walk is linear.
func TestSelectAllBoundsHostilePages(t *testing.T) {
	chain := func(depth, n int) string {
		return strings.Repeat(strings.Repeat("<div>", depth)+strings.Repeat("</div>", depth), n)
	}
	huge := `<div title="` + strings.Repeat("ab", 300_000) + `"></div>`
	for _, h := range []struct {
		name, page, sel string
		tooMuch         bool
	}{
		{"deep nesting", chain(500, 190), "x div div div div div div p", false},
		{"many siblings", strings.Repeat("<p></p>", 150_000), strings.TrimSuffix(strings.Repeat("p:nth-child(2) b,", 14), ","), false},
		{"huge class list", `<div class="` + strings.Repeat("a ", 450_000) + `">` + strings.Repeat("<i></i>", 2000), ".x" + strings.Repeat(".a", 15) + " i", false},
		{"many attributes", `<div ` + manyAttributes(60_000) + `>` + strings.Repeat("<i></i>", 2000), strings.Repeat("[zz]", 16) + " i", false},
		{"many parts", chain(500, 190), strings.TrimSuffix(strings.Repeat("a b c d e f g h,", 15), ","), false},
		// Sibling chains (backlog 3.90): a ~ state reaches every later
		// sibling, so on 150 000 siblings each part keeps all its
		// compounds open — linear still, past the budget with 15 parts.
		{"long sibling chains", strings.Repeat("<p></p>", 150_000), "p~p~p~p~p~p~p~p,p+p+p+p+p+p+p+p", false},
		{"many sibling chain parts", strings.Repeat("<p></p>", 150_000), strings.TrimSuffix(strings.Repeat("p~p~p~p~p~p~p~p,", 15), ","), true},
		{"sibling chains over deep nesting", chain(500, 190), strings.TrimSuffix(strings.Repeat("div~div+div~div div+div~div div,", 7), ","), false},
		{"distinct sibling names counted by type", distinctSiblings(60_000), strings.TrimSuffix(strings.Repeat(":nth-last-of-type(2) b,", 10), ","), false},
		// Four parts (the 256 bytes hold no more) of a :not at the cap of
		// 16 simple selectors: 15 class tests per element and part.
		{"nots at the simple cap", chain(500, 190), strings.TrimSuffix(strings.Repeat(":not(.a,.b,.c,.d,.e,.f,.g,.h,.i,.j,.k,.l,.m,.n,.o) p,", 4), ","), false},
		// Each of the 15 compounds inside the :not reads the 600 KB
		// attribute and fails: 9 M steps per element.
		{"nots reading a huge attribute", huge + huge, ":not(" + strings.TrimSuffix(strings.Repeat(`[title*="zz"],`, 15), ",") + ")", true},
		// 16 substring tests read a 600 KB attribute 16 times: 9.6 M
		// steps per element, two elements past the assertion's budget.
		{"huge attribute read again and again", huge + huge, strings.Repeat(`[title*="ab"]`, 16), true},
	} {
		t.Run(h.name, func(t *testing.T) {
			doc := htmlOf(t, h.page)
			left := int64(htmlCheckBudget)
			start := time.Now()
			_, problem := selectAll(context.Background(), doc, selectorOf(t, h.sel), htmlSelectBudget, &left)
			if want := map[bool]string{true: errHTMLTooMuch, false: ""}[h.tooMuch]; problem != want {
				t.Fatalf("%q = %q, want %q", h.sel, problem, want)
			}
			// The bound catches work that grows with the square of the page,
			// which costs minutes here. It is no benchmark: CI under -race
			// and -cover, beside other packages, measured 5.6 s.
			if d := time.Since(start); d > 60*time.Second {
				t.Fatalf("the walk took %v", d)
			}
		})
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

// TestHTMLTextIsChargedToTheCheckBudget: reading the first match's text
// (one step per node below it and one per byte of its text) comes out of
// the check's html budget after the selector's walk, so a text assertion
// on a large element is bounded by the same budget.
func TestHTMLTextIsChargedToTheCheckBudget(t *testing.T) {
	page := "<div>" + strings.Repeat("<p>ab  c</p>", 100) + "</div>"
	a, err := assert.Parse(`t=html("div") contains "zz"`)
	if err != nil {
		t.Fatal(err)
	}
	walk := int64(htmlCheckBudget)
	if _, problem := selectAll(context.Background(), htmlOf(t, page), a.Selector(), htmlSelectBudget, &walk); problem != "" {
		t.Fatal(problem)
	}
	selected := htmlCheckBudget - walk
	// The div, 100 p and 100 text nodes; 100 texts of 5 bytes.
	const text = 201 + 500

	d := parsedDocs{body: []byte(page), htmlLeft: htmlCheckBudget, work: &regexWork{left: regexCheckBudget}}
	if v := d.judgeHTML(context.Background(), a); v.err != "" || v.holds {
		t.Fatalf("verdict = %+v, want a judged failure", v)
	}
	if used := htmlCheckBudget - d.htmlLeft; used != selected+text {
		t.Fatalf("the assertion used %d, want the walk's %d and the text's %d", used, selected, text)
	}

	for _, left := range []int64{selected, selected + 201, selected + text - 1} {
		d = parsedDocs{body: []byte(page), htmlLeft: left, work: &regexWork{left: regexCheckBudget}}
		if v := d.judgeHTML(context.Background(), a); v.err != errHTMLTooMuch {
			t.Fatalf("with %d left: verdict = %+v, want %q", left, v, errHTMLTooMuch)
		}
	}
	d = parsedDocs{body: []byte(page), htmlLeft: selected + text, work: &regexWork{left: regexCheckBudget}}
	if v := d.judgeHTML(context.Background(), a); v.err != "" {
		t.Fatalf("with exactly enough: verdict = %+v, want it judged", v)
	}
}
