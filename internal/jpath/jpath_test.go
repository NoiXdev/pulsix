package jpath_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/jpath"
)

// budget is generous for these small documents.
const budget = 1 << 20

func decode(t *testing.T, doc string) any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode %q: %v", doc, err)
	}
	return v
}

// TestParseAccepts: every construct of the subset, with RFC 9535's blank
// space where the RFC allows it.
func TestParseAccepts(t *testing.T) {
	for _, path := range []string{
		"$",
		"$.a",
		"$.a.b",
		"$._a1",
		"$.ä",
		"$.true",
		"$.*",
		"$['a']",
		`$["a"]`,
		"$['']",
		"$['a b']",
		`$['\'']`,
		`$["\""]`,
		`$['"']`,
		`$["'"]`,
		`$['\b\f\n\r\t\/\\']`,
		`$['ä']`,
		`$['😀']`,
		"$['ü']",
		"$[0]",
		"$[-1]",
		"$[9007199254740991]",
		"$[-9007199254740991]",
		"$[*]",
		"$[ 0 ]",
		"$[\t'a'\n]",
		"$ .a",
		"$\n[0]",
		"$..a",
		"$..*",
		"$..[0]",
		"$..['a']",
		"$..[?@.a]",
		"$.a..b",
		"$[?@]",
		"$[?@.a]",
		"$[?!@.a]",
		"$[?@.a == 1]",
		"$[?@.a==1]",
		"$[?@.a != 'x']",
		"$[?@.a < 1]",
		"$[?@.a <= -1.5]",
		"$[?@.a > 1e3]",
		"$[?@.a >= 1E-3]",
		"$[?@.a == 1.5e+2]",
		"$[?@.a == -0]",
		"$[?@.a == 0.5]",
		"$[?@.a == true]",
		"$[?@.a == false]",
		"$[?@.a == null]",
		`$[?@.a == "x"]`,
		"$[?@['a'][0].b == 1]",
		"$[?@[-1] == 1]",
		"$[?@ .a == 1]",
		"$[?@.a == 1 && @.b == 2]",
		"$[?@.a == 1 || @.b == 2]",
		"$[?(@.a == 1)]",
		"$[?!(@.a == 1)]",
		"$[?! (@.a == 1)]",
		"$[?( @.a == 1 || @.b ) && !@.c]",
		"$[?@.a==1&&@.b==2||@.c==3&&@.d==4||@.e&&@.f&&@.g&&@.h]",
		"$.checks[?@.name=='db'].status",
		"$." + strings.Repeat("p", 510),
	} {
		if _, err := jpath.Parse(path); err != nil {
			t.Errorf("Parse(%q) = %v, want it accepted", path, err)
		}
	}
}

// TestParseRefuses: RFC 9535 syntax errors, and everything outside the
// subset, including every construct that multiplies the work.
func TestParseRefuses(t *testing.T) {
	for path, why := range map[string]string{
		"":                        "a path starts with $",
		"a":                       "a path starts with $",
		" $":                      "a path starts with $",
		"$ ":                      "blank space after the path",
		"$.":                      "expected a name",
		"$.1a":                    "expected a name",
		"$.a-b":                   "expected ., .. or [",
		"$a":                      "expected ., .. or [",
		"$..":                     "expected a name",
		"$.. a":                   "expected a name",
		"$...a":                   "expected a name",
		"$[":                      "expected a selector",
		"$[]":                     "expected a name, an index, * or a filter",
		"$['a'":                   "expected ]",
		"$['a":                    "the string is not closed",
		"$['\x01']":               "a control character in a string must be escaped",
		`$['\"']`:                 "invalid escape",
		`$["\'"]`:                 "invalid escape",
		`$['\x']`:                 "invalid escape",
		`$['\u12']`:               "\\u needs four hex digits",
		`$['\uDE00']`:             "a low surrogate without a high one",
		`$['\uD83D']`:             "a high surrogate without a low one",
		`$['\uD83DA']`:            "a high surrogate without a low one",
		"$[01]":                   "an index has no leading zero",
		"$[-0]":                   "-0 is not an index",
		"$[9007199254740992]":     "the index is out of range",
		"$[1.5]":                  "expected ]",
		"$\xff":                   "the path is not valid UTF-8",
		"$..a..b":                 "at most one descendant segment (..)",
		"$..*..*..*":              "at most one descendant segment (..)",
		"$[0,0]":                  "a bracket holds one selector",
		"$[*,*]":                  "a bracket holds one selector",
		"$['a','b']":              "a bracket holds one selector",
		"$..[*,*]":                "a bracket holds one selector",
		"$[0:2]":                  "slices are not supported",
		"$[:2]":                   "slices are not supported",
		"$[::-1]":                 "slices are not supported",
		"$[?$.a]":                 "$ is not supported inside a filter",
		"$[?@.a == $.b]":          "$ is not supported inside a filter",
		"$[0][?count($[0][*])>0]": "functions are not supported",
		"$[?length(@) > 1]":       "functions are not supported",
		"$[?match(@, 'a.*')]":     "functions are not supported",
		"$[?search(@.a, @.b)]":    "functions are not supported",
		"$[?@[?@.a]]":             "a filter inside a filter is not supported",
		"$..[?@[?@[?@]]]":         "a filter inside a filter is not supported",
		"$[?@.*]":                 "a wildcard is not supported inside a filter",
		"$[?@[*]]":                "a wildcard is not supported inside a filter",
		"$..[?@..x]":              "a descendant segment is not supported inside a filter",
		"$[?@[0,1]]":              "a bracket holds one selector",
		"$[?@[ 0]]":               "expected a name or an index",
		"$[?1 == @.a]":            "a comparison starts with an @ path",
		"$[?true]":                "a comparison starts with an @ path",
		"$[?@.a == @.b]":          "a comparison has a literal on its right",
		"$[?@.a = 1]":             "expected ]",
		"$[?@.a == 01]":           "a number has no leading zero",
		"$[?@.a == 1.]":           "expected a digit",
		"$[?@.a == .5]":           "expected a literal",
		"$[?@.a == 1e]":           "expected a digit",
		"$[?@.a == True]":         "expected a literal",
		"$[?!@.a == 1]":           "expected ]",
		"$[?!1]":                  "! applies to ( or @ only",
		"$[?(@.a]":                "expected )",
		"$[?@.a &&]":              "a comparison starts with an @ path",
		"$[?@.a&&@.b&&@.c&&@.d&&@.e&&@.f&&@.g&&@.h&&@.i]": "a filter holds at most 8 comparisons",
	} {
		_, err := jpath.Parse(path)
		if err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("Parse(%q) = %v, want an error saying %q", path, err, why)
		}
	}
}

