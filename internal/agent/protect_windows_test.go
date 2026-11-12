//go:build windows

package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWindowsProtector runs against a real temp directory on the Windows
// CI job: a fresh directory inherits a loose list, Protect replaces it, and
// a file created inside inherits only SYSTEM and Administrators.
func TestWindowsProtector(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Pulsix")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := newProtector()

	found, err := p.Loose(dir)
	if err != nil {
		t.Fatalf("Loose(fresh dir) = %v", err)
	}
	if found == "" {
		t.Fatal("Loose(fresh dir) = \"\", want the inherited list reported")
	}
	t.Logf("fresh dir is accessible to: %s", found)

	if err := p.Protect(dir); err != nil {
		t.Fatalf("Protect = %v", err)
	}
	if found, err := p.Loose(dir); err != nil || found != "" {
		t.Fatalf("Loose(protected dir) = %q, %v; want empty", found, err)
	}

	file := filepath.Join(dir, "agent.key")
	if err := os.WriteFile(file, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if found, err := p.Loose(file); err != nil || found != "" {
		t.Fatalf("Loose(file in protected dir) = %q, %v; want empty", found, err)
	}
	if err := CheckEnvFile(p, file); err != nil {
		t.Fatalf("CheckEnvFile(file in protected dir) = %v", err)
	}
}
