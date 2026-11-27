package secrets

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestRedactorReplacesLongestFirst: every value of at least four bytes is
// replaced wherever it stands; a value containing a shorter one is
// replaced whole.
func TestRedactorReplacesLongestFirst(t *testing.T) {
	r := newRedactor(map[string]map[string]string{
		"db":  {"password": "hunter22", "dsn": "postgres://app:hunter22@db/app"},
		"api": {"token": "tok-9f8e"},
	})
	tests := []struct{ in, want string }{
		{"dial postgres://app:hunter22@db/app: refused", "dial [secret]: refused"},
		{"password hunter22 wrong", "password [secret] wrong"},
		{"tok-9f8e tok-9f8e", "[secret] [secret]"},
		{"nothing here", "nothing here"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := r.Redact(tc.in); got != tc.want {
			t.Errorf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if s := fmt.Sprintf("%v %+v %#v %s", r, r, r, r); strings.Contains(s, "hunter22") {
		t.Fatalf("printed redactor shows a value: %s", s)
	}
}

// TestRedactorIgnoresShortValues: values under four bytes are left, and a
// set with none left has a nil redactor that replaces nothing.
func TestRedactorIgnoresShortValues(t *testing.T) {
	if r := newRedactor(map[string]map[string]string{"x": {"port": "543", "tls": "on"}}); r != nil {
		t.Fatalf("redactor for short values = %v", r)
	}
	var r *Redactor
	if got := r.Redact("543 on"); got != "543 on" {
		t.Fatalf("nil Redact = %q", got)
	}
	r = newRedactor(map[string]map[string]string{"x": {"port": "5432", "tls": "on"}})
	if got := r.Redact("port 5432 tls on"); got != "port [secret] tls on" {
		t.Fatalf("Redact = %q", got)
	}
}

// TestWatcherKeepsRedactorWhileRefused: the redactor follows the last file
// that was read — kept while the file is refused, dropped when it is gone.
func TestWatcherKeepsRedactorWhileRefused(t *testing.T) {
	f := newWatchFixture(t)
	if f.w.Redactor() != nil {
		t.Fatal("redactor before the first look")
	}
	f.write("secrets:\n  db:\n    password: hunter22\n")
	f.w.Current()
	if got := f.w.Redactor().Redact("pw hunter22"); got != "pw [secret]" {
		t.Fatalf("after load: %q", got)
	}
	f.advance(time.Minute)
	f.write("secrets: [broken\n")
	if _, state := f.w.Current(); state == StateOK {
		t.Fatal("broken file accepted")
	}
	if got := f.w.Redactor().Redact("pw hunter22"); got != "pw [secret]" {
		t.Fatalf("while refused: %q", got)
	}
	f.advance(time.Minute)
	f.remove()
	f.w.Current()
	if f.w.Redactor() != nil {
		t.Fatal("redactor kept after the file was removed")
	}
}
