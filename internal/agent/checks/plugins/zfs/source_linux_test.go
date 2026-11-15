//go:build linux

package zfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathExists(t *testing.T) {
	dir := t.TempDir()
	if ok, err := pathExists(filepath.Join(dir, "nope")); ok || err != nil {
		t.Fatalf("missing: %v, %v", ok, err)
	}
	p := filepath.Join(dir, "zfs")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := pathExists(p); !ok || err != nil {
		t.Fatalf("present: %v, %v", ok, err)
	}
	if ok, err := pathExists(filepath.Join(p, "below-a-file")); ok || err == nil {
		t.Fatalf("not a directory: %v, %v", ok, err)
	}
}
