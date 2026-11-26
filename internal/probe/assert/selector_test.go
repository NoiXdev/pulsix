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
		{"p", assert.Selector{Parts: []assert.Part{{Compounds: []assert.Compound{{Tag: "p"}}, Combinators: []assert.Combinator{assert.Descendant}}}}},
		{" DIV  p ", assert.Selector{Parts: []assert.Part{{Compounds: []assert.Compound{{Tag: "div"}, {Tag: "p"}}, Combinators: []assert.Combinator{assert.Descendant, assert.Descendant}}}}},
		{"a>b  >  c d", assert.Selector{Parts: []assert.Part{{Compounds: []assert.Compound{{Tag: "a"}, {Tag: "b"}, {Tag: "c"}, {Tag: "d"}}, Combinators: []assert.Combinator{assert.Descendant, assert.Child, assert.Child, assert.Descendant}}}}},
		{"*", assert.Selector{Parts: []assert.Part{{Compounds: []assert.Compound{{}}, Combinators: []assert.Combinator{assert.Descendant}}}}},
		{"h1, h2,h3", assert.Selector{Parts: []assert.Part{
			{Compounds: []assert.Compound{{Tag: "h1"}}, Combinators: []assert.Combinator{assert.Descendant}},
			{Compounds: []assert.Compound{{Tag: "h2"}}, Combinators: []assert.Combinator{assert.Descendant}},
			{Compounds: []assert.Compound{{Tag: "h3"}}, Combinators: []assert.Combinator{assert.Descendant}},
		}}},
		{`li#Main.a.B[data-x][href^="/p"][title='a b'][rel~=next][src$=".png"][alt*=x]:first-child:last-child:nth-child(3):NTH-CHILD( odd ):nth-child(even)`,
			assert.Selector{Parts: []assert.Part{{Combinators: []assert.Combinator{assert.Descendant}, Compounds: []assert.Compound{{
				Tag: "li", ID: "Main", Classes: []string{"a", "B"},
				Attrs: []assert.AttrTest{
					{Name: "data-x"}, {Name: "href", Op: assert.AttrPrefix, Value: "/p"}, {Name: "title", Op: assert.AttrEquals, Value: "a b"},
					{Name: "rel", Op: assert.AttrWord, Value: "next"}, {Name: "src", Op: assert.AttrSuffix, Value: ".png"}, {Name: "alt", Op: assert.AttrContains, Value: "x"},
				},
				Pseudos: []assert.Pseudo{{Kind: assert.PseudoFirst}, {Kind: assert.PseudoLast}, {Kind: assert.PseudoNth, B: 3}, {Kind: assert.PseudoNth, A: 2, B: 1}, {Kind: assert.PseudoNth, A: 2, B: 0}},
			}}}}},
		},
		{"h1+p ~ ul > li  +  li a", assert.Selector{Parts: []assert.Part{{
			Compounds:   []assert.Compound{{Tag: "h1"}, {Tag: "p"}, {Tag: "ul"}, {Tag: "li"}, {Tag: "li"}, {Tag: "a"}},
			Combinators: []assert.Combinator{assert.Descendant, assert.Adjacent, assert.Sibling, assert.Child, assert.Adjacent, assert.Descendant},
		}}}},
		{"p:only-child:nth-last-child(2n+1):first-of-type:last-of-type:only-of-type:nth-of-type(-n+3):nth-last-of-type(even):empty:root",
			assert.Selector{Parts: []assert.Part{{Combinators: []assert.Combinator{assert.Descendant}, Compounds: []assert.Compound{{
				Tag: "p",
				Pseudos: []assert.Pseudo{
					{Kind: assert.PseudoOnly}, {Kind: assert.PseudoNthLast, A: 2, B: 1}, {Kind: assert.PseudoFirstOfType}, {Kind: assert.PseudoLastOfType},
					{Kind: assert.PseudoOnlyOfType}, {Kind: assert.PseudoNthOfType, A: -1, B: 3}, {Kind: assert.PseudoNthLastOfType, A: 2, B: 0},
					{Kind: assert.PseudoEmpty}, {Kind: assert.PseudoRoot},
				},
			}}}}},
		},
		{`li:not(.a, p#x[title], :first-child, *):not( b )`,
			assert.Selector{Parts: []assert.Part{{Combinators: []assert.Combinator{assert.Descendant}, Compounds: []assert.Compound{{
				Tag: "li",
				Pseudos: []assert.Pseudo{
					{Kind: assert.PseudoNot, Not: []assert.Compound{
						{Classes: []string{"a"}}, {Tag: "p", ID: "x", Attrs: []assert.AttrTest{{Name: "title"}}},
						{Pseudos: []assert.Pseudo{{Kind: assert.PseudoFirst}}}, {},
					}},
					{Kind: assert.PseudoNot, Not: []assert.Compound{{Tag: "b"}}},
				},
			}}}}},
		},
		{"[ TITLE = x ]", assert.Selector{Parts: []assert.Part{{Compounds: []assert.Compound{{Attrs: []assert.AttrTest{{Name: "title", Op: assert.AttrEquals, Value: "x"}}}}, Combinators: []assert.Combinator{assert.Descendant}}}}},
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
	for _, sel := range []string{"main div p", "nav ul li a", "table tbody tr td", ".content .price", "div.card > .inner .price", "tr:nth-child(2) td", "ul > li:first-child a", "h2 + p", "h2 ~ p", "dt+dd", "li:not(:last-child)"} {
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
		{"~ p", `cannot use '~'`},
		{"+ p", `cannot use '+'`},
		{"h1 ~", "ends where"},
		{"h1 + > p", `cannot use '>'`},
		{"h1 ~~ p", `cannot use '~'`},
		{"p:hover", ":hover"},
		{"p:has(a)", ":has"},
		{"p:is(a)", ":is"},
		{"p:where(a)", ":where"},
		{"input:checked", ":checked"},
		{"a:link", ":link"},
		{"p:lang(en)", ":lang"},
		{"p:", "needs a pseudo-class name"},
		{"p:not(a b)", "combinators inside :not"},
		{"p:not(a > b)", "combinators inside :not"},
		{"p:not(a, b c)", "combinators inside :not"},
		{"p:not(:not(a))", "nest :not"},
		{"p:not()", "needs a selector inside :not"},
		{"p:not(a,)", "needs a selector inside :not"},
		{"p:not(,a)", "needs a selector inside :not"},
		{"p:not(a", "needs ) to close :not"},
		{"p:not(::before)", "pseudo-elements"},
		{"p:not", "needs ( after :not"},
		{"p:nth-child", "needs ( after :nth-child"},
		{"p:nth-child(2", "needs ) to close :nth-child"},
		{"p:nth-child()", "An+B"},
		{"p:nth-child(2n+1 of .a)", "of S"},
		{"p:nth-child(2n+1\tof .a)", "of S"},
		{"p:nth-child(odd\nof p)", "of S"},
		{"p:nth-child(2n+1  OF p)", "of S"},
		{"p:nth-of-type(x)", "An+B"},
		{"p:first-child()", `cannot use '('`},
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
		{"#x#y", "one #id"},
		{"p#x.a#y", "one #id"},
		{".1a", "needs a name"},
		{"[]", "attribute name"},
		{"a/**/b", "comments"},
		{"a b c d e f g h i", "at most 8 compound"},
		{"a" + strings.Repeat(".c", 16), "at most 16 simple"},
		{"a b c d e f g h, i", ""},
		{"a+b~c>d e+f~g>h", ""},
		{"a+b~c>d e+f~g>h+i", "at most 8 compound"},
		// A :not counts itself and every simple selector inside.
		{"a" + strings.Repeat(".c", 13) + ":not(.d)", ""},
		{"a" + strings.Repeat(".c", 13) + ":not(.d.e)", "at most 16 simple"},
		{"a:not(" + strings.Repeat(".c", 15) + ")", "at most 16 simple"},
		{":not(" + strings.Repeat(".c", 15) + ")", ""},
		{":not(.a,.b,.c,.d,.e,.f,.g,.h,.i,.j,.k,.l,.m,.n,.o) p", ""},
		{":not(.a,.b,.c,.d,.e,.f,.g,.h,.i,.j,.k,.l,.m,.n,.o,.p) p", "at most 16 simple"},
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

// TestParseNth: the An+B argument of the nth pseudo-classes, as CSS
// writes it, with |A| and |B| at most a million.
func TestParseNth(t *testing.T) {
	valid := []struct {
		arg  string
		a, b int
	}{
		{"odd", 2, 1}, {"EVEN", 2, 0}, {" 3 ", 0, 3}, {"0", 0, 0}, {"-1", 0, -1}, {"+2", 0, 2},
		{"n", 1, 0}, {"+n", 1, 0}, {"-n", -1, 0}, {"N", 1, 0}, {"2n", 2, 0}, {"2n+1", 2, 1}, {"2N-1", 2, -1},
		{"-n+3", -1, 3}, {"+3n-2", 3, -2}, {" 2n + 1 ", 2, 1}, {"2n- 1", 2, -1}, {"0n+5", 0, 5},
		{"1000000n+1000000", 1_000_000, 1_000_000}, {"-1000000n-1000000", -1_000_000, -1_000_000},
		// Leading zeros, which 3.13b's whole numbers allowed in any count.
		{"00000001", 0, 1}, {"0001000000", 0, 1_000_000}, {strings.Repeat("0", 200) + "7", 0, 7},
		{"0002n+00003", 2, 3}, {"000", 0, 0},
	}
	for _, tc := range valid {
		sel, err := assert.ParseSelector("p:nth-child(" + tc.arg + ")")
		if err != nil {
			t.Errorf("nth-child(%s) = %v", tc.arg, err)
			continue
		}
		want := assert.Pseudo{Kind: assert.PseudoNth, A: tc.a, B: tc.b}
		if got := sel.Parts[0].Compounds[0].Pseudos[0]; !reflect.DeepEqual(got, want) {
			t.Errorf("nth-child(%s) = %+v, want %+v", tc.arg, got, want)
		}
	}
	for _, arg := range []string{
		"", " ", "x", "2 n", "- n", "+ 2", "n-", "n+", "2n+", "n - -1", "n++1", "2n+1n", "1.5", "2n+1.5",
		"n1", "--n", "+-2", "1000001", "1000001n", "n+1000001", "-1000001", "99999999999999999999", "2n 1", "odd2",
		"0001000001", strings.Repeat("0", 200) + "99999999",
	} {
		if _, err := assert.ParseSelector("p:nth-child(" + arg + ")"); err == nil || !strings.Contains(err.Error(), "An+B") {
			t.Errorf("nth-child(%s) = %v, want an An+B refusal", arg, err)
		}
	}
}

// TestSelectorOfType: OfType tells the matcher to count element names,
// also inside :not.
func TestSelectorOfType(t *testing.T) {
	for sel, want := range map[string]bool{
		"p": false, "p:nth-child(2)": false, "p:first-of-type": true, "a, p:not(:last-of-type)": true,
		"b + p:nth-last-of-type(2)": true, "p:only-of-type": true, "p:nth-of-type(odd)": true, "p:not(.a)": false,
	} {
		s, err := assert.ParseSelector(sel)
		if err != nil {
			t.Fatalf("%q: %v", sel, err)
		}
		if got := s.OfType(); got != want {
			t.Errorf("OfType(%q) = %v, want %v", sel, got, want)
		}
	}
}

// TestHTMLAssertionSelector: Parse refuses a selector outside the subset,
// comments included, and keeps the parsed one.
func TestHTMLAssertionSelector(t *testing.T) {
	if _, err := assert.Parse(`x=html("a/**/b") exists`); err == nil || !strings.Contains(err.Error(), "comments") {
		t.Errorf("a comment = %v", err)
	}
	if _, err := assert.Parse(`x=html("a ~ ~ b") exists`); err == nil || !strings.Contains(err.Error(), "'~'") {
		t.Errorf("a ~ ~ b = %v", err)
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
