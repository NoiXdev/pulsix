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
