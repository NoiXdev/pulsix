package varsource

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// aliases is "*name, *name, …" n times.
func aliases(name string, n int) string {
	return strings.TrimSuffix(strings.Repeat("*"+name+", ", n), ", ")
}

// TestScalarBytesAreCharged (review P5c): one long number aliased as the
// default of thousands of options is charged on every read; the file runs
// out of its byte budget and is given up.
func TestScalarBytesAreCharged(t *testing.T) {
	var b strings.Builder
	b.WriteString("f: &f 1." + strings.Repeat("0", 20_000) + "\n")
	b.WriteString("spec: &spec {type: float, default: *f}\n")
	b.WriteString("nest: &nest {type: dict, options: {")
	for i := range 99 {
		fmt.Fprintf(&b, "s%d: *spec, ", i)
	}
	b.WriteString("}}\nargument_specs:\n  main:\n    options:\n")
	for i := range 100 {
		fmt.Fprintf(&b, "      o%d: *nest\n", i)
	}
	y := newYAMLFile(specFile)
	fields, notes, readOK, _ := readArgumentSpec(y, []byte(b.String()), "nginx", "", schema.SourceArgumentSpec)
	if readOK || fields != nil {
		t.Fatalf("readOK = %v, %d fields; want the file given up", readOK, len(fields))
	}
	wantNotes(t, notes, "warning file_too_complex "+specFile+":0")
	if y.bytesRead() > maxReadBytes {
		t.Fatalf("read %d bytes, want at most the budget %d", y.bytesRead(), maxReadBytes)
	}
}

// TestLongNumbersAreNotDecoded: a number longer than maxNumberLen is never
// decoded: as a default it is dropped, as a marker value the variable is
// not a field, as a choice it stays text.
func TestLongNumbersAreNotDecoded(t *testing.T) {
	long := "1." + strings.Repeat("0", 100)
	t.Run("default", func(t *testing.T) {
		fields, notes := readSpec(t, specYAML("x: {type: float, default: "+long+"}"))
		wantFields(t, fields, []schema.Field{fromSpec(4, schema.Field{Name: "x", Type: schema.TypeFloat})})
		wantNotes(t, notes, "warning default_invalid "+specFile+":4")
		if len(notes) == 0 || !strings.Contains(notes[0].Message, "longer than 64") {
			t.Fatalf("notes = %+v, want a message naming the length limit", notes)
		}
	})
	t.Run("marker", func(t *testing.T) {
		vars := playVars(t, "v: "+long+"  # pulsix:")
		_, notes, isMarker, ok := markedVar(newYAMLFile("site.yml"), vars.Content[0], vars.Content[1])
		if !isMarker || ok {
			t.Fatalf("isMarker, ok = %v, %v", isMarker, ok)
		}
		wantNotes(t, notes, "warning marker_value_type site.yml:3")
	})
	t.Run("choice", func(t *testing.T) {
		fields, notes := readSpec(t, specYAML("x: {choices: [a, "+long+"]}"))
		wantFields(t, fields, []schema.Field{fromSpec(4, schema.Field{Name: "x", Type: schema.TypeEnum, Options: []string{"a", long}})})
		wantNotes(t, notes, "warning choices_converted "+specFile+":4")
	})
}

// TestChoicesExaminedBounded (review P5b): a choices list longer than the
// option limit is refused before any choice is read.
func TestChoicesExaminedBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("f: &f 1." + strings.Repeat("0", 2_000) + "\n")
	b.WriteString("seq: &seq [" + aliases("f", 20_000) + "]\n")
	b.WriteString("argument_specs:\n  main:\n    options:\n")
	for i := range 100 {
		fmt.Fprintf(&b, "      o%d: {type: float, choices: *seq}\n", i)
	}
	y := newYAMLFile(specFile)
	fields, notes, readOK, _ := readArgumentSpec(y, []byte(b.String()), "nginx", "", schema.SourceArgumentSpec)
	if !readOK || len(fields) != 0 || len(notes) != 100 {
		t.Fatalf("readOK = %v, %d fields, %d notes; want 100 too_many_options", readOK, len(fields), len(notes))
	}
	for i, n := range notes {
		if n.Code != codeTooManyOptions || n.Line != 6+i {
			t.Fatalf("note %d = %s, want too_many_options at line %d", i, noteKey(n), 6+i)
		}
	}
	if y.bytesRead() > 10_000 {
		t.Fatalf("read %d bytes; want the choices left unread", y.bytesRead())
	}
}

