package assert_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/probe/assert"
)

// TestParseSelector: the supported subset, parsed into parts, compounds
// and their conditions.
func TestParseSelector(t *testing.T) {
	cases := []struct {
		sel  string
		want assert.Selector
	}{
		{"p", assert.Selector{Parts: []assert.Part{{Compounds: []assert.Compound{{Tag: "p"}}, Child: []bool{false}}}}},
		{" DIV  p ", assert.Selector{Parts: []assert.Part{{Compounds: []assert.Compound{{Tag: "div"}, {Tag: "p"}}, Child: []bool{false, false}}}}},
		{"a>b  >  c d", assert.Selector{Parts: []assert.Part{{Compounds: []assert.Compound{{Tag: "a"}, {Tag: "b"}, {Tag: "c"}, {Tag: "d"}}, Child: []bool{false, true, true, false}}}}},
		{"*", assert.Selector{Parts: []assert.Part{{Compounds: []assert.Compound{{}}, Child: []bool{false}}}}},
		{"h1, h2,h3", assert.Selector{Parts: []assert.Part{
			{Compounds: []assert.Compound{{Tag: "h1"}}, Child: []bool{false}},
			{Compounds: []assert.Compound{{Tag: "h2"}}, Child: []bool{false}},
			{Compounds: []assert.Compound{{Tag: "h3"}}, Child: []bool{false}},
		}}},
		{`li#Main.a.B[data-x][href^="/p"][title='a b'][rel~=next][src$=".png"][alt*=x]:first-child:last-child:nth-child(3):NTH-CHILD( odd ):nth-child(even)`,
			assert.Selector{Parts: []assert.Part{{Child: []bool{false}, Compounds: []assert.Compound{{
				Tag: "li", ID: "Main", Classes: []string{"a", "B"},
				Attrs: []assert.AttrTest{
					{Name: "data-x"}, {Name: "href", Op: assert.AttrPrefix, Value: "/p"}, {Name: "title", Op: assert.AttrEquals, Value: "a b"},
					{Name: "rel", Op: assert.AttrWord, Value: "next"}, {Name: "src", Op: assert.AttrSuffix, Value: ".png"}, {Name: "alt", Op: assert.AttrContains, Value: "x"},
				},
				Pseudos: []assert.Pseudo{{Kind: assert.PseudoFirst}, {Kind: assert.PseudoLast}, {Kind: assert.PseudoNth, N: 3}, {Kind: assert.PseudoOdd}, {Kind: assert.PseudoEven}},
			}}}}},
		},
		{"[ TITLE = x ]", assert.Selector{Parts: []assert.Part{{Compounds: []assert.Compound{{Attrs: []assert.AttrTest{{Name: "title", Op: assert.AttrEquals, Value: "x"}}}}, Child: []bool{false}}}}},
	}
	for _, tc := range cases {
		got, err := assert.ParseSelector(tc.sel)
		if err != nil {
			t.Errorf("ParseSelector(%q) = %v", tc.sel, err)
			continue
		}
		if !reflect.DeepEqual(*got, tc.want) {
			t.Errorf("ParseSelector(%q) = %+v, want %+v", tc.sel, *got, tc.want)
		}
	}
	for _, sel := range []string{"main div p", "nav ul li a", "table tbody tr td", ".content .price", "div.card > .inner .price", "tr:nth-child(2) td", "ul > li:first-child a"} {
		if _, err := assert.ParseSelector(sel); err != nil {
			t.Errorf("ParseSelector(%q) = %v", sel, err)
		}
	}
}

