package config_test

import (
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/config"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadValid(t *testing.T) {
	cfg, err := config.Load(env(map[string]string{
		"PULSIX_DATABASE_URL": "postgres://u:p@db/pulsix",
		"PULSIX_MASTER_KEY":   strings.Repeat("ab", 32),
		"PULSIX_PUBLIC_URL":   "https://pulsix.example.test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":8080" {
		t.Errorf("Listen default = %q", cfg.Listen)
	}
	if cfg.MasterKey[0] != 0xab || cfg.MasterKey[31] != 0xab {
		t.Errorf("master key not decoded: %x", cfg.MasterKey)
	}
}

func TestRPIDAndOriginDeriveFromPublicURL(t *testing.T) {
	cfg, err := config.Load(env(map[string]string{
		"PULSIX_DATABASE_URL": "postgres://u:p@db/pulsix",
		"PULSIX_MASTER_KEY":   strings.Repeat("ab", 32),
		"PULSIX_PUBLIC_URL":   "https://pulsix.example.test:8443",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.RPID(); got != "pulsix.example.test" {
		t.Errorf("RPID() = %q, want %q (no scheme, no port)", got, "pulsix.example.test")
	}
	if got := cfg.Origin(); got != "https://pulsix.example.test:8443" {
		t.Errorf("Origin() = %q, want %q", got, "https://pulsix.example.test:8443")
	}
}

func TestLoadRejectsMissingAndBadValues(t *testing.T) {
	cases := map[string]map[string]string{
		"missing db": {"PULSIX_MASTER_KEY": strings.Repeat("ab", 32), "PULSIX_PUBLIC_URL": "https://x"},
		"short key":  {"PULSIX_DATABASE_URL": "postgres://x", "PULSIX_MASTER_KEY": "abcd", "PULSIX_PUBLIC_URL": "https://x"},
		"bad url":    {"PULSIX_DATABASE_URL": "postgres://x", "PULSIX_MASTER_KEY": strings.Repeat("ab", 32), "PULSIX_PUBLIC_URL": "not a url"},
	}
	for name, m := range cases {
		if _, err := config.Load(env(m)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// TestGRPCListenAndHost (Task 4): PULSIX_GRPC_LISTEN is optional (empty =
// gRPC disabled) and GRPCHost strips the port for the server certificate's
// DNS SAN.
func TestGRPCListenAndHost(t *testing.T) {
	cfg, err := config.Load(env(map[string]string{
		"PULSIX_DATABASE_URL": "postgres://u:p@db/pulsix",
		"PULSIX_MASTER_KEY":   strings.Repeat("ab", 32),
		"PULSIX_PUBLIC_URL":   "https://pulsix.example.test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GRPCListen != "" {
		t.Errorf("GRPCListen default = %q, want empty (disabled)", cfg.GRPCListen)
	}
	if cfg.GRPCHost() != "" {
		t.Errorf("GRPCHost without a listener = %q, want empty", cfg.GRPCHost())
	}

	cfg, err = config.Load(env(map[string]string{
		"PULSIX_DATABASE_URL": "postgres://u:p@db/pulsix",
		"PULSIX_MASTER_KEY":   strings.Repeat("ab", 32),
		"PULSIX_PUBLIC_URL":   "https://pulsix.example.test",
		"PULSIX_GRPC_LISTEN":  "manager.example.test:8444",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GRPCListen != "manager.example.test:8444" {
		t.Errorf("GRPCListen = %q", cfg.GRPCListen)
	}
	if got := cfg.GRPCHost(); got != "manager.example.test" {
		t.Errorf("GRPCHost = %q, want manager.example.test", got)
	}
}
