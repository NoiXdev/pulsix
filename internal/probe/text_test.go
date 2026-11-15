package probe_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/probe"
)

func TestBoundsMatchTheManagersContract(t *testing.T) {
	if probe.MaxItemLen != checks.MaxItemLen || probe.MaxLabelLen != checks.MaxLabelLen {
		t.Fatalf("item/label bounds %d/%d, the manager's contract says %d/%d", probe.MaxItemLen, probe.MaxLabelLen, checks.MaxItemLen, checks.MaxLabelLen)
	}
}

func TestCleanText(t *testing.T) {
	for in, want := range map[string]string{
		"eth0":           "eth0",
		"  Gi1/0/1 \x00": "Gi1/0/1",
		"a\tb":           "a b",
		"a\r\nb\x1b[31m": "ab[31m",
		"bad\xffutf8":    "bad�utf8",
		"line sep ":      "linesep",
		"\x00\x01":       "",
	} {
		if got := probe.CleanText(in); got != want {
			t.Errorf("CleanText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLabelIsBoundedOnARuneBoundary(t *testing.T) {
	long := strings.Repeat("ä", 400) // 800 bytes
	got := probe.Label(long)
	if len(got) > probe.MaxLabelLen || !utf8.ValidString(got) || len(got) < probe.MaxLabelLen-1 {
		t.Fatalf("label of 800 bytes: %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
	if got := probe.Label("\x00\x00"); got != probe.EmptyText {
		t.Fatalf("label of control characters only = %q, want %q", got, probe.EmptyText)
	}
}

func TestReportableItem(t *testing.T) {
	for item, want := range map[string]bool{
		"eth0":                   true,
		"":                       false,
		strings.Repeat("x", 512): true,
		strings.Repeat("x", 513): false,
		"bad\xff":                false,
		"ctl\x07":                false,
		"sep ":                   false,
	} {
		if got := probe.ReportableItem(item); got != want {
			t.Errorf("ReportableItem(%q) = %v, want %v", item, got, want)
		}
	}
}
