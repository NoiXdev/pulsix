package http

import (
	"bytes"
	"context"

	"golang.org/x/net/html"
)

// errHTMLTooComplex is every html assertion's error when the page would
// make the HTML parser build far more nodes than it has bytes.
const errHTMLTooComplex = "the page is too complex to parse"

// htmlParseCap bounds parseCost. Measured: the parse took at most about
// 19 ns and 50 bytes per unit (450 bold tags left open over 2 000
// paragraphs: 4.3 M units, 51 ms, 137 MB; 500-deep nesting: 6.3 M units,
// 122 ms, 6 MB), so a page under the cap parses in about 80 ms and
// 200 MB at worst. A real 800 KiB page (20 000 elements) costs 190 000.
const htmlParseCap = 4_000_000

// formattingElements are the HTML parsing spec's formatting elements:
// while one is open, the tree builder clones it, attributes and all, at
// every text or element it inserts ("reconstruct the active formatting
// elements"), so a few of them left open over many paragraphs make a
// small page into a tree of millions of nodes.
var formattingElements = map[string]bool{
	"a": true, "b": true, "big": true, "code": true, "em": true, "font": true, "i": true,
	"nobr": true, "s": true, "small": true, "strike": true, "strong": true, "tt": true, "u": true,
}

// flatElements do not deepen the stack of open elements for long: void
// elements have no end tag, and the others are closed by the next of
// their kind, so a list of them without end tags stays one level deep.
var flatElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true,
	"input": true, "link": true, "meta": true, "param": true, "source": true, "track": true, "wbr": true,
	"p": true, "li": true, "dt": true, "dd": true, "option": true, "optgroup": true, "tr": true, "td": true,
	"th": true, "tbody": true, "thead": true, "tfoot": true, "rb": true, "rt": true, "rp": true,
	"caption": true, "colgroup": true,
}

// maxOpenElements is the parser's bound on its stack of open elements.
const maxOpenElements = 512

// parseHTML parses body as a browser would (golang.org/x/net/html), after
// a linear pass over its tokens has bounded what the parse can build
// (parseCost): past htmlParseCap the page is errHTMLTooComplex and is
// never parsed. The pass stops with errOutOfTime once ctx ends.
func parseHTML(ctx context.Context, body []byte) (*html.Node, string) {
	cost, ended := parseCost(ctx, body, htmlParseCap)
	switch {
	case ended:
		return nil, errOutOfTime
	case cost > htmlParseCap:
		return nil, errHTMLTooComplex
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, errNotHTML
	}
	return doc, ""
}

// parseCost bounds the work the HTML parser does on body, in units of
// about a node built:
//   - one per token for the token's own node, plus its attributes;
//   - at every text or start tag, the weight (1 + its attributes) of each
//     formatting element still open, which the parser may clone there; a
//     formatting element counts as open from its start tag until an end
//     tag of the same name;
//   - a quarter of the open elements' depth per token: the parser's
//     scope checks walk its stack of open elements (at most 512).
//
// The pass stops once the cost passes limit, or with ended once ctx ends.
func parseCost(ctx context.Context, body []byte, limit int64) (cost int64, ended bool) {
	z := html.NewTokenizer(bytes.NewReader(body))
	open := map[string][]int64{}
	var weight, depth int64
	for {
		if ctx.Err() != nil {
			return cost, true
		}
		tt := z.Next()
		cost += depth / 4
		switch tt {
		case html.ErrorToken:
			// io.EOF, or the reader's error: the parser stops there too.
			return cost, false
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			attrs := int64(0)
			for hasAttr {
				_, _, hasAttr = z.TagAttr()
				attrs++
			}
			cost += weight + 1 + attrs
			if !flatElements[string(name)] && depth < maxOpenElements {
				depth++
			}
			if formattingElements[string(name)] {
				open[string(name)] = append(open[string(name)], 1+attrs)
				weight += 1 + attrs
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			if stack := open[string(name)]; len(stack) > 0 {
				weight -= stack[len(stack)-1]
				open[string(name)] = stack[:len(stack)-1]
			}
			if depth > 0 && !flatElements[string(name)] {
				depth--
			}
			cost++
		case html.TextToken:
			cost += weight + 1
		default:
			cost++
		}
		if cost > limit {
			return cost, false
		}
	}
}