// TestParseSelectorRefuses: everything outside the subset, naming it.
func TestParseSelectorRefuses(t *testing.T) {
	cases := []struct{ sel, want string }{
		{"", "ends where"},
		{"a,", "ends where"},
		{",a", `cannot use ','`},
		{"a > ", "ends where"},
		{"> a", `cannot use '>'`},
		{"h1 ~ p", "combinator ~"},
		{"h1 + p", "combinator +"},
		{"h1~p", "combinator ~"},
		{"p:hover", ":hover"},
		{"p:has(a)", ":has"},
		{"p:not(.a)", ":not"},
		{"p:empty", ":empty"},
		{"p:nth-child(2n+1)", "whole number"},
		{"p:nth-child(0)", "whole number"},
		{"p:nth-child(-1)", "whole number"},
		{"p:nth-child(+2)", "whole number"},
		{"p:nth-child(2", "whole number"},
		{"p:nth-child", ":nth-child(N)"},
		{"p::before", "pseudo-elements"},
		{"[a|=b]", `"|="`},
		{"[a#=b]", `"#="`},
		{"[a!=b]", `"!="`},
		{"[a=b i]", "flags"},
		{"[a=]", "needs a name"},
		{"[a", "needs ]"},
		{`[a="b]`, "closing quote"},
		{`[a="b\"c"]`, "escapes"},
		{`#\31 23`, "escapes"},
		{`a\ b`, "escapes"},
		{"#", "needs a name"},
		{".1a", "needs a name"},
		{"[]", "attribute name"},
		{"a/**/b", "comments"},
		{"a b c d e f g h i", "at most 8 compound"},
		{"a" + strings.Repeat(".c", 16), "at most 16 simple"},
		{"a b c d e f g h, i", ""},
	}
	for _, tc := range cases {
		_, err := assert.ParseSelector(tc.sel)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("ParseSelector(%q) = %v, want it accepted", tc.sel, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("ParseSelector(%q) = %v, want an error containing %q", tc.sel, err, tc.want)
		}
	}
	// The bounds themselves pass.
	if _, err := assert.ParseSelector("a" + strings.Repeat(".c", 15)); err != nil {
		t.Errorf("16 simple selectors refused: %v", err)
	}
}

// TestHTMLAssertionSelector: Parse refuses a selector outside the subset,
// comments included, and keeps the parsed one.
func TestHTMLAssertionSelector(t *testing.T) {
	if _, err := assert.Parse(`x=html("a/**/b") exists`); err == nil || !strings.Contains(err.Error(), "comments") {
		t.Errorf("a comment = %v", err)
	}
	if _, err := assert.Parse(`x=html("a ~ b") exists`); err == nil || !strings.Contains(err.Error(), "combinator ~") {
		t.Errorf("a ~ = %v", err)
	}
	a := mustParse(t, `x=html("div > p") count > 1`)
	if sel := a.Selector(); sel == nil || len(sel.Parts) != 1 || len(sel.Parts[0].Compounds) != 2 {
		t.Fatalf("Selector() = %+v", sel)
	}
	if mustParse(t, "x=status == 200").Selector() != nil {
		t.Fatal("a status assertion has a selector")
	}
}

// TestRegexProgramSize: a pattern compiling to more than 1 000
// instructions is refused, however short; the size is kept for the
// probe's budget.
func TestRegexProgramSize(t *testing.T) {
	if _, err := assert.Parse(`x=body matches ".{1000}"`); err == nil || !strings.Contains(err.Error(), "too complex") {
		t.Errorf(".{1000} = %v, want too complex", err)
	}
	if _, err := assert.Parse(`x=body !matches "` + strings.Repeat(`[ab]{100}`, 11) + `"`); err == nil || !strings.Contains(err.Error(), "too complex") {
		t.Errorf("a long program = %v, want too complex", err)
	}
	a := mustParse(t, `x=body matches "v[0-9]+"`)
	if n := a.ProgramSize(); n < 3 || n > 20 {
		t.Errorf("ProgramSize(v[0-9]+) = %d", n)
	}
	if n := mustParse(t, `x=body matches ".{900}"`).ProgramSize(); n < 900 || n > assert.MaxProgram {
		t.Errorf("ProgramSize(.{900}) = %d", n)
	}
	if n := mustParse(t, `x=body contains "x"`).ProgramSize(); n != 0 {
		t.Errorf("contains has a program of %d", n)
	}
}