// rfcDoc is RFC 9535's examples document (section 1.5) plus filter
// fixtures of section 2.3.5.3.
const rfcDoc = `{
	"store": {
		"book": [
			{"category": "reference", "author": "Nigel Rees", "title": "Sayings of the Century", "price": 8.95},
			{"category": "fiction", "author": "Evelyn Waugh", "title": "Sword of Honour", "price": 12.99},
			{"category": "fiction", "author": "Herman Melville", "title": "Moby Dick", "isbn": "0-553-21311-3", "price": 8.99},
			{"category": "fiction", "author": "J. R. R. Tolkien", "title": "The Lord of the Rings", "isbn": "0-395-19395-8", "price": 22.99}
		],
		"bicycle": {"color": "red", "price": 399}
	},
	"o": {"j j": {"k.k": 3}},
	"'": {"@": 2},
	"a": [3, 5, 1, 2, 4, 6, {"b": "j"}, {"b": "k"}, {"b": {}}, {"b": "kilo"}],
	"n": [{"a": null}, {"a": 1}, {"b": 1}, {"a": "1"}, {"a": true}, {"a": [1]}, {"a": {"x": 1}}],
	"e": [],
	"big": 12345678901234567890
}`

// TestSelectRFCVectors: the subset's semantics follow RFC 9535: names,
// indexes, wildcards, descendants and filters with its comparison rules.
func TestSelectRFCVectors(t *testing.T) {
	doc := decode(t, rfcDoc)
	for _, tc := range []struct {
		path string
		want string // the node list as JSON, sorted when unordered
	}{
		{"$.store.book[0].author", `["Nigel Rees"]`},
		{"$['store']['book'][-1].title", `["The Lord of the Rings"]`},
		{"$.store.book[4]", `[]`},
		{"$.store.book[-5]", `[]`},
		{"$.store.book.author", `[]`},
		{"$.store.bicycle[0]", `[]`},
		{`$.o['j j']['k.k']`, `[3]`},
		{`$["'"]["@"]`, `[2]`},
		{"$.store.book[*].author", `["Evelyn Waugh","Herman Melville","J. R. R. Tolkien","Nigel Rees"]`},
		{"$.store.*", `[[{"author":"Nigel Rees","category":"reference","price":8.95,"title":"Sayings of the Century"},{"author":"Evelyn Waugh","category":"fiction","price":12.99,"title":"Sword of Honour"},{"author":"Herman Melville","category":"fiction","isbn":"0-553-21311-3","price":8.99,"title":"Moby Dick"},{"author":"J. R. R. Tolkien","category":"fiction","isbn":"0-395-19395-8","price":22.99,"title":"The Lord of the Rings"}],{"color":"red","price":399}]`},
		{"$..author", `["Evelyn Waugh","Herman Melville","J. R. R. Tolkien","Nigel Rees"]`},
		{"$.store..price", `[12.99,22.99,399,8.95,8.99]`},
		{"$..book[2].title", `["Moby Dick"]`},
		{"$..book[-1].title", `["The Lord of the Rings"]`},
		{"$..[?@.isbn].title", `["Moby Dick","The Lord of the Rings"]`},
		{"$..book[?@.price<10].title", `["Moby Dick","Sayings of the Century"]`},
		{"$.store.book[?@.price >= 12.99 && @.category == 'fiction'].author", `["Evelyn Waugh","J. R. R. Tolkien"]`},
		{"$.store.book[?!@.isbn].title", `["Sayings of the Century","Sword of Honour"]`},
		{"$.store.book[?(@.price < 9 || @.price > 20) && !(@.author == 'Herman Melville')].title", `["Sayings of the Century","The Lord of the Rings"]`},
		// Section 2.3.5.3's examples over "a".
		{"$.a[?@.b == 'kilo']", `[{"b":"kilo"}]`},
		{"$.a[?@>3.5]", `[4,5,6]`},
		{"$.a[?@.b]", `[{"b":"j"},{"b":"k"},{"b":"kilo"},{"b":{}}]`},
		{"$.a[?@<2 || @.b == 'k']", `[1,{"b":"k"}]`},
		{"$.a[?@.b > 'k']", `[{"b":"kilo"}]`},
		{"$.a[?@.b >= 'k']", `[{"b":"k"},{"b":"kilo"}]`},
		// Types: a number never equals or orders against a string; null
		// equals null only; Nothing equals nothing.
		{"$.n[?@.a == 1]", `[{"a":1}]`},
		{"$.n[?@.a == '1']", `[{"a":"1"}]`},
		{"$.n[?@.a < 2]", `[{"a":1}]`},
		{"$.n[?@.a == null]", `[{"a":null}]`},
		{"$.n[?@.a != null]", `[{"a":"1"},{"a":1},{"a":[1]},{"a":true},{"a":{"x":1}},{"b":1}]`},
		{"$.n[?@.a]", `[{"a":"1"},{"a":1},{"a":[1]},{"a":null},{"a":true},{"a":{"x":1}}]`},
		{"$.n[?@.a == true]", `[{"a":true}]`},
		{"$.n[?@.a < true]", `[]`},
		{"$.n[?@.a <= null]", `[{"a":null}]`},
		{"$.n[?@.a > 0]", `[{"a":1}]`},
		{"$.n[?@.a <= 'z']", `[{"a":"1"}]`},
		{"$.n[?@.a[0] == 1]", `[{"a":[1]}]`},
		{"$.n[?@.a.x == 1]", `[{"a":{"x":1}}]`},
		{"$.n[?@.a == 1.0]", `[{"a":1}]`},
		{"$.n[?@.a == 1e0]", `[{"a":1}]`},
		{"$.e[*]", `[]`},
		{"$.e[0]", `[]`},
		{"$[?@ == 12345678901234567890]", `[12345678901234567890]`},
		{"$", ``},
	} {
		t.Run(tc.path, func(t *testing.T) {
			p, err := jpath.Parse(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			nodes, err := p.Select(doc, budget)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(nodes) != 1 || !reflect.DeepEqual(nodes[0], doc) {
					t.Fatalf("$ = %v, want the root", nodes)
				}
				return
			}
			if got := canonical(t, nodes); got != tc.want {
				t.Fatalf("Select = %s, want %s", got, tc.want)
			}
		})
	}
}