// TestTextCaps (review P7): a description is read only up to the label and
// description caps; both are cut with an ellipsis.
func TestTextCaps(t *testing.T) {
	s := strings.Repeat("x", 1_000)
	data := []byte("s: &s " + s + "\nargument_specs:\n  main:\n    options:\n      x: {description: [" + aliases("s", 1_000) + "]}\n")
	y := newYAMLFile(specFile)
	fields, notes, readOK, _ := readArgumentSpec(y, data, "nginx", "", schema.SourceArgumentSpec)
	if !readOK || len(fields) != 1 {
		t.Fatalf("readOK = %v, %d fields, notes %v", readOK, len(fields), notes)
	}
	f := fields[0]
	if len(f.Label) > maxLabelBytes || !strings.HasSuffix(f.Label, "…") {
		t.Fatalf("label is %d bytes (%q…), want at most %d ending in an ellipsis", len(f.Label), f.Label[:10], maxLabelBytes)
	}
	if len(f.Description) > maxDescriptionBytes || !strings.HasSuffix(f.Description, "…") {
		t.Fatalf("description is %d bytes, want at most %d ending in an ellipsis", len(f.Description), maxDescriptionBytes)
	}
	if y.bytesRead() > 8_000 {
		t.Fatalf("read %d bytes; want the description read only up to its caps", y.bytesRead())
	}
}

// TestDefaultSizeCap (review P7b): a default (a list's items summed) larger
// than maxDefaultBytes is refused once the cap is passed, not read whole.
func TestDefaultSizeCap(t *testing.T) {
	s := strings.Repeat("x", 1_000)
	t.Run("marker", func(t *testing.T) {
		vars := playVars(t, "s: &s "+s, "v: ["+aliases("s", 1_000)+"]  # pulsix:")
		y := newYAMLFile("site.yml")
		_, notes, _, ok := markedVar(y, vars.Content[2], vars.Content[3])
		if ok {
			t.Fatal("ok = true, want no field")
		}
		wantNotes(t, notes, "warning marker_value_type site.yml:4")
		if y.bytesRead() > maxDefaultBytes+len(s) {
			t.Fatalf("read %d bytes, want at most the default cap", y.bytesRead())
		}
	})
	t.Run("argument spec", func(t *testing.T) {
		data := []byte("s: &s " + s + "\nargument_specs:\n  main:\n    options:\n      x: {type: list, elements: str, default: [" + aliases("s", 1_000) + "]}\n")
		y := newYAMLFile(specFile)
		fields, notes, _, _ := readArgumentSpec(y, data, "nginx", "", schema.SourceArgumentSpec)
		wantFields(t, fields, []schema.Field{fromSpec(5, schema.Field{Name: "x", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeString}})})
		wantNotes(t, notes, "warning default_invalid "+specFile+":5")
		if y.bytesRead() > maxDefaultBytes+2*len(s) {
			t.Fatalf("read %d bytes, want at most the default cap", y.bytesRead())
		}
	})
}

// TestNoteMessagesAreCapped: a long name is cut, at a rune boundary with an
// ellipsis, before the message quoting it is built.
func TestNoteMessagesAreCapped(t *testing.T) {
	name := strings.Repeat("ä", 5_000)
	_, notes := promptFields(newYAMLFile("site.yml"), promptNode(t, "- {name: "+name+", private: false}\n"))
	wantNotes(t, notes, "warning invalid_name site.yml:1")
	if len(notes) == 0 {
		return
	}
	m := notes[0].Message
	if len(m) > maxMessageBytes || !strings.Contains(m, strings.Repeat("ä", 10)+"…") || !utf8.ValidString(m) {
		t.Fatalf("message %q: %d bytes, valid UTF-8 %v; want at most %d, quoting the name cut with an ellipsis", m, len(m), utf8.ValidString(m), maxMessageBytes)
	}
}

// TestAliasKeysReadByTheirValue: an alias used as a mapping key is read as
// the scalar its anchor names, at the alias's line.
func TestAliasKeysReadByTheirValue(t *testing.T) {
	data := []byte("names: [&port http_port]\nargument_specs:\n  main:\n    options:\n      *port : {type: int}\n")
	fields, notes := readSpec(t, data)
	wantFields(t, fields, []schema.Field{fromSpec(5, schema.Field{Name: "http_port", Type: schema.TypeInt})})
	wantNotes(t, notes)
}

