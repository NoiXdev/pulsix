//go:build !windows

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnixProtectorFileModes(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name string
		dir  bool
		mode os.FileMode
		want string
	}{
		{"owner only file", false, 0o600, ""},
		{"group readable file", false, 0o640, "mode 0640"},
		{"world writable file", false, 0o602, "mode 0602"},
		{"open directory", true, 0o755, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(root, strings.ReplaceAll(tt.name, " ", "-"))
			if tt.dir {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tt.mode); err != nil {
				t.Fatal(err)
			}
			got, err := newProtector().Loose(path)
			if err != nil {
				t.Fatalf("Loose = %v", err)
			}
			if got != tt.want {
				t.Errorf("Loose(%s) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestUnixProtectorProtect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.env")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	p := newProtector()
	if err := p.Protect(path); err != nil {
		t.Fatalf("Protect = %v", err)
	}
	if got, err := p.Loose(path); err != nil || got != "" {
		t.Errorf("Loose after Protect = %q, %v; want empty", got, err)
	}
}

func TestLoadEnvFile(t *testing.T) {
	root := t.TempDir()
	tight := filepath.Join(root, "tight.env")
	if err := os.WriteFile(tight, []byte("PULSIX_MANAGER_ADDR=m:8444\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadEnvFile(tight)
	if err != nil {
		t.Fatalf("LoadEnvFile(0600) = %v", err)
	}
	if got["PULSIX_MANAGER_ADDR"] != "m:8444" {
		t.Errorf("LoadEnvFile = %v", got)
	}

	loose := filepath.Join(root, "loose.env")
	if err := os.WriteFile(loose, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEnvFile(loose); err == nil || !strings.Contains(err.Error(), loose) || !strings.Contains(err.Error(), "mode 0644") {
		t.Errorf("LoadEnvFile(0644) = %v, want an error naming the path and the mode", err)
	}
}
