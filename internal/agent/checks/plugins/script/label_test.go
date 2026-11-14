package script

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

func TestCleanMessage(t *testing.T) {
	long := strings.Repeat("a", 511) + "äöü" // 'ä' straddles byte 512
	tests := []struct {
		name, in, want string
	}{
		{"plain", "OK - all fine", "OK - all fine"},
		{"trimmed", "  \t OK  ", "OK"},
		{"invalid UTF-8 replaced", "disk \xff full", "disk \uFFFD full"},
		{"control characters removed", "a\x00b\x1bc\x7fd\te", "abcde"},
		{"line separators removed", "a\u2028b\u2029c\u0085d", "abcd"},
		{"empty", "", noOutput},
		{"only spaces and controls", " \x01\t ", noOutput},
		{"cut on a rune boundary", long, strings.Repeat("a", 511)},
		{"exactly the bound", strings.Repeat("b", checks.MaxLabelLen), strings.Repeat("b", checks.MaxLabelLen)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanMessage(tt.in)
			if got != tt.want {
				t.Errorf("cleanMessage(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if !checks.ReportableLabel(got) {
				t.Errorf("cleanMessage(%q) = %q is not a reportable label", tt.in, got)
			}
		})
	}
}

func TestCleanMessageAlwaysReportable(t *testing.T) {
	inputs := []string{
		strings.Repeat("\xff", 4096),
		strings.Repeat("€", 1000),
		strings.Repeat(" x\x00", 600),
	}
	for _, in := range inputs {
		got := cleanMessage(in)
		if !checks.ReportableLabel(got) || len(got) > checks.MaxLabelLen || !utf8.ValidString(got) {
			t.Errorf("cleanMessage of %d bytes = %q: not reportable", len(in), got)
		}
	}
}

func TestValueSet(t *testing.T) {
	t.Run("items are command:label, in order", func(t *testing.T) {
		vs := newValueSet("disk_root")
		vs.add("/", 1)
		vs.add("free space", 2)
		want := []itemValue{{"disk_root:/", 1}, {"disk_root:free space", 2}}
		if got := vs.values; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("values = %v, want %v", got, want)
		}
	})
	t.Run("empty and unreportable labels are skipped", func(t *testing.T) {
		vs := newValueSet("c")
		vs.add("", 1)
		vs.add("tab\there", 2)
		vs.add("bad\xffutf8", 3)
		vs.add(strings.Repeat("l", checks.MaxItemLen), 4) // "c:" pushes it over the bound
		vs.add("ok", 5)
		want := []itemValue{{"c:ok", 5}}
		if got := vs.values; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("values = %v, want %v", got, want)
		}
	})
	t.Run("a repeated label keeps its first value", func(t *testing.T) {
		vs := newValueSet("c")
		vs.add("a", 1)
		vs.add("a", 2)
		want := []itemValue{{"c:a", 1}}
		if got := vs.values; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("values = %v, want %v", got, want)
		}
	})
	t.Run("at most maxValues", func(t *testing.T) {
		vs := newValueSet("c")
		for i := range maxValues + 1 {
			vs.add(fmt.Sprintf("v%d", i), float64(i))
		}
		if len(vs.values) != maxValues {
			t.Fatalf("kept %d values, want %d", len(vs.values), maxValues)
		}
		if last := vs.values[maxValues-1]; last.item != fmt.Sprintf("c:v%d", maxValues-1) {
			t.Errorf("last kept value = %v, want the 64th", last)
		}
		if !vs.full() {
			t.Error("full() = false after maxValues values")
		}
	})
}
