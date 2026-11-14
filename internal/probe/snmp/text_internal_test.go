package snmp

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/checks"
)

func TestBoundsMatchTheManagersContract(t *testing.T) {
	if maxItemLen != checks.MaxItemLen || maxLabelLen != checks.MaxLabelLen {
		t.Fatalf("item/label bounds %d/%d, the manager's contract says %d/%d", maxItemLen, maxLabelLen, checks.MaxItemLen, checks.MaxLabelLen)
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
		if got := cleanText(in); got != want {
			t.Errorf("cleanText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLabelIsBoundedOnARuneBoundary(t *testing.T) {
	long := strings.Repeat("ä", 400) // 800 bytes
	got := label(long)
	if len(got) > maxLabelLen || !utf8.ValidString(got) || len(got) < maxLabelLen-1 {
		t.Fatalf("label of 800 bytes: %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
	if got := label("\x00\x00"); got != emptyText {
		t.Fatalf("label of control characters only = %q, want %q", got, emptyText)
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
		if got := reportableItem(item); got != want {
			t.Errorf("reportableItem(%q) = %v, want %v", item, got, want)
		}
	}
}
