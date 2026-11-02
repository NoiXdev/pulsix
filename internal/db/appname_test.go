package db_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/noixdev/pulsix/internal/db"
)

func TestWithApplicationName(t *testing.T) {
	for _, tc := range []struct {
		name, in string
	}{
		{"url", "postgres://u:p@db.example:5432/pulsix?sslmode=disable"},
		{"url replacing one", "postgresql://u:p@db.example/pulsix?application_name=other&sslmode=disable"},
		{"keyword value", "host=db.example user=u password=p dbname=pulsix sslmode=disable"},
		{"keyword value replacing one", "host=db.example application_name=other dbname=pulsix"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := db.WithApplicationName(tc.in, "pulsix-manager")
			if err != nil {
				t.Fatalf("WithApplicationName: %v", err)
			}
			cfg, err := pgconn.ParseConfig(out)
			if err != nil {
				t.Fatalf("parse %q: %v", out, err)
			}
			if got := cfg.RuntimeParams["application_name"]; got != "pulsix-manager" {
				t.Errorf("application_name = %q, want pulsix-manager (from %q)", got, out)
			}
			if cfg.Host != "db.example" || cfg.Database != "pulsix" {
				t.Errorf("host %q, database %q: the rest of the connection string changed (%q)", cfg.Host, cfg.Database, out)
			}
		})
	}
}

func TestWithApplicationNameQuotesKeywordValues(t *testing.T) {
	out, err := db.WithApplicationName("host=db.example dbname=pulsix", `it's a \ name`)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := pgconn.ParseConfig(out)
	if err != nil {
		t.Fatalf("parse %q: %v", out, err)
	}
	if got := cfg.RuntimeParams["application_name"]; got != `it's a \ name` {
		t.Errorf("application_name = %q", got)
	}
}

func TestWithApplicationNameRejectsInvalid(t *testing.T) {
	for _, in := range []string{"postgres://u@db.example:notaport/x", "host=db.example port=notaport"} {
		if _, err := db.WithApplicationName(in, "x"); err == nil {
			t.Errorf("WithApplicationName(%q) succeeded, want an error", in)
		}
	}
}