// allocated runs fn and returns the bytes it allocated.
func allocated(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// nested100x99 is an argument spec whose 100 top-level options are each a
// dict of 99 sub-options aliasing anchor; head defines the anchor.
func nested100x99(head, anchor string) []byte {
	var b strings.Builder
	b.WriteString(head)
	b.WriteString("nest: &nest {type: dict, options: {")
	for i := range 99 {
		fmt.Fprintf(&b, "s%d: *%s, ", i, anchor)
	}
	b.WriteString("}}\nargument_specs:\n  main:\n    options:\n")
	for i := range 100 {
		fmt.Fprintf(&b, "      o%d: *nest\n", i)
	}
	return []byte(b.String())
}

// TestMessagesQuoteShortStrings (review Q1): a long key reached through
// aliases thousands of times is charged once and quoted cut, so building
// its notes allocates little.
func TestMessagesQuoteShortStrings(t *testing.T) {
	head := "kk: &k k" + strings.Repeat("x", 100_000) + "\nl1: &l1 {type: dict, options: {*k : {type: str}, ok: {type: str}}}\n"
	data := nested100x99(head, "l1")
	y := newYAMLFile(specFile)
	var notes []Note
	alloc := allocated(func() { _, notes, _, _ = readArgumentSpec(y, data, "nginx", "", schema.SourceArgumentSpec) })
	invalid := 0
	for _, n := range notes {
		if n.Code == codeInvalidName {
			invalid++
		}
		if len(n.Message) > maxMessageBytes {
			t.Fatalf("message of %d bytes", len(n.Message))
		}
	}
	if invalid == 0 {
		t.Fatal("no invalid_name note for the long key")
	}
	if alloc > 128<<20 || y.bytesRead() > 300_000 {
		t.Fatalf("allocated %d MiB, read %d bytes; want the key read once and quoted cut", alloc>>20, y.bytesRead())
	}
}

// TestTagBytesAreCharged (review Q2): a long verbatim tag is charged with
// its scalar on every read, so aliasing it exhausts the budget.
func TestTagBytesAreCharged(t *testing.T) {
	head := "v: &v !<tag:" + strings.Repeat("x", 100_000) + "> x\nspec: &spec {type: int, default: *v}\n"
	y := newYAMLFile(specFile)
	fields, notes, readOK, _ := readArgumentSpec(y, nested100x99(head, "spec"), "nginx", "", schema.SourceArgumentSpec)
	if readOK || fields != nil {
		t.Fatalf("readOK = %v, %d fields; want the file given up", readOK, len(fields))
	}
	wantNotes(t, notes, "warning file_too_complex "+specFile+":0")
	if y.bytesRead() > maxReadBytes {
		t.Fatalf("read %d bytes, over the budget", y.bytesRead())
	}
}

// TestMarkerCommentsAreCharged (review Q3): each marker comment read is
// charged, so one long comment read again and again exhausts the budget.
func TestMarkerCommentsAreCharged(t *testing.T) {
	for name, comment := range map[string]string{
		"readable":   "# pulsix: required " + strings.Repeat("L", 100_000),
		"unreadable": "# pulsix: enum[" + strings.Repeat("a", 100_000) + ",b] secret",
	} {
		t.Run(name, func(t *testing.T) {
			vars := playVars(t, "v: 1  "+comment)
			y := newYAMLFile("site.yml")
			calls := 0
			for !y.exceeded() && calls < 1_000 {
				calls++
				_, notes, _, _ := markedVar(y, vars.Content[0], vars.Content[1])
				for _, n := range notes {
					if len(n.Message) > maxMessageBytes {
						t.Fatalf("message of %d bytes", len(n.Message))
					}
				}
			}
			if want := maxReadBytes/100_000 + 1; calls > want {
				t.Fatalf("%d reads before the budget ran out, want at most %d", calls, want)
			}
		})
	}
}

// TestLongFirstLines: a first line too long for a label is cut at a word
// boundary for the label and kept whole in the description.
func TestLongFirstLines(t *testing.T) {
	text := strings.TrimSpace(strings.Repeat("Port to listen on for requests ", 12)) // 371 bytes, one line
	check := func(t *testing.T, f schema.Field) {
		t.Helper()
		body, cut := strings.CutSuffix(f.Label, "…")
		if !cut || len(f.Label) > maxLabelBytes || !strings.HasPrefix(text, body) || text[len(body)] != ' ' {
			t.Fatalf("label %q: want a word-boundary prefix of the text ending in an ellipsis, at most %d bytes", f.Label, maxLabelBytes)
		}
		if f.Description != text {
			t.Fatalf("description = %q, want the whole text", f.Description)
		}
	}
	t.Run("argument spec", func(t *testing.T) {
		fields, _ := readSpec(t, specYAML("x: {type: str, description: \""+text+"\"}"))
		check(t, fields[0])
	})
	t.Run("argument spec list", func(t *testing.T) {
		fields, _ := readSpec(t, specYAML("x: {type: str, description: [\""+text+"\"]}"))
		check(t, fields[0])
	})
	t.Run("prompt", func(t *testing.T) {
		fields, _ := promptFields(newYAMLFile("site.yml"), promptNode(t, "- {name: x, prompt: \""+text+"\", private: false}\n"))
		check(t, fields[0])
	})
}