// canonical is nodes as JSON, sorted: objects iterate in no fixed order.
func canonical(t *testing.T, nodes []any) string {
	t.Helper()
	parts := make([]string, len(nodes))
	for i, n := range nodes {
		b, err := json.Marshal(n)
		if err != nil {
			t.Fatal(err)
		}
		parts[i] = string(b)
	}
	sort.Strings(parts)
	return "[" + strings.Join(parts, ",") + "]"
}

// TestSelectKeepsDocumentOrderInArrays: descendants come in document
// order where the document has one.
func TestSelectKeepsDocumentOrderInArrays(t *testing.T) {
	doc := decode(t, `[[1,[2]],[3]]`)
	p, _ := jpath.Parse("$..[0]")
	nodes, err := p.Select(doc, budget)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	for _, n := range nodes {
		fmt.Fprint(&b, n, ";")
	}
	if b.String() != "[1 [2]];1;2;3;" {
		t.Fatalf("Select = %s, want [1 [2]];1;2;3;", b.String())
	}
}

// TestSelectStopsAtTheBudget: the budget bounds every evaluation.
func TestSelectStopsAtTheBudget(t *testing.T) {
	doc := decode(t, "["+strings.Repeat("[1,1,1],", 999)+"[1,1,1]]")
	for path, want := range map[string]error{
		"$[*][*]":      nil,      // 1000 + 3000 visits
		"$..*":         nil,      // 4000 + 4000
		"$..[?@ == 1]": nil,      // 8000
		"$[*][*][*]":   nil,      // a number has no children
		"$..*..*":      errParse, // refused before it could run
	} {
		p, err := jpath.Parse(path)
		if err != nil {
			if want != errParse {
				t.Fatalf("Parse(%q) = %v", path, err)
			}
			continue
		}
		if _, err := p.Select(doc, 10_000); err != want {
			t.Errorf("Select(%q, 10000) = %v, want %v", path, err, want)
		}
		if _, err := p.Select(doc, 3_000); !errors.Is(err, jpath.ErrTooMuch) {
			t.Errorf("Select(%q, 3000) = %v, want ErrTooMuch", path, err)
		}
	}
}

var errParse = errors.New("refused by Parse")
