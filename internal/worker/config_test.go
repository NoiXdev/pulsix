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
	if cfg.ServerName != "" {
		t.Errorf("ServerName = %q, want empty without PULSIX_MANAGER_SERVER_NAME", cfg.ServerName)
	}
	// The helper has a default even with PlayUID off: the runner only reads
	// it when the uid is set, and an empty value there would exec "".
	if cfg.SetuidHelper != "/usr/local/bin/pulsix-setuid-exec" {
		t.Errorf("SetuidHelper = %q, want the default", cfg.SetuidHelper)
	}
}

// TestLoadServerNameOverride (Task 17): PULSIX_MANAGER_SERVER_NAME is
// optional and, when set, lands on Config.ServerName verbatim — Dial uses
// it instead of deriving the TLS ServerName from ManagerAddr's host (see
// client.go's dial and Config.ServerName's own doc comment for why the two
// legitimately differ for a loopback dial to a bind-all listener).
func TestLoadServerNameOverride(t *testing.T) {
	cfg, err := Load([]string{
		"PULSIX_MANAGER_ADDR=127.0.0.1:8444",
		"PULSIX_WORKER_CERT=/etc/pulsix/cert.pem",
		"PULSIX_WORKER_KEY=/etc/pulsix/key.pem",
		"PULSIX_CA_CERT=/etc/pulsix/ca.pem",
		"PULSIX_MANAGER_SERVER_NAME=pulsix-manager",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerName != "pulsix-manager" {
		t.Errorf("ServerName = %q, want pulsix-manager", cfg.ServerName)
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
		"PULSIX_WORKER_SETUID_HELPER=/opt/pulsix/setuid-exec",
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
	// A worker outside the Pulsix image ships its own setuid helper, so the
	// path has to be configurable rather than compiled into the worker.
	if cfg.SetuidHelper != "/opt/pulsix/setuid-exec" {
		t.Errorf("SetuidHelper = %q", cfg.SetuidHelper)
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

// TestLoadIdentityDirAndToken: without the three bundle paths the worker
// uses its identity directory, and PULSIX_ENROLL_TOKEN is read trimmed.
func TestLoadIdentityDirAndToken(t *testing.T) {
	cfg, err := Load([]string{"PULSIX_MANAGER_ADDR=manager:8444", "PULSIX_WORKER_DATA_DIR=/data", "PULSIX_ENROLL_TOKEN= pxw1.a.b.c \n"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IdentityDir != "/data/identity" || cfg.CertPath != "/data/identity/cert.pem" || cfg.KeyPath != "/data/identity/key.pem" ||
		cfg.CAPath != "/data/identity/ca.pem" || cfg.EnrollToken != "pxw1.a.b.c" {
		t.Fatalf("cfg = %+v", cfg)
	}
	cfg, err = Load([]string{"PULSIX_MANAGER_ADDR=manager:8444", "PULSIX_WORKER_IDENTITY_DIR=/id"})
	if err != nil || cfg.CertPath != "/id/cert.pem" {
		t.Fatalf("identity dir override = %+v, %v", cfg, err)
	}
	if _, err := Load([]string{"PULSIX_MANAGER_ADDR=manager:8444", "PULSIX_WORKER_CERT=/c.pem"}); err == nil || !strings.Contains(err.Error(), "PULSIX_WORKER_KEY") {
		t.Fatalf("a partial bundle must name the missing path: %v", err)
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
