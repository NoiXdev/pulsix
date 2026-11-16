package http

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
)

// render writes the children of n compactly: an element as its name,
// its attributes in brackets and its children in parentheses, a text
// node quoted.
func render(n *html.Node) string {
	var parts []string
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch c.Type {
		case html.TextNode:
			parts = append(parts, fmt.Sprintf("%q", c.Data))
		case html.ElementNode:
			var b strings.Builder
			b.WriteString(c.Data)
			for _, a := range c.Attr {
				fmt.Fprintf(&b, "[%s=%s]", a.Key, a.Val)
			}
			b.WriteString("(" + render(c) + ")")
			parts = append(parts, b.String())
		default:
			parts = append(parts, fmt.Sprintf("?%d", c.Type))
		}
	}
	return strings.Join(parts, " ")
}

// treeShape is the number of nodes under doc (doc excluded) and the most
// element ancestors any element has (at most the open elements, 512),
// walked with an explicit stack.
func treeShape(doc *html.Node) (nodes, depth int) {
	type at struct {
		n     *html.Node
		depth int
	}
	stack := []at{{doc, 0}}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur.n != doc {
			nodes++
		}
		if cur.n.Type == html.ElementNode {
			depth = max(depth, cur.depth-1)
		}
		for c := cur.n.FirstChild; c != nil; c = c.NextSibling {
			stack = append(stack, at{c, cur.depth + 1})
		}
	}
	return nodes, depth
}

// tokenCount is how many tokens the tokenizer reads from page.
func tokenCount(page []byte) int {
	z := html.NewTokenizer(bytes.NewReader(page))
	n := 0
	for z.Next() != html.ErrorToken {
		n++
	}
	return n
}

// TestBuildHTMLTree: the tree built from the tokens — nesting, void and
// self-closing elements, stray and early end tags, the implied closes,
// raw text as the tokenizer reads it, and nothing implied beyond them.
func TestBuildHTMLTree(t *testing.T) {
	cases := []struct{ page, want string }{
		{"", ""},
		{"x", `"x"`},
		{"<p>x</p>", `p("x")`},
		{"<div><span>a</span>b</div>", `div(span("a") "b")`},
		// Open at the end of the page: closed there.
		{"<div><span>a", `div(span("a"))`},
		// No html, head or body is implied.
		{"<title>t</title><h1>x</h1>", `title("t") h1("x")`},
		{"<!doctype html><html><body><p>x</body></html>", `html(body(p("x")))`},
		// Names lower-cased by the tokenizer, entities decoded.
		{`<DIV ID=A Class="b&amp;c">&lt;x&gt;</DiV>`, `div[id=A][class=b&c]("<x>")`},
		// Comments and doctypes dropped.
		{"<!doctype html><!-- c --><p>a<!-- d -->b", `p("a" "b")`},
		// Void and self-closing elements are not opened.
		{`<p>a<br>b<img src=x>c<input>d</p>`, `p("a" br() "b" img[src=x]() "c" input() "d")`},
		{"<div/>x<span/><i>y", `div() "x" span() i("y")`},
		{"<area><base><col><embed><link><meta><source><track><wbr>x", `area() base() col() embed() link() meta() source() track() wbr() "x"`},
		// A stray end tag is ignored; an end tag closes up to the nearest
		// open element of its name.
		{"<div>a</span>b</div>c", `div("a" "b") "c"`},
		{"<div><span>a</div>b", `div(span("a")) "b"`},
		{"<div><div>a</div>b</div>c", `div(div("a") "b") "c"`},
		{"<b><i>a</b>b</i>c", `b(i("a")) "b" "c"`},
		// A block-level start tag closes an open p.
		{"<p>a<p>b", `p("a") p("b")`},
		{"<p>a<div>b</div>c", `p("a") div("b") "c"`},
		{"<p><span>a<h2>b", `p(span("a")) h2("b")`},
		{"<p>a<table><tr><td>b", `p("a") table(tr(td("b")))`},
		{"<p>a<hr>b", `p("a") hr() "b"`},
		{"<div><p>a<blockquote>b</blockquote></div>", `div(p("a") blockquote("b"))`},
		// An inline start tag does not.
		{"<p>a<span>b</span><a>c", `p("a" span("b") a("c"))`},
		// li closes an open li, up to the nearest list.
		{"<ul><li>a<li>b</ul>", `ul(li("a") li("b"))`},
		{"<ul><li>a<ul><li>b<li>c</ul><li>d</ul>", `ul(li("a" ul(li("b") li("c"))) li("d"))`},
		{"<ol><li><p>a<li>b", `ol(li(p("a")) li("b"))`},
		// dt and dd close each other, up to the nearest dl.
		{"<dl><dt>a<dd>b<dt>c<dd>d</dl>", `dl(dt("a") dd("b") dt("c") dd("d"))`},
		{"<dl><dd><dl><dt>a</dl><dt>b</dl>", `dl(dd(dl(dt("a"))) dt("b"))`},
		// Table rows, cells and sections.
		{"<table><tr><td>a<td>b<tr><th>c<td>d</table>", `table(tr(td("a") td("b")) tr(th("c") td("d")))`},
		{"<table><thead><tr><th>h<tbody><tr><td>a<tfoot><tr><td>f</table>", `table(thead(tr(th("h"))) tbody(tr(td("a"))) tfoot(tr(td("f"))))`},
		{"<table><tr><td><table><tr><td>a</table>b<td>c</table>", `table(tr(td(table(tr(td("a"))) "b") td("c")))`},
		{"<table><tr><td><table><td>a<tr>b</table></table>", `table(tr(td(table(td("a") tr("b")))))`},
		// option closes an open option.
		{"<select><option>a<option>b</select>", `select(option("a") option("b"))`},
		{"<select><optgroup><option>a</optgroup><option>b</select>", `select(optgroup(option("a")) option("b"))`},
		// optgroup closes an open option or optgroup.
		{"<select><optgroup><option>a<optgroup><option>b</select>", `select(optgroup(option("a")) optgroup(option("b")))`},
		{"<select><option>a<optgroup><option>b</select>", `select(option("a") optgroup(option("b")))`},
		// body closes an open head (minifiers leave out </head>).
		{"<html><head><title>t</title><body><p>x", `html(head(title("t")) body(p("x")))`},
		{"<html><head><meta charset=utf-8><body>x", `html(head(meta[charset=utf-8]()) body("x"))`},
		// Raw text as the tokenizer gives it, wherever it is.
		{`<script>if (a<b) { x = "</p>"; }</script><p>y`, `script("if (a<b) { x = \"</p>\"; }") p("y")`},
		{"<style>p > a { }</style>x", `style("p > a { }") "x"`},
		{"<svg><style><p>x</style></svg>", `svg(style("<p>x"))`},
		{"<title>a &amp; <b></title>", `title("a & <b>")`},
		{"<textarea><p>x</textarea>", `textarea("<p>x")`},
		{"<plaintext><p>x</p>", `plaintext("<p>x</p>")`},
	}
	for _, tc := range cases {
		doc, problem := buildHTML(context.Background(), []byte(tc.page))
		if problem != "" {
			t.Errorf("%q: %q", tc.page, problem)
			continue
		}
		if got := render(doc); got != tc.want {
			t.Errorf("%q\n got %s\nwant %s", tc.page, got, tc.want)
		}
	}
}

