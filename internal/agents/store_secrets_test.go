package agents_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/db"
)

// TestHeartbeatStoresSecretsWithLastSeen walks one agent through the
// heartbeat shapes against the real store: a new agent's names and state
// land with last_seen and version; zero names is an empty list, not NULL;
// the stream-open Touch leaves them alone; an old agent's heartbeat (no
// state) writes NULL — unknown — rather than keeping what a newer build
// reported; a re-registration starts unknown again.
func TestHeartbeatStoresSecretsWithLastSeen(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-secrets")
	h := createHost(t, d, org, zone, "db-1")
	if err := svc.Register(ctx, registrationOf(org, h.ID, "agent-a", "v1", "9101")); err != nil {
		t.Fatal(err)
	}
	read := func() agents.Agent {
		t.Helper()
		a, err := svc.GetByHost(db.WithOrgScope(ctx, org), org, h.ID)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	columnsNull := func() (namesNull, stateNull bool) {
		t.Helper()
		if err := d.Pool().QueryRow(ctx,
			`SELECT secret_names IS NULL, secrets_state IS NULL FROM host_agents WHERE host_id = $1`, h.ID,
		).Scan(&namesNull, &stateNull); err != nil {
			t.Fatal(err)
		}
		return namesNull, stateNull
	}

	if a := read(); a.Secrets.Known || a.Secrets.Names != nil {
		t.Fatalf("a registered agent that never reported = %+v, want unknown", a.Secrets)
	}

	if err := svc.Heartbeat(ctx, "9101", agents.HeartbeatReport{Version: "v2", SecretNames: []string{"mysql-prod", "bad name", "api"}, SecretsState: "ok"}); err != nil {
		t.Fatal(err)
	}
	a := read()
	if a.Version != "v2" || a.LastSeen == nil {
		t.Fatalf("heartbeat did not move last_seen and version: %+v", a)
	}
	if !a.Secrets.Known || a.Secrets.State != "ok" || !slices.Equal(a.Secrets.Names, []string{"api", "mysql-prod"}) {
		t.Fatalf("secrets = %+v, want known, ok, [api mysql-prod]", a.Secrets)
	}
	listed, err := svc.List(db.WithOrgScope(ctx, org), org)
	if err != nil || len(listed) != 1 || !slices.Equal(listed[0].Secrets.Names, a.Secrets.Names) {
		t.Fatalf("List = %+v, %v; want the same secrets", listed, err)
	}

	// Touch (stream open, ingest pacing) carries no report: it keeps it.
	if err := svc.Touch(ctx, "9101", "v2"); err != nil {
		t.Fatal(err)
	}
	if a := read(); !a.Secrets.Known || len(a.Secrets.Names) != 2 {
		t.Fatalf("after Touch = %+v, want the report kept", a.Secrets)
	}

	if err := svc.Heartbeat(ctx, "9101", agents.HeartbeatReport{Version: "v2", SecretsState: "agent.yaml is readable by other users"}); err != nil {
		t.Fatal(err)
	}
	if a := read(); !a.Secrets.Known || a.Secrets.Names == nil || len(a.Secrets.Names) != 0 ||
		a.Secrets.State != "agent.yaml is readable by other users" {
		t.Fatalf("zero names = %+v, want known with an empty list and the error", a.Secrets)
	}
	if namesNull, stateNull := columnsNull(); namesNull || stateNull {
		t.Fatalf("zero names stored NULL (names %v, state %v); want '{}' and the state", namesNull, stateNull)
	}

	// A state near the cap, multi-byte, survives the round trip intact.
	long := strings.Repeat("ü", agents.MaxSecretsStateBytes/2)
	if err := svc.Heartbeat(ctx, "9101", agents.HeartbeatReport{Version: "v2", SecretsState: long}); err != nil {
		t.Fatal(err)
	}
	if a := read(); a.Secrets.State != long {
		t.Fatalf("long state came back as %d bytes, want %d", len(a.Secrets.State), len(long))
	}

	// Downgraded to a build older than agent.yaml: unknown again.
	if err := svc.Heartbeat(ctx, "9101", agents.HeartbeatReport{Version: "v1", SecretsState: ""}); err != nil {
		t.Fatal(err)
	}
	if a := read(); a.Secrets.Known || a.Secrets.Names != nil || a.Version != "v1" {
		t.Fatalf("old agent = %+v (version %q), want unknown", a.Secrets, a.Version)
	}
	if namesNull, stateNull := columnsNull(); !namesNull || !stateNull {
		t.Fatalf("old agent stored names NULL=%v, state NULL=%v; want both NULL", namesNull, stateNull)
	}

	// A new registration describes a new agent: nothing reported yet.
	if err := svc.Heartbeat(ctx, "9101", agents.HeartbeatReport{Version: "v2", SecretNames: []string{"api"}, SecretsState: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Register(ctx, registrationOf(org, h.ID, "agent-a", "v2", "9102")); err != nil {
		t.Fatal(err)
	}
	if a := read(); a.Secrets.Known {
		t.Fatalf("after re-registration = %+v, want unknown", a.Secrets)
	}
}
