package http

import (
	"bytes"
	"context"
	"slices"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// errHTMLTooComplex is every html assertion's error when the page holds
// more nodes than the builder keeps (htmlMaxNodes).
const errHTMLTooComplex = "the page is too complex to parse"

const (
	// htmlMaxDepth bounds the open elements: past it a start tag becomes
	// a child of the 512th, not opened, so no element has more ancestors.
	htmlMaxDepth = 512
	// htmlMaxNodes bounds the elements and text nodes built. A node takes
	// one token, so the kept MiB holds at most about 520 000 of them (a
	// tag and one character each); a real 800 KiB page holds tens of
	// thousands. Measured, a build allocates at most about 60 bytes per
	// byte of page (380 000 nodes from 760 KB: 43 MB in 70 ms).
	htmlMaxNodes = 400_000
)

// voidElements have no content and no end tag; they are never opened.
var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true,
	"input": true, "link": true, "meta": true, "source": true, "track": true, "wbr": true,
}

// impliedClose is what a start tag closes before it opens: the
// outermost open element named in closes that was opened after the
// innermost one named in within (and everything opened after it).
type impliedClose struct {
	closes, within []string
}

// blockElements close an open p, which may not hold them; an open one of
// them (but p) bounds that.
var blockElements = []string{
	"address", "article", "aside", "blockquote", "div", "dl", "fieldset", "figure", "footer", "form",
	"h1", "h2", "h3", "h4", "h5", "h6", "header", "hr", "main", "nav", "ol", "p", "pre", "section",
	"table", "ul",
}

// impliedCloses are the common end tags HTML lets a page leave out, by
// the start tag that implies them. It is deliberately small: a page that
// leaves out others groups differently than in a browser.
var impliedCloses = func() map[string]impliedClose {
	cells := []string{"tr", "td", "th"}
	sections := []string{"thead", "tbody", "tfoot"}
	m := map[string]impliedClose{
		"li":     {closes: []string{"li"}, within: []string{"ul", "ol"}},
		"dt":     {closes: []string{"dt", "dd"}, within: []string{"dl"}},
		"dd":     {closes: []string{"dt", "dd"}, within: []string{"dl"}},
		"tr":     {closes: cells, within: append([]string{"table"}, sections...)},
		"td":     {closes: []string{"td", "th"}, within: []string{"tr", "table"}},
		"th":     {closes: []string{"td", "th"}, within: []string{"tr", "table"}},
		"option": {closes: []string{"option"}, within: []string{"select", "datalist", "optgroup"}},
	}
	for _, s := range sections {
		m[s] = impliedClose{closes: append(append([]string{}, sections...), cells...), within: []string{"table"}}
	}
	containers := slices.DeleteFunc(slices.Clone(blockElements), func(name string) bool { return name == "p" })
	for _, b := range blockElements {
		m[b] = impliedClose{closes: []string{"p"}, within: containers}
	}
	return m
}()

// buildHTML builds body's tree (buildTree) under htmlMaxNodes.
func buildHTML(ctx context.Context, body []byte) (*html.Node, string) {
	return buildTree(ctx, body, htmlMaxNodes)
}

