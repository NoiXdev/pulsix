// Load: the worker data directory and the characters it may hold.
package worker

import (
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