// TestBuildHTMLDepthCap: start tags past 512 open elements become
// children of the 512th, which keeps them and the text after them.
func TestBuildHTMLDepthCap(t *testing.T) {
	doc, problem := buildHTML(context.Background(), []byte(strings.Repeat("<div>", 600)+"x"+strings.Repeat("</div>", 600)+"y"))
	if problem != "" {
		t.Fatal(problem)
	}
	nodes, depth := treeShape(doc)
	if nodes != 602 || depth != htmlMaxDepth {
		t.Fatalf("nodes %d, depth %d; want 602, %d", nodes, depth, htmlMaxDepth)
	}
	deepest := doc
	for range htmlMaxDepth {
		deepest = deepest.FirstChild
	}
	if got := render(deepest); got != strings.Repeat("div() ", 88)+`"x"` {
		t.Fatalf("the 512th element holds %.80s…", got)
	}
	if doc.LastChild.Type != html.TextNode || doc.LastChild.Data != "y" {
		t.Fatalf("the end tags did not close the chain: %v", doc.LastChild)
	}
}

// TestBuildHTMLNodeCap: past the cap the build stops with
// errHTMLTooComplex; up to it the tree is built.
func TestBuildHTMLNodeCap(t *testing.T) {
	page := []byte(strings.Repeat("<i>x</i>", 5))
	if doc, problem := buildTree(context.Background(), page, 10); problem != "" || doc == nil {
		t.Fatalf("10 nodes under a cap of 10 = %q", problem)
	}
	if doc, problem := buildTree(context.Background(), page, 9); problem != errHTMLTooComplex || doc != nil {
		t.Fatalf("10 nodes under a cap of 9 = %v, %q", doc, problem)
	}
	// Under the kept MiB, only pages of almost nothing but tags and
	// single characters reach the real cap.
	dense := []byte(strings.Repeat("<i>x", 210_000))
	if _, problem := buildHTML(context.Background(), dense); problem != errHTMLTooComplex {
		t.Fatalf("420 000 nodes = %q, want %q", problem, errHTMLTooComplex)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, problem := buildHTML(ctx, []byte(shopPage())); problem != errOutOfTime {
		t.Fatalf("an ended probe = %q, want %q", problem, errOutOfTime)
	}
}

// openBold is a page that leaves open formatting elements over many
// paragraphs, after prefix: a browser's parser clones every one of them
// into each paragraph (the reviews' repros: 450 and 60 000 make 244 KB
// into 27 M nodes and 4 GB there).
func openBold(prefix, between string, open, paragraphs int) string {
	var b strings.Builder
	b.WriteString(prefix + "<p>")
	for i := range open {
		fmt.Fprintf(&b, "<b a=%d>", i)
	}
	b.WriteString(between)
	b.WriteString(strings.Repeat("<p>x", paragraphs))
	return b.String()
}

// hostilePages are the reviews' repros and pages built to make the
// builder's work grow faster than the page, from 244 KB to the kept MiB
// (the larger open-bold pages stay under the node cap).
func hostilePages() map[string]string {
	marker := "</p><table><td>" + strings.Repeat("</b>", 450) + "</table>"
	deepSpans := "<ul><li>" + strings.Repeat("<span>", 520)
	return map[string]string{
		"open bold":            openBold("", "", 450, 60_000),
		"open bold, td marker": openBold("", marker, 450, 60_000),
		"svg style":            openBold("<svg><style>", "", 450, 60_000),
		"svg title":            openBold("<svg><title>", "", 450, 60_000),
		"math svg title":       openBold("<math><mi><svg><title>", "", 450, 60_000),
		"open bold, 760 KB":    openBold("", "", 450, 190_000),
		"td marker, 760 KB":    openBold("", marker, 450, 190_000),
		"deep nesting":         strings.Repeat(strings.Repeat("<div>", 500)+strings.Repeat("</div>", 500), 95),
		"stray end tags deep":  strings.Repeat("<div>", 520) + strings.Repeat("</x>", 250_000),
		"li under a deep list": deepSpans + strings.Repeat("<li>", 250_000),
		"p closes, deep":       strings.Repeat("<span>", 520) + strings.Repeat("<p>", 300_000),
		"attributes":           strings.Repeat("<i "+manyAttributes(40)+">", 6_000),
		"short attributes":     strings.Repeat("<i"+strings.Repeat(" a", 30)+">", 15_000),
		"distinct names":       manyNames(50_000),
	}
}

// manyNames is n elements of distinct names, each closed at once.
func manyNames(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "<x%d>y</x%d>", i, i)
	}
	return b.String()
}

