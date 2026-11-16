package http

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

// openBold is a page that leaves open formatting elements over many
// paragraphs: the HTML parser clones every one of them into each
// paragraph (the re-review's repro: 450 and 60 000 make 244 KB into 27 M
// nodes and 4 GB).
func openBold(open, paragraphs int) string {
	var b strings.Builder
	b.WriteString("<p>")
	for i := range open {
		fmt.Fprintf(&b, "<b a=%d>", i)
	}
	b.WriteString(strings.Repeat("<p>x", paragraphs))
	return b.String()
}

// TestParseHTMLRefusesAComplexPage: the token pass refuses a page that
// would make the parser build millions of clones, quickly and without
// building them; a realistic page parses.
func TestParseHTMLRefusesAComplexPage(t *testing.T) {
	for _, page := range []string{
		openBold(450, 60_000),
		openBold(450, 2_000),
		// Every open formatting element's attributes are cloned too.
		"<p>" + strings.Repeat("<b "+manyAttributes(50)+">", 20) + strings.Repeat("<p>x", 5_000),
		// 500-deep nesting makes every token walk the parser's stack.
		strings.Repeat(strings.Repeat("<div>", 500)+strings.Repeat("</div>", 500), 200),
	} {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		doc, problem := parseHTML(context.Background(), []byte(page))
		d := time.Since(start)
		runtime.ReadMemStats(&after)
		if problem != errHTMLTooComplex || doc != nil {
			t.Errorf("a %d-byte hostile page = %v, %q; want %q", len(page), doc, problem, errHTMLTooComplex)
		}
		if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 64<<20 || d > 2*time.Second {
			t.Errorf("refusing a %d-byte page took %v and %d MB", len(page), d, alloc>>20)
		}
	}
	for _, page := range []string{shopPage(), openBold(5, 50_000), strings.Repeat("<li>x", 100_000), strings.Repeat("<div>", 300)} {
		if doc, problem := parseHTML(context.Background(), []byte(page)); problem != "" || doc == nil {
			t.Errorf("a %d-byte ordinary page = %q", len(page), problem)
		}
	}
}

// TestParseCost: one unit per token and attribute, the open formatting
// elements' weight at every text and start tag (until their own end tag),
// and a quarter of the open depth per token.
func TestParseCost(t *testing.T) {
	cases := []struct {
		page string
		want int64
	}{
		{"", 0},
		{"x", 1},
		{"<p>x</p>", 3},
		{`<p a=1 b=2>`, 3},
		// <b> open over the text and the <i>: weight 1 each.
		{"<b>x<i>y", 1 + 2 + 2 + 3},
		{"<b>x</b>y", 1 + 2 + 1 + 1},
		// An unrelated end tag closes nothing.
		{"<b>x</i>y", 1 + 2 + 1 + 2},
		// An attribute adds to the clone's weight.
		{"<b c=1>x", 2 + 3},
		// Four nested divs: depth 4 adds one to each later token, the
		// end of input included.
		{"<div><div><div><div>x", 4 + 2 + 1},
	}
	for _, tc := range cases {
		if got, ended := parseCost(context.Background(), []byte(tc.page), 1<<40); got != tc.want || ended {
			t.Errorf("parseCost(%q) = %d, %v; want %d", tc.page, got, ended, tc.want)
		}
	}
	if got, _ := parseCost(context.Background(), []byte(openBold(450, 60_000)), 1000); got > 2000 {
		t.Errorf("past the limit the pass went on to %d", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, problem := parseHTML(ctx, []byte(shopPage())); problem != errOutOfTime {
		t.Errorf("an ended probe = %q, want %q", problem, errOutOfTime)
	}
}