// buildTree builds the tree of body in one pass over the tokens of
// golang.org/x/net/html's tokenizer — not the HTML parsing algorithm,
// whose tree builder clones open formatting elements at every insertion
// and so can build millions of nodes from a small page, in one call that
// cannot be interrupted. Here each token makes at most one node:
//   - a start tag an element, a child of the current one, opened unless
//     it is void, self-closing or past htmlMaxDepth; before it, the end
//     tags it implies (impliedCloses) close what they name;
//   - text a text node, raw text (script, style, title, textarea …)
//     included as the tokenizer reads it, wherever it is;
//   - an end tag closes the nearest open element of its name and
//     everything opened after it, or nothing when none is open;
//   - comments and doctypes nothing.
//
// No html, head or body is implied, and malformed markup can group
// differently than in a browser. Each token takes bounded work: the open
// elements are indexed by name, so an end tag or an implied close looks
// up a few names instead of walking up to 512 open elements. Past maxNodes the build stops with
// errHTMLTooComplex, and with errOutOfTime once ctx ends (looked at
// before every token).
func buildTree(ctx context.Context, body []byte, maxNodes int) (*html.Node, string) {
	b := treeBuilder{
		open:  []*html.Node{{Type: html.DocumentNode}},
		index: map[string][]int{},
	}
	z := html.NewTokenizer(bytes.NewReader(body))
	for {
		if ctx.Err() != nil {
			return nil, errOutOfTime
		}
		switch z.Next() {
		case html.ErrorToken:
			// io.EOF: a bytes.Reader has no other error.
			return b.open[0], ""
		case html.TextToken:
			b.append(&html.Node{Type: html.TextNode, Data: string(z.Text())}, false)
		case html.StartTagToken:
			n := b.element(z)
			b.append(n, !voidElements[n.Data])
		case html.SelfClosingTagToken:
			b.append(b.element(z), false)
		case html.EndTagToken:
			name, _ := z.TagName()
			if at := b.index[string(name)]; len(at) > 0 {
				b.closeTo(at[len(at)-1])
			}
		}
		if b.nodes > maxNodes {
			return nil, errHTMLTooComplex
		}
	}
}

// treeBuilder is a build in progress: the open elements, the document
// first, and per name the positions in open of the elements of that
// name, ascending.
type treeBuilder struct {
	open  []*html.Node
	index map[string][]int
	nodes int
}

// element is the element of the start tag z is on, after the end tags
// that tag implies are closed.
func (b *treeBuilder) element(z *html.Tokenizer) *html.Node {
	name, more := z.TagName()
	if rule, ok := impliedCloses[string(name)]; ok {
		if target := b.outermostAfter(rule.closes, b.nearest(rule.within)); target > 0 {
			b.closeTo(target)
		}
	}
	n := &html.Node{Type: html.ElementNode}
	n.DataAtom, n.Data = intern(name)
	for more {
		var key, val []byte
		key, val, more = z.TagAttr()
		_, k := intern(key)
		n.Attr = append(n.Attr, html.Attribute{Key: k, Val: string(val)})
	}
	return n
}

// append adds n as the last child of the current element, and opens it
// when open is set and the open elements are not at htmlMaxDepth.
func (b *treeBuilder) append(n *html.Node, open bool) {
	b.open[len(b.open)-1].AppendChild(n)
	b.nodes++
	if open && len(b.open) <= htmlMaxDepth {
		b.index[n.Data] = append(b.index[n.Data], len(b.open))
		b.open = append(b.open, n)
	}
}

// nearest is the position of the innermost open element named in names,
// or -1.
func (b *treeBuilder) nearest(names []string) int {
	pos := -1
	for _, name := range names {
		if at := b.index[name]; len(at) > 0 {
			pos = max(pos, at[len(at)-1])
		}
	}
	return pos
}

// outermostAfter is the position of the outermost open element named in
// names that was opened after position floor, or -1.
func (b *treeBuilder) outermostAfter(names []string, floor int) int {
	pos := -1
	for _, name := range names {
		at := b.index[name]
		if i, _ := slices.BinarySearch(at, floor+1); i < len(at) && (pos < 0 || at[i] < pos) {
			pos = at[i]
		}
	}
	return pos
}

// closeTo closes the open element at pos (never the document) and every
// one opened after it.
func (b *treeBuilder) closeTo(pos int) {
	for len(b.open) > pos {
		name := b.open[len(b.open)-1].Data
		if at := b.index[name]; len(at) > 1 {
			b.index[name] = at[:len(at)-1]
		} else {
			delete(b.index, name)
		}
		b.open = b.open[:len(b.open)-1]
	}
}

// intern is name's atom and a string of it, shared for names HTML
// knows so that common tags and attributes allocate nothing.
func intern(name []byte) (atom.Atom, string) {
	if a := atom.Lookup(name); a != 0 {
		return a, a.String()
	}
	return 0, string(name)
}