// TestBuildHTMLIsLinear: every hostile page builds into at most one node
// per token, at most 512 deep, allocating at most 100 bytes per byte of
// page (measured: at most 57) and in under two seconds (measured: at most
// 90 ms, 610 ms under -race).
func TestBuildHTMLIsLinear(t *testing.T) {
	for name, page := range hostilePages() {
		t.Run(name, func(t *testing.T) {
			body := []byte(page)
			if len(body) > httpBodyLimit {
				t.Fatalf("the page is %d bytes, past the kept MiB", len(body))
			}
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			start := time.Now()
			doc, problem := buildHTML(context.Background(), body)
			d := time.Since(start)
			runtime.ReadMemStats(&after)
			if problem != "" {
				t.Fatalf("%q", problem)
			}
			nodes, depth := treeShape(doc)
			if tokens := tokenCount(body); nodes > tokens || depth > htmlMaxDepth {
				t.Errorf("%d nodes from %d tokens, depth %d", nodes, tokens, depth)
			}
			alloc := after.TotalAlloc - before.TotalAlloc
			if alloc > 100*uint64(len(body)) || d > 2*time.Second {
				t.Errorf("%d bytes built in %v, %d MB allocated", len(body), d, alloc>>20)
			}
			t.Logf("%d bytes: %d nodes, depth %d, %v, %d MB", len(body), nodes, depth, d, alloc>>20)
		})
	}
}

// FuzzBuildTree: on any input the tree has at most one node per token
// besides the document, and no element deeper than 512.
func FuzzBuildTree(f *testing.F) {
	for _, page := range []string{
		"", "<p>x", "<ul><li>a<li>b</ul>", "<table><tr><td>a<td>b</table>", "<svg><style><p>x</style>",
		"<script>a</p>b</script>", "<div/><br><b><i>x</b></i>", strings.Repeat("<div>", 600) + "x",
		openBold("", "", 20, 40),
		openBold("", "</p><table><td>"+strings.Repeat("</b>", 20)+"</table>", 20, 40),
		openBold("<svg><style>", "", 20, 40),
		openBold("<math><mi><svg><title>", "", 20, 40),
		openBold("", "", 450, 60_000),
	} {
		f.Add([]byte(page))
	}
	f.Fuzz(func(t *testing.T, page []byte) {
		doc, problem := buildHTML(context.Background(), page)
		if problem != "" {
			// Under the kept MiB the only refusal is the node cap.
			if problem != errHTMLTooComplex || tokenCount(page) <= htmlMaxNodes {
				t.Fatalf("%q", problem)
			}
			return
		}
		nodes, depth := treeShape(doc)
		if tokens := tokenCount(page); nodes+1 > tokens+1 || depth > htmlMaxDepth {
			t.Fatalf("%d nodes from %d tokens, depth %d", nodes+1, tokens, depth)
		}
	})
}
