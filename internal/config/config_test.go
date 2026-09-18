package config_test

import (
	"strings"
	"testing"
	"time"

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

// TestCAListenHostFallback (Task 17): CAListenHost prefers
// PULSIX_GRPC_SERVER_NAME, then GRPCHost's derived host, then the
// package's own default — a bind-all listener like ":8444" must still
// produce a usable SAN name, not an empty one.
func TestCAListenHostFallback(t *testing.T) {
	base := map[string]string{
		"PULSIX_DATABASE_URL": "postgres://u:p@db/pulsix",
		"PULSIX_MASTER_KEY":   strings.Repeat("ab", 32),
		"PULSIX_PUBLIC_URL":   "https://pulsix.example.test",
	}
	withKeys := func(extra map[string]string) map[string]string {
		m := make(map[string]string, len(base)+len(extra))
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	cases := []struct {
		name  string
		extra map[string]string
		want  string
	}{
		{"bind-all listener falls back to the package default", map[string]string{"PULSIX_GRPC_LISTEN": ":8444"}, "pulsix-manager"},
		{"explicit host wins over the default", map[string]string{"PULSIX_GRPC_LISTEN": "manager.example.test:8444"}, "manager.example.test"},
		{"PULSIX_GRPC_SERVER_NAME overrides even an explicit host", map[string]string{
			"PULSIX_GRPC_LISTEN": "manager.example.test:8444", "PULSIX_GRPC_SERVER_NAME": "pulsix.internal",
		}, "pulsix.internal"},
		{"PULSIX_GRPC_SERVER_NAME alone, no listener host", map[string]string{
			"PULSIX_GRPC_LISTEN": ":8444", "PULSIX_GRPC_SERVER_NAME": "pulsix.internal",
		}, "pulsix.internal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(env(withKeys(tc.extra)))
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.CAListenHost(); got != tc.want {
				t.Errorf("CAListenHost() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEmbeddedWorkerRequiresGRPCListen (Task 17): PULSIX_EMBEDDED_WORKER
// is only meaningful once the gRPC endpoint the embedded worker dials
// actually exists — enabling it without PULSIX_GRPC_LISTEN is a
// configuration error, not a silent no-op.
func TestEmbeddedWorkerRequiresGRPCListen(t *testing.T) {
	base := map[string]string{
		"PULSIX_DATABASE_URL":    "postgres://u:p@db/pulsix",
		"PULSIX_MASTER_KEY":      strings.Repeat("ab", 32),
		"PULSIX_PUBLIC_URL":      "https://pulsix.example.test",
		"PULSIX_EMBEDDED_WORKER": "1",
	}
	if _, err := config.Load(env(base)); err == nil {
		t.Fatal("PULSIX_EMBEDDED_WORKER without PULSIX_GRPC_LISTEN must fail")
	}

	withListen := map[string]string{}
	for k, v := range base {
		withListen[k] = v
	}
	withListen["PULSIX_GRPC_LISTEN"] = ":8444"
	cfg, err := config.Load(env(withListen))
	if err != nil {
		t.Fatalf("PULSIX_EMBEDDED_WORKER with PULSIX_GRPC_LISTEN set: %v", err)
	}
	if !cfg.EmbeddedWorker {
		t.Error("EmbeddedWorker = false, want true")
	}
}

// TestEmbeddedWorkerParsesBool: an unparseable value is rejected, and the
// default (unset) is false.
func TestEmbeddedWorkerParsesBool(t *testing.T) {
	base := map[string]string{
		"PULSIX_DATABASE_URL": "postgres://u:p@db/pulsix",
		"PULSIX_MASTER_KEY":   strings.Repeat("ab", 32),
		"PULSIX_PUBLIC_URL":   "https://pulsix.example.test",
	}
	cfg, err := config.Load(env(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EmbeddedWorker {
		t.Error("EmbeddedWorker default = true, want false")
	}

	bad := map[string]string{}
	for k, v := range base {
		bad[k] = v
	}
	bad["PULSIX_EMBEDDED_WORKER"] = "not-a-bool"
	if _, err := config.Load(env(bad)); err == nil {
		t.Fatal("PULSIX_EMBEDDED_WORKER=not-a-bool must fail")
	}
}

// TestJobMaxAttempts: PULSIX_JOB_MAX_ATTEMPTS bounds how often a job is
// claimed before the reaper fails a stale claim instead of requeuing it;
// the default is 3 and only a positive integer is accepted.
func TestJobMaxAttempts(t *testing.T) {
	base := map[string]string{
		"PULSIX_DATABASE_URL": "postgres://u:p@db/pulsix",
		"PULSIX_MASTER_KEY":   strings.Repeat("ab", 32),
		"PULSIX_PUBLIC_URL":   "https://pulsix.example.test",
	}
	cases := []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{raw: "", want: 3},
		{raw: "5", want: 5},
		{raw: "1", want: 1},
		{raw: "0", wantErr: true},
		{raw: "-2", wantErr: true},
		{raw: "many", wantErr: true},
	}
	for _, c := range cases {
		m := map[string]string{"PULSIX_JOB_MAX_ATTEMPTS": c.raw}
		for k, v := range base {
			m[k] = v
		}
		cfg, err := config.Load(env(m))
		if c.wantErr {
			if err == nil {
				t.Errorf("PULSIX_JOB_MAX_ATTEMPTS=%q: expected an error", c.raw)
			}
			continue
		}
		if err != nil || cfg.JobMaxAttempts != c.want {
			t.Errorf("PULSIX_JOB_MAX_ATTEMPTS=%q: got %d, %v; want %d", c.raw, cfg.JobMaxAttempts, err, c.want)
		}
	}
}

// TestScheduleTick: PULSIX_SCHEDULE_TICK drives the schedule ticker; the
// default is 15s and anything below 5s is refused (spec §2 "Ticker").
func TestScheduleTick(t *testing.T) {
	base := map[string]string{
		"PULSIX_DATABASE_URL": "postgres://u:p@db/pulsix",
		"PULSIX_MASTER_KEY":   strings.Repeat("ab", 32),
		"PULSIX_PUBLIC_URL":   "https://pulsix.example.test",
	}
	cases := []struct {
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{raw: "", want: 15 * time.Second},
		{raw: "30s", want: 30 * time.Second},
		{raw: "5s", want: 5 * time.Second},
		{raw: "4s", wantErr: true},
		{raw: "0", wantErr: true},
		{raw: "soon", wantErr: true},
	}
	for _, c := range cases {
		m := map[string]string{"PULSIX_SCHEDULE_TICK": c.raw}
		for k, v := range base {
			m[k] = v
		}
		cfg, err := config.Load(env(m))
		if c.wantErr {
			if err == nil {
				t.Errorf("PULSIX_SCHEDULE_TICK=%q: expected an error", c.raw)
			}
			continue
		}
		if err != nil || cfg.ScheduleTick != c.want {
			t.Errorf("PULSIX_SCHEDULE_TICK=%q: got %v, %v; want %v", c.raw, cfg.ScheduleTick, err, c.want)
		}
	}
}

// TestTrustedProxies: a comma-separated CIDR list, empty by default.
func TestTrustedProxies(t *testing.T) {
	base := map[string]string{
		"PULSIX_DATABASE_URL": "postgres://u:p@db/pulsix",
		"PULSIX_MASTER_KEY":   strings.Repeat("ab", 32),
		"PULSIX_PUBLIC_URL":   "https://pulsix.example.test",
	}
	cfg, err := config.Load(env(base))
	if err != nil || len(cfg.TrustedProxies) != 0 {
		t.Fatalf("default: %v, %v", cfg.TrustedProxies, err)
	}
	base["PULSIX_TRUSTED_PROXIES"] = "172.16.0.0/12, 10.0.0.5"
	cfg, err = config.Load(env(base))
	if err != nil || len(cfg.TrustedProxies) != 2 || cfg.TrustedProxies[1].String() != "10.0.0.5/32" {
		t.Fatalf("parsed: %v, %v", cfg.TrustedProxies, err)
	}
	base["PULSIX_TRUSTED_PROXIES"] = "10.0.0.0/33"
	if _, err := config.Load(env(base)); err == nil {
		t.Fatal("an invalid CIDR must be refused")
	}
}
