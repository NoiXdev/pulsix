// Load: the worker binary's environment contract. The names must match
// `pulsix-manager worker add`'s usage note (internal/app/workeradd.go) —
// that output is the enrollment contract, so its strings win.
package worker

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load([]string{
		"PULSIX_MANAGER_ADDR=manager.example.test:8444",
		"PULSIX_WORKER_CERT=/etc/pulsix/cert.pem",
		"PULSIX_WORKER_KEY=/etc/pulsix/key.pem",
		"PULSIX_CA_CERT=/etc/pulsix/ca.pem",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ManagerAddr != "manager.example.test:8444" {
		t.Errorf("ManagerAddr = %q", cfg.ManagerAddr)
	}
	if cfg.CertPath != "/etc/pulsix/cert.pem" || cfg.KeyPath != "/etc/pulsix/key.pem" || cfg.CAPath != "/etc/pulsix/ca.pem" {
		t.Errorf("cert paths = %q, %q, %q", cfg.CertPath, cfg.KeyPath, cfg.CAPath)
	}
	if cfg.DataDir != "/var/lib/pulsix-worker" {
		t.Errorf("DataDir = %q, want the default", cfg.DataDir)
	}
	if cfg.AnsibleRunnerBin != "ansible-runner" {
		t.Errorf("AnsibleRunnerBin = %q, want the default", cfg.AnsibleRunnerBin)
	}
	if cfg.AnsibleBin != "ansible-playbook" {
		t.Errorf("AnsibleBin = %q, want the default", cfg.AnsibleBin)
	}
	if cfg.ClaimInterval != 3*time.Second {
		t.Errorf("ClaimInterval = %s, want the default", cfg.ClaimInterval)
	}
	if cfg.MaxConcurrent != 1 {
		t.Errorf("MaxConcurrent = %d, want the default 1", cfg.MaxConcurrent)
	}
	if cfg.Version != "" {
		t.Errorf("Version = %q, must not come from the environment", cfg.Version)
	}
	if len(cfg.Zones) != 0 {
		t.Errorf("Zones = %v, want none without PULSIX_WORKER_ZONES", cfg.Zones)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load([]string{
		"PULSIX_MANAGER_ADDR=manager.example.test:8444",
		"PULSIX_WORKER_CERT=/c.pem",
		"PULSIX_WORKER_KEY=/k.pem",
		"PULSIX_CA_CERT=/ca.pem",
		"PULSIX_WORKER_DATA_DIR=/data/worker",
		"PULSIX_WORKER_ZONES=018f4e2a-7b3c-7d4e-8f90-1a2b3c4d5e61 , 018f4e2a-7b3c-7d4e-8f90-1a2b3c4d5e62",
		"PULSIX_ANSIBLE_RUNNER_BIN=/usr/local/bin/ansible-runner",
		"PULSIX_ANSIBLE_BIN=/usr/local/bin/ansible-playbook",
		"PULSIX_CLAIM_INTERVAL=250ms",
		"PULSIX_MAX_CONCURRENT=1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "/data/worker" {
		t.Errorf("DataDir = %q", cfg.DataDir)
	}
	if cfg.AnsibleRunnerBin != "/usr/local/bin/ansible-runner" {
		t.Errorf("AnsibleRunnerBin = %q", cfg.AnsibleRunnerBin)
	}
	if cfg.AnsibleBin != "/usr/local/bin/ansible-playbook" {
		t.Errorf("AnsibleBin = %q", cfg.AnsibleBin)
	}
	if cfg.ClaimInterval != 250*time.Millisecond {
		t.Errorf("ClaimInterval = %s", cfg.ClaimInterval)
	}
	if cfg.MaxConcurrent != 1 {
		t.Errorf("MaxConcurrent = %d", cfg.MaxConcurrent)
	}
	want := 2
	if len(cfg.Zones) != want {
		t.Fatalf("Zones = %v (%d), want %d", cfg.Zones, len(cfg.Zones), want)
	}
	if cfg.Zones[0] != mustZone(t, "018f4e2a-7b3c-7d4e-8f90-1a2b3c4d5e61") {
		t.Errorf("Zones[0] = %s", cfg.Zones[0])
	}
	if cfg.Zones[1] != mustZone(t, "018f4e2a-7b3c-7d4e-8f90-1a2b3c4d5e62") {
		t.Errorf("Zones[1] = %s", cfg.Zones[1])
	}
}

func TestLoadErrors(t *testing.T) {
	cases := []struct {
		name   string
		env    map[string]string
		inName string // error text must name this value
	}{
		{
			name: "missing manager addr",
			env: map[string]string{
				"PULSIX_WORKER_CERT": "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
			},
			inName: "PULSIX_MANAGER_ADDR",
		},
		{
			name: "empty manager addr",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
			},
			inName: "PULSIX_MANAGER_ADDR",
		},
		{
			name: "http scheme addr",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "http://manager.example.test:8444",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
			},
			inName: "http://manager.example.test:8444",
		},
		{
			name: "grpc scheme addr",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "grpcs://manager.example.test:8444",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
			},
			inName: "grpcs://manager.example.test:8444",
		},
		{
			name: "addr without port",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "manager.example.test",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
			},
			inName: "manager.example.test",
		},
		{
			name: "missing cert path",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "manager.example.test:8444",
				"PULSIX_WORKER_KEY":   "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
			},
			inName: "PULSIX_WORKER_CERT",
		},
		{
			name: "missing key path",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "manager.example.test:8444",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_CA_CERT": "/ca.pem",
			},
			inName: "PULSIX_WORKER_KEY",
		},
		{
			name: "missing CA path",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "manager.example.test:8444",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem",
			},
			inName: "PULSIX_CA_CERT",
		},
		{
			name: "invalid zone id",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "manager.example.test:8444",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
				"PULSIX_WORKER_ZONES": "not-a-uuid",
			},
			inName: "not-a-uuid",
		},
		{
			name: "second zone invalid",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "manager.example.test:8444",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
				"PULSIX_WORKER_ZONES": "018f4e2a-7b3c-7d4e-8f90-1a2b3c4d5e61,broken",
			},
			inName: "broken",
		},
		{
			name: "invalid claim interval",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "manager.example.test:8444",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
				"PULSIX_CLAIM_INTERVAL": "soon",
			},
			inName: "PULSIX_CLAIM_INTERVAL",
		},
		{
			name: "negative claim interval",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "manager.example.test:8444",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
				"PULSIX_CLAIM_INTERVAL": "-5s",
			},
			inName: "PULSIX_CLAIM_INTERVAL",
		},
		{
			name: "invalid max concurrent",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "manager.example.test:8444",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
				"PULSIX_MAX_CONCURRENT": "many",
			},
			inName: "PULSIX_MAX_CONCURRENT",
		},
		{
			name: "zero max concurrent",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "manager.example.test:8444",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
				"PULSIX_MAX_CONCURRENT": "0",
			},
			inName: "PULSIX_MAX_CONCURRENT",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(envSlice(tc.env))
			if err == nil {
				t.Fatal("Load must fail")
			}
			if !strings.Contains(err.Error(), tc.inName) {
				t.Fatalf("error %q must name %q", err, tc.inName)
			}
		})
	}
}

