package agents_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
)

// TestHeartbeatBoundsVersion: the build version an agent reports is cleaned
// (control characters dropped, NUL included) and cut to
// agents.MaxVersionRunes on a rune boundary before it is stored, by both the
// heartbeat and the touch path; a NUL no longer fails the UPDATE.
func TestHeartbeatBoundsVersion(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", "v1.2.3", "v1.2.3"},
		{"nul dropped", "v1\x00.2", "v1.2"},
		{"controls dropped", "v\x01\x1f1\n\t.2\x7f", "v1.2"},
		{"oversize cut", strings.Repeat("a", 500), strings.Repeat("a", agents.MaxVersionRunes)},
		{"multibyte cut on rune", strings.Repeat("ü", 100), strings.Repeat("ü", agents.MaxVersionRunes)},
		{"invalid utf8 replaced", "v\xff1", "v�1"},
		{"empty", "", ""},
	}
	for i, tc := range tests {
		for _, path := range []string{"heartbeat", "touch"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				svc, _, _, _ := newService(t)
				org, host := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
				serial := "serial-ver-" + path + string(rune('a'+i))
				if err := svc.Register(ctx, reg(org, host, "agent-a", "v1", serial)); err != nil {
					t.Fatal(err)
				}
				var err error
				if path == "heartbeat" {
					err = svc.Heartbeat(ctx, serial, agents.HeartbeatReport{Version: tc.in, SecretsState: "ok"})
				} else {
					err = svc.Touch(ctx, serial, tc.in)
				}
				if err != nil {
					t.Fatal(err)
				}
				got, err := svc.GetByHost(ctx, org, host)
				if err != nil {
					t.Fatal(err)
				}
				if got.Version != tc.want || !utf8.ValidString(got.Version) {
					t.Fatalf("stored version = %q, want %q", got.Version, tc.want)
				}
			})
		}
	}
}

// TestHeartbeatQueryWritesReportsOnlyOnChange guards backlog 3.64: the
// heartbeat statement must keep the IS DISTINCT FROM guards on the two
// report columns (it keeps the old TOAST pointer for an unchanged report).
// The saving is not observable through values, so the query text is checked.
func TestHeartbeatQueryWritesReportsOnlyOnChange(t *testing.T) {
	raw, err := os.ReadFile("../db/queries/agents.sql")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)-- name: RecordAgentHeartbeat :exec(.*?)-- name:`).FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("RecordAgentHeartbeat not found")
	}
	q := m[1]
	for _, col := range []string{"local_checks", "local_check_problems"} {
		re := regexp.MustCompile(`(?s)` + col + ` = CASE WHEN ` + col + ` IS DISTINCT FROM`)
		if !re.MatchString(q) {
			t.Errorf("RecordAgentHeartbeat writes %s unconditionally; want CASE WHEN %s IS DISTINCT FROM ...", col, col)
		}
	}
}

// TestAuthQueriesSelectIdentityOnly guards backlog 3.61: the authentication
// lookup and the adoption lock read no report columns.
func TestAuthQueriesSelectIdentityOnly(t *testing.T) {
	raw, err := os.ReadFile("../db/queries/agents.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"LookupAgentSerial", "LockHostAgentsByName"} {
		m := regexp.MustCompile(`(?s)-- name: ` + name + ` :\w+(.*?)(?:\n-- name:|$)`).FindStringSubmatch(string(raw))
		if m == nil {
			t.Fatalf("%s not found", name)
		}
		body := regexp.MustCompile(`(?m)^--.*$`).ReplaceAllString(m[1], "")
		if strings.Contains(body, "sqlc.embed") || strings.Contains(body, "SELECT *") || strings.Contains(body, "h.*") {
			t.Errorf("%s selects whole rows; want the identity columns only", name)
		}
	}
}
