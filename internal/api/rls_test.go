package api_test

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
)

// orgOwnedTables is every table that carries org_id and the
// org_isolation/org_isolation_bypass row-level-security policy pair.
var orgOwnedTables = []string{"org_memberships", "invites", "api_tokens", "audit_events", "zones", "hosts", "host_tags"}

// probeOrgTables runs, for every org-owned table, a statement with no org
// predicate at all — the shape of a forgotten WHERE org_id clause — through
// d with ctx, and returns the distinct organizations each one returned rows
// for.
func probeOrgTables(ctx context.Context, d *db.DB) (map[string][]uuid.UUID, error) {
	seen := make(map[string][]uuid.UUID, len(orgOwnedTables))
	for _, table := range orgOwnedTables {
		rows, err := d.Query(ctx, `SELECT DISTINCT org_id FROM `+table)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			seen[table] = append(seen[table], id)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return seen, nil
}

// probingHostsStore is the real hosts store plus one side effect: ListHosts
// first runs probeOrgTables with the very context the handler handed its
// store, so the test observes exactly what the database lets a real
// org-scoped request see.
type probingHostsStore struct {
	hosts.Store
	d *db.DB

	mu   sync.Mutex
	seen map[string][]uuid.UUID
	err  error
}

func (p *probingHostsStore) ListHosts(ctx context.Context, orgID uuid.UUID, f hosts.ListFilter, cursor string, limit int) ([]hosts.Host, string, error) {
	seen, err := probeOrgTables(ctx, p.d)
	p.mu.Lock()
	p.seen, p.err = seen, err
	p.mu.Unlock()
	return p.Store.ListHosts(ctx, orgID, f, cursor, limit)
}

// seedOrgRows gives orgID a row in every org-owned table the fixtures do not
// already fill (SeedOrg creates the owner membership and the default zone),
// through the raw pool where the bypass policy admits the writes.
func seedOrgRows(t *testing.T, s *apitest.Server, orgID, userID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var zoneID uuid.UUID
	if err := s.DB.Pool().QueryRow(ctx, `SELECT id FROM zones WHERE org_id = $1`, orgID).Scan(&zoneID); err != nil {
		t.Fatal(err)
	}
	hostID := uuid.Must(uuid.NewV7())
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO invites (id, org_id, email, role, token_hash, invited_by, expires_at)
		  VALUES ($1, $2, 'invitee@example.test', 'viewer', $3, $4, now() + interval '1 day')`,
			[]any{uuid.Must(uuid.NewV7()), orgID, []byte(hostID.String()), userID}},
		{`INSERT INTO api_tokens (id, user_id, org_id, name, role, secret_hash) VALUES ($1, $2, $3, 'probe', 'viewer', $4)`,
			[]any{uuid.Must(uuid.NewV7()), userID, orgID, []byte(zoneID.String() + hostID.String())}},
		{`INSERT INTO audit_events (id, org_id, actor_kind, action, object_type, object_id, summary, at)
		  VALUES ($1, $2, 'system', 'probe', 'host', 'h', 'probe', now())`, []any{uuid.Must(uuid.NewV7()), orgID}},
		{`INSERT INTO hosts (id, org_id, zone_id, name) VALUES ($1, $2, $3, 'probe-host')`, []any{hostID, orgID, zoneID}},
		{`INSERT INTO host_tags (host_id, org_id, key, value, source) VALUES ($1, $2, 'role', 'db', 'manual')`, []any{hostID, orgID}},
	} {
		if _, err := s.DB.Pool().Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

// TestOrgScopedRequestCannotSeeAnotherOrganization is the end-to-end proof
// that row-level security is live on the request path: during a real
// GET /orgs/{a}/hosts, a statement over every org-owned table that carries
// no org predicate at all returns rows of organization A only — even though
// the caller is a member of B as well, and B holds rows in every one of
// those tables. It fails if orgScopeMiddleware is taken off the org-scoped
// group (requireOrg then refuses the request), if *db.DB stops honouring
// the scope bound to the context, or if the org_isolation policies stop
// holding; the unscoped control below shows the same probe does see B when
// no organization is bound, so a pass is not vacuous.
func TestOrgScopedRequestCannotSeeAnotherOrganization(t *testing.T) {
	probe := &probingHostsStore{}
	s := apitest.New(t, apitest.WithHostsStore(func(real hosts.Store) hosts.Store {
		probe.Store = real
		return probe
	}))
	probe.d = s.DB

	owner := s.SeedUser("rls-owner@example.test", "correct-horse-battery")
	orgA := s.SeedOrg("rls-a", owner)
	orgB := s.SeedOrg("rls-b", owner)
	seedOrgRows(t, s, orgA, owner)
	seedOrgRows(t, s, orgB, owner)

	res, err := s.LoginAs(owner).Get(s.HTTP.URL + "/api/v1/orgs/rls-a/hosts")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /orgs/rls-a/hosts: status %d, want 200", res.StatusCode)
	}

	probe.mu.Lock()
	seen, probeErr := probe.seen, probe.err
	probe.mu.Unlock()
	if probeErr != nil {
		t.Fatalf("probe inside the request failed: %v", probeErr)
	}
	for _, table := range orgOwnedTables {
		if got := seen[table]; len(got) != 1 || got[0] != orgA {
			t.Errorf("%s: an unscoped statement inside a request for rls-a saw organizations %v, want only %s", table, got, orgA)
		}
	}

	control, err := probeOrgTables(context.Background(), s.DB)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range orgOwnedTables {
		if got := control[table]; len(got) != 2 {
			t.Errorf("%s: the same statement outside any request saw %v, want both %s and %s — the probe would not detect a leak", table, got, orgA, orgB)
		}
	}
}

// TestOrgScopedRequestForUnknownOrganizationSeesNothing covers the other
// binding orgScopeMiddleware can make: a slug naming no organization binds
// uuid.Nil, and the request answers 404 exactly as before.
func TestOrgScopedRequestForUnknownOrganizationSeesNothing(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("rls-unknown@example.test", "correct-horse-battery")
	s.SeedOrg("rls-known", owner)

	res, err := s.LoginAs(owner).Get(s.HTTP.URL + "/api/v1/orgs/no-such-org/hosts")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404", res.StatusCode)
	}
}