// TestLoadIgnoresAliasedEnv pins that only the exact PULSIX_* names above
// are honored: manager-style listen variables and near-misses must change
// nothing (they belong to the manager process, not the worker).
func TestLoadIgnoresAliasedEnv(t *testing.T) {
	cfg, err := Load([]string{
		"PULSIX_MANAGER_ADDR=manager.example.test:8444",
		"PULSIX_WORKER_CERT=/c.pem",
		"PULSIX_WORKER_KEY=/k.pem",
		"PULSIX_CA_CERT=/ca.pem",
		"PULSIX_LISTEN=:9090",
		"PULSIX_GRPC_LISTEN=:8444",
		"PULSIX_DATABASE_URL=postgres://ignored",
		"PULSIX_WORKER_ADDR=other:9999",
		"PULSIX_DATA_DIR=/elsewhere",
		"PULSIX_ZONES=018f4e2a-7b3c-7d4e-8f90-1a2b3c4d5e61",
		"PULSIX_CLAIM_INTERVAL=1h", // exact name is honored; the alias below is not
		"PULSIX_INTERVAL=1h",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ManagerAddr != "manager.example.test:8444" {
		t.Errorf("ManagerAddr = %q, must ignore PULSIX_LISTEN/GRPC_LISTEN/WORKER_ADDR", cfg.ManagerAddr)
	}
	if cfg.DataDir != "/var/lib/pulsix-worker" {
		t.Errorf("DataDir = %q, must ignore PULSIX_DATA_DIR", cfg.DataDir)
	}
	if len(cfg.Zones) != 0 {
		t.Errorf("Zones = %v, must ignore PULSIX_ZONES", cfg.Zones)
	}
	if cfg.ClaimInterval != time.Hour {
		t.Errorf("ClaimInterval = %s, must honor PULSIX_CLAIM_INTERVAL only", cfg.ClaimInterval)
	}
}

func envSlice(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

func mustZone(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
