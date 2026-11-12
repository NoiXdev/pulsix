package postgres

import (
	"os"
	"path/filepath"
	"testing"
)

// hostileEnv plants every environment variable libpq and pgx read, so a
// config that depended on any of them would differ from the baseline.
func hostileEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".pgpass"), []byte("*:*:*:*:from-pgpass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", home)
	for k, v := range map[string]string{
		"PGHOST": "env-host", "PGPORT": "1", "PGDATABASE": "env-db", "PGUSER": "env-user",
		"PGPASSWORD": "env-pass", "PGPASSFILE": filepath.Join(home, ".pgpass"), "PGAPPNAME": "env-app",
		"PGCONNECT_TIMEOUT": "99", "PGSSLMODE": "verify-full", "PGSSLKEY": "/nope.key",
		"PGSSLCERT": "/nope.crt", "PGSSLCERTMODE": "require", "PGSSLSNI": "0",
		"PGSSLROOTCERT": "/nope.pem", "PGSSLPASSWORD": "x", "PGSSLNEGOTIATION": "direct",
		"PGTARGETSESSIONATTRS": "read-write", "PGTZ": "Mars/Olympus", "PGOPTIONS": "-c work_mem=1",
		"PGMINPROTOCOLVERSION": "3.2", "PGMAXPROTOCOLVERSION": "3.2", "PGCHANNELBINDING": "require",
		"PGREQUIREAUTH": "scram-sha-256",
	} {
		t.Setenv(k, v)
	}
}

func baseSettings() connSettings {
	return connSettings{host: "db.internal", port: "5433", user: "mon", password: "pw", database: "app", sslmode: "prefer"}
}

func TestBuildConfigIgnoresTheAgentsEnvironment(t *testing.T) {
	for _, s := range []connSettings{
		baseSettings(),
		{host: "/var/run/postgresql", port: "5432", user: "mon", database: "postgres", sslmode: "prefer"},
		{host: "db", port: "5432", user: "mon", database: "postgres", sslmode: "disable"},
	} {
		clean, err := buildConfig(s)
		if err != nil {
			t.Fatal(err)
		}
		hostileEnv(t)
		hostile, err := buildConfig(s)
		if err != nil {
			t.Fatalf("with environment: %v", err)
		}
		c, h := clean.Config, hostile.Config
		if c.Host != h.Host || c.Port != h.Port || c.User != h.User || c.Password != h.Password || c.Database != h.Database ||
			c.ConnectTimeout != h.ConnectTimeout || len(c.Fallbacks) != len(h.Fallbacks) ||
			c.RequireAuth != h.RequireAuth || c.ChannelBinding != h.ChannelBinding ||
			c.MinProtocolVersion != h.MinProtocolVersion || c.MaxProtocolVersion != h.MaxProtocolVersion ||
			(c.TLSConfig == nil) != (h.TLSConfig == nil) || (c.ValidateConnect == nil) != (h.ValidateConnect == nil) ||
			c.SSLNegotiation != h.SSLNegotiation {
			t.Errorf("%+v:\nclean   %+v\nhostile %+v", s, c, h)
		}
		if c.TLSConfig != nil && (c.TLSConfig.InsecureSkipVerify != h.TLSConfig.InsecureSkipVerify || c.TLSConfig.RootCAs != nil || h.TLSConfig.RootCAs != nil || len(h.TLSConfig.Certificates) != 0) {
			t.Errorf("TLS differs: %+v vs %+v", c.TLSConfig, h.TLSConfig)
		}
		if len(h.RuntimeParams) != 2 || h.RuntimeParams["application_name"] != "pulsix-agent" || h.RuntimeParams["statement_timeout"] != "5000" {
			t.Errorf("runtime params = %v", h.RuntimeParams)
		}
		if h.Host != s.host || h.User != s.user || h.Password != s.password || h.Database != s.database {
			t.Errorf("settings not applied: %+v", h)
		}
	}
}

func TestBuildConfigQuotesEveryValue(t *testing.T) {
	// Values that would inject another keyword or end the quoting.
	nasty := `x' sslmode=disable host=evil \ y`
	s := connSettings{host: "h", port: "5432", user: nasty, password: nasty, database: nasty, sslmode: "prefer"}
	cfg, err := buildConfig(s)
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.Config
	if c.User != nasty || c.Password != nasty || c.Database != nasty || c.Host != "h" {
		t.Fatalf("value did not round-trip: %+v", c)
	}
	if c.TLSConfig == nil {
		t.Fatal("sslmode was overridden by an injected keyword")
	}
}

func TestBuildConfigNeverSurfacesTheValues(t *testing.T) {
	// An unreadable CA file makes ParseConfig fail; the error must not
	// reach anything the caller reports.
	s := baseSettings()
	s.sslmode = "verify-full"
	s.sslrootcert = "/nonexistent/" + plantedHost + ".pem"
	_, err := buildConfig(s)
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Error() != "invalid connection settings" {
		t.Fatalf("buildConfig error = %q, want the fixed text", err)
	}
}

func TestSSLModes(t *testing.T) {
	for mode, wantTLS := range map[string]bool{"disable": false, "prefer": true, "require": true, "verify-ca": true, "verify-full": true} {
		s := baseSettings()
		s.sslmode = mode
		cfg, err := buildConfig(s)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if (cfg.TLSConfig != nil) != wantTLS {
			t.Errorf("%s: TLS = %v", mode, cfg.TLSConfig != nil)
		}
	}
	s := baseSettings()
	s.sslmode = "verify-full"
	cfg, _ := buildConfig(s)
	if cfg.TLSConfig.InsecureSkipVerify || cfg.TLSConfig.ServerName != "db.internal" {
		t.Errorf("verify-full does not verify: %+v", cfg.TLSConfig)
	}
}
