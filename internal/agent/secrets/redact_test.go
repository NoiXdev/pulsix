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
	if r := newRedactor(map[string]map[string]string{"x": {"pin": "543", "mode": "on"}}); r != nil {
		t.Fatalf("redactor for short values = %v", r)
	}
	var r *Redactor
	if got := r.Redact("543 on"); got != "543 on" {
		t.Fatalf("nil Redact = %q", got)
	}
	r = newRedactor(map[string]map[string]string{"x": {"pin": "5432", "mode": "on"}})
	if got := r.Redact("pin 5432 mode on"); got != "pin [secret] mode on" {
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

// TestRedactorLeavesConnectionFields: the owner's allowlist (2026-11-27)
// and the plugins' other plainly non-secret fields are not replaced; every
// other field, an unknown one included, is.
func TestRedactorLeavesConnectionFields(t *testing.T) {
	plain := map[string]string{
		"host": "db.internal", "port": "5432", "user": "monitor", "username": "monitor2", "database": "appdb",
		"socket": "/run/mysqld.sock", "tls_server_name": "db.example", "tls_ca": "/etc/ca.pem", "tls_cert": "/etc/c.pem",
		"tls_key": "/etc/k.pem", "tls": "preferred", "pipe": "MySQL", "sslmode": "require",
		"sslrootcert": "/etc/root.pem", "sslcert": "/etc/pc.pem", "sslkey": "/etc/pk.pem", "basic_auth_user": "status",
	}
	secret := map[string]string{"password": "hunter22", "basic_auth_password": "s3cr3tpw", "api_key": "unknown-field-value"}
	fields := map[string]string{}
	for k, v := range plain {
		fields[k] = v
	}
	for k, v := range secret {
		fields[k] = v
	}
	r := newRedactor(map[string]map[string]string{"x": fields})
	for k, v := range plain {
		if got := r.Redact("a " + v + " b"); got != "a "+v+" b" {
			t.Errorf("non-secret field %s replaced: %q", k, got)
		}
		if IsSecretField(k) {
			t.Errorf("IsSecretField(%q) = true", k)
		}
	}
	for k, v := range secret {
		if got := r.Redact("a " + v + " b"); got != "a [secret] b" {
			t.Errorf("secret field %s not replaced: %q", k, got)
		}
	}
	if r := newRedactor(map[string]map[string]string{"x": plain}); r != nil {
		t.Fatal("a set of connection fields only has a redactor")
	}
}

// TestRedactorReplacesTheQuotedForm: a log line quotes a value holding a
// quote or a backslash (slog's text handler), and that form is replaced
// too.
func TestRedactorReplacesTheQuotedForm(t *testing.T) {
	r := newRedactor(map[string]map[string]string{"db": {"password": `Tr0ub"dor\3`}})
	for _, in := range []string{`pw Tr0ub"dor\3 x`, `advisory="pw Tr0ub\"dor\\3 x"`} {
		if got := r.Redact(in); strings.Contains(got, "Tr0ub") || !strings.Contains(got, "[secret]") {
			t.Errorf("Redact(%q) = %q", in, got)
		}
	}
}

// TestRedactTailStripsACutValue: a text cut through a secret value loses
// the value's start too; a fragment under four bytes is left.
func TestRedactTailStripsACutValue(t *testing.T) {
	r := newRedactor(map[string]map[string]string{"db": {"password": "hunter22-long"}})
	for in, want := range map[string]string{
		"dial hunter22-long: refused": "dial [secret]: refused",
		"error at hunter2":            "error at ",
		"error at hunt":               "error at ",
		"error at hun":                "error at hun",
		"no secret here":              "no secret here",
	} {
		if got := r.RedactTail(in); got != want {
			t.Errorf("RedactTail(%q) = %q, want %q", in, got, want)
		}
	}
	var nilR *Redactor
	if nilR.RedactTail("hunt") != "hunt" {
		t.Fatal("nil RedactTail changed text")
	}
}
