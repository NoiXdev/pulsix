package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/setup"
)

// TestSetupTokenCommand: `setup-token` prints the token and the link that
// pre-fills it, with nothing but the master key and the public URL — an
// unreachable or unset database does not stop it.
func TestSetupTokenCommand(t *testing.T) {
	var key [32]byte
	for i := range key {
		key[i] = 0xab
	}
	want := setup.Token(key)
	t.Setenv("PULSIX_PUBLIC_URL", "https://pulsix.example.com")
	t.Setenv("PULSIX_MASTER_KEY", strings.Repeat("ab", 32))
	for _, dbURL := range []string{"", "postgres://u:p@db.invalid/pulsix"} {
		t.Setenv("PULSIX_DATABASE_URL", dbURL)
		var stdout, stderr bytes.Buffer
		if code := dispatch([]string{"setup-token"}, &stdout, &stderr); code != 0 {
			t.Fatalf("setup-token (database %q): exit %d, stderr %q", dbURL, code, stderr.String())
		}
		for _, s := range []string{want, "https://pulsix.example.com/setup?token=" + want} {
			if !strings.Contains(stdout.String(), s) {
				t.Errorf("setup-token (database %q) printed %q, want %q in it", dbURL, stdout.String(), s)
			}
		}
	}
}

func TestSetupTokenCommandUsage(t *testing.T) {
	var stderr bytes.Buffer
	if code := dispatch([]string{"setup-token", "--help"}, io.Discard, &stderr); code != 0 || !strings.Contains(stderr.String(), "usage: pulsix-manager setup-token") {
		t.Errorf("setup-token --help: exit %d, stderr %q", code, stderr.String())
	}
	stderr.Reset()
	if code := dispatch([]string{"setup-token", "extra"}, io.Discard, &stderr); code != 2 {
		t.Errorf("setup-token extra: exit %d, want 2; stderr %q", code, stderr.String())
	}
	// Without a master key: a configuration failure, not a usage error.
	t.Setenv("PULSIX_PUBLIC_URL", "https://pulsix.example.com")
	t.Setenv("PULSIX_MASTER_KEY", "")
	stderr.Reset()
	if code := dispatch([]string{"setup-token"}, io.Discard, &stderr); code != 1 || !strings.Contains(stderr.String(), "PULSIX_MASTER_KEY") {
		t.Errorf("setup-token without a master key: exit %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(usageText, "pulsix-manager setup-token") {
		t.Errorf("usage text lacks setup-token:\n%s", usageText)
	}
}
