//go:build linux

package raid

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileSourceMissingIsNotExist(t *testing.T) {
	_, err := fileSource{path: filepath.Join(t.TempDir(), "nope")}.Read(context.Background())
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
}

func TestFileSourceCapsRead(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mdstat")
	if err := os.WriteFile(p, []byte(strings.Repeat("a", 2<<20)), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := fileSource{path: p}.Read(context.Background())
	if err != nil || len(b) != maxMdstatLen {
		t.Fatalf("len=%d err=%v", len(b), err)
	}
}
