package logring

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestRingKeepsLastLines(t *testing.T) {
	r := New(3)
	for i := range 5 {
		_, _ = fmt.Fprintf(r, "line %d\n", i)
	}
	if got := r.Lines(); strings.Join(got, "|") != "line 2|line 3|line 4" {
		t.Fatalf("Lines = %q", got)
	}
	got := r.Lines()
	got[0] = "changed"
	if r.Lines()[0] != "line 2" {
		t.Fatal("Lines handed out the ring's own slice")
	}
}

func TestRingRedactsBeforeStoring(t *testing.T) {
	r := New(10)
	_, _ = fmt.Fprintln(r, "before hunter22")
	r.SetRedact(func(s string) string { return strings.ReplaceAll(s, "hunter22", "[secret]") })
	_, _ = fmt.Fprintln(r, "pw hunter22 end")
	got := r.Lines()
	if got[1] != "pw [secret] end" {
		t.Fatalf("Lines = %q", got)
	}
	// A line stored before the redactor was set is redacted on the way out.
	if got[0] != "before [secret]" {
		t.Fatalf("Lines = %q", got)
	}
}

func TestRingJoinsPartialWrites(t *testing.T) {
	r := New(10)
	_, _ = r.Write([]byte("par"))
	_, _ = r.Write([]byte("tial\nsecond\nthi"))
	if got := strings.Join(r.Lines(), "|"); got != "partial|second" {
		t.Fatalf("Lines = %q", got)
	}
	_, _ = r.Write([]byte("rd\n"))
	if got := strings.Join(r.Lines(), "|"); got != "partial|second|third" {
		t.Fatalf("Lines = %q", got)
	}
}

func TestRingCapsLineLength(t *testing.T) {
	r := New(10)
	long := strings.Repeat("x", 3*MaxLineBytes)
	n, err := r.Write([]byte(long + "\nshort\n"))
	if err != nil || n != len(long)+7 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	got := r.Lines()
	if len(got) != 2 || len(got[0]) > MaxLineBytes+len(truncated) || !strings.HasSuffix(got[0], truncated) || got[1] != "short" {
		t.Fatalf("Lines = %d lines, first %d bytes", len(got), len(got[0]))
	}
	// A partial line without an end is capped as well.
	_, _ = r.Write([]byte(long))
	_, _ = r.Write([]byte(long))
	_, _ = r.Write([]byte("\n"))
	if got := r.Lines(); len(got[2]) > MaxLineBytes+len(truncated) {
		t.Fatalf("partial line grew to %d bytes", len(got[2]))
	}
}

// TestRingRedactsBeforeCutting: a secret straddling the cut is replaced
// before the line is cut, so no half of it survives.
func TestRingRedactsBeforeCutting(t *testing.T) {
	r := New(10)
	r.SetRedact(func(s string) string { return strings.ReplaceAll(s, "hunter22", "[secret]") })
	line := strings.Repeat("x", MaxLineBytes-4) + "hunter22"
	_, _ = fmt.Fprintln(r, line)
	if got := r.Lines()[0]; strings.Contains(got, "hunt") {
		t.Fatalf("a part of the secret survived: %q", got[len(got)-20:])
	}
}

func TestRingConcurrentWrites(t *testing.T) {
	r := New(100)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 50 {
				_, _ = fmt.Fprintf(r, "w%d %d\n", i, j)
				_ = r.Lines()
			}
		})
	}
	wg.Wait()
	if len(r.Lines()) != 100 {
		t.Fatalf("Lines = %d", len(r.Lines()))
	}
}
