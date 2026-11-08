// Load: the worker data directory and the characters it may hold.
package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadAllowsOnlySafeDataDirCharacters: the paths below the data dir go
// into each run's ansible.cfg and SSH arguments, where ':' splits roles_path,
// ',' splits vault_identity_list, whitespace and quotes split ssh_args, a
// line break or a comment marker ends a setting and '%' is interpolated.
func TestLoadAllowsOnlySafeDataDirCharacters(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dir     string
		refused bool
	}{
		{"colon", "/data/a:b", true},
		{"newline", "/data/a\nb", true},
		{"carriage return", "/data/a\rb", true},
		{"space", "/data/a b", true},
		{"tab", "/data/a\tb", true},
		{"single quote", "/data/a'b", true},
		{"double quote", "/data/a\"b", true},
		{"percent", "/data/a%b", true},
		{"hash", "/data/a#b", true},
		{"semicolon", "/data/a;b", true},
		{"comma", "/data/a,b", true},
		{"dollar", "/data/a$b", true},
		{"non-ASCII letter", "/data/\u00e4b", true},
		{"default", "/var/lib/pulsix-worker", false},
		{"dots, underscore, digits", "/data/worker_1.2", false},
		{"plus, at, equals", "/data/a+b@c=d", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load([]string{
				"PULSIX_MANAGER_ADDR=manager.example.test:8444",
				"PULSIX_WORKER_CERT=/c.pem",
				"PULSIX_WORKER_KEY=/k.pem",
				"PULSIX_CA_CERT=/ca.pem",
				"PULSIX_WORKER_DATA_DIR=" + tc.dir,
			})
			if !tc.refused {
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
				if cfg.DataDir != tc.dir {
					t.Errorf("DataDir = %q, want %q", cfg.DataDir, tc.dir)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "PULSIX_WORKER_DATA_DIR") {
				t.Fatalf("Load error = %v, want one naming PULSIX_WORKER_DATA_DIR", err)
			}
		})
	}
}

// TestLoadResolvesARelativeDataDir: Load resolves a relative data dir against
// the working directory once, at start, so everything derived from it (the
// identity directory, the certificate paths) and every path the runner writes
// into a run's ansible.cfg is absolute, whatever the process does later.
// It changes the working directory, so it must not run in parallel.
func TestLoadResolvesARelativeDataDir(t *testing.T) {
	t.Chdir(t.TempDir())
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(wd, "data")
	for _, raw := range []string{"data", "./x/../data"} {
		t.Run(raw, func(t *testing.T) {
			cfg, err := Load([]string{
				"PULSIX_MANAGER_ADDR=manager.example.test:8444",
				"PULSIX_WORKER_DATA_DIR=" + raw,
			})
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.DataDir != want {
				t.Errorf("DataDir = %q, want %q", cfg.DataDir, want)
			}
			if wantIdentity := filepath.Join(want, "identity"); cfg.IdentityDir != wantIdentity {
				t.Errorf("IdentityDir = %q, want %q", cfg.IdentityDir, wantIdentity)
			}
			if !strings.HasPrefix(cfg.CertPath, want+string(filepath.Separator)) {
				t.Errorf("CertPath = %q, want it below %q", cfg.CertPath, want)
			}
		})
	}
}

// TestLoadRefusesADataDirThatResolvesToAnUnsafePath: the safety check sees
// the path that is actually used, so a relative value under a working
// directory whose path holds an unsafe character is refused at start, naming
// both values, instead of failing at the first run's ansible.cfg. It changes
// the working directory, so it must not run in parallel.
func TestLoadRefusesADataDirThatResolvesToAnUnsafePath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a b")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	_, err = Load([]string{
		"PULSIX_MANAGER_ADDR=manager.example.test:8444",
		"PULSIX_WORKER_CERT=/c.pem",
		"PULSIX_WORKER_KEY=/k.pem",
		"PULSIX_CA_CERT=/ca.pem",
		"PULSIX_WORKER_DATA_DIR=data",
	})
	if err == nil {
		t.Fatal("Load accepted a data dir that resolves below a path with a space")
	}
	for _, want := range []string{"PULSIX_WORKER_DATA_DIR", `"data"`, `(resolved to "` + filepath.Join(wd, "data") + `")`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

// TestLoadRefusalOfAnAbsoluteDataDirNamesNoResolvedPath pins the existing
// message: an absolute value resolves to itself, so there is nothing to add.
func TestLoadRefusalOfAnAbsoluteDataDirNamesNoResolvedPath(t *testing.T) {
	_, err := Load([]string{
		"PULSIX_MANAGER_ADDR=manager.example.test:8444",
		"PULSIX_WORKER_DATA_DIR=/data/a b",
	})
	if err == nil {
		t.Fatal("Load accepted a data dir with a space")
	}
	if strings.Contains(err.Error(), "resolved to") {
		t.Errorf("error %q names a resolved path for an absolute value", err)
	}
}
