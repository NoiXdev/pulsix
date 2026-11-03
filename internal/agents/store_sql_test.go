package agents_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/hosts"
)

// hostServiceProvisioner adapts hosts.Service to agents.HostProvisioner —
// exactly the wiring Task 7 performs (the adapter lives there in
// production; here it proves the port is satisfiable by the real service
// and exercises the claim against the real hosts rules).
type hostServiceProvisioner struct{ svc *hosts.Service }

func (p hostServiceProvisioner) GetHost(ctx context.Context, orgID, hostID uuid.UUID) error {
	_, err := p.svc.GetHost(ctx, orgID, hostID)
	return err
}

func (p hostServiceProvisioner) CreateHost(ctx context.Context, orgID, zoneID uuid.UUID, name string) (uuid.UUID, error) {
	h, err := p.svc.CreateHost(ctx, orgID, hosts.HostInput{Name: name, ZoneID: zoneID})
	if err != nil {
		return uuid.Nil, err
	}
	return h.ID, nil
}

func (p hostServiceProvisioner) SetMonitored(ctx context.Context, orgID, hostID uuid.UUID) error {
	monitored := true
	_, err := p.svc.SetCapabilities(ctx, orgID, hostID, nil, &monitored)
	return err
}

func seedOrg(t *testing.T, q *gen.Queries, slug string) uuid.UUID {
	t.Helper()
	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

func seedZone(t *testing.T, q *gen.Queries, orgID uuid.UUID, slug string) uuid.UUID {
	t.Helper()
	zone, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return zone.ID
}

// newAgentWorld seeds one organization (with a zone), the real hosts
// service and the agents service over the real SQL store.
func newAgentWorld(t *testing.T, slug string) (*agents.Service, *agents.SQLStore, *db.DB, uuid.UUID, uuid.UUID) {
	t.Helper()
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, slug)
	zone := seedZone(t, q, org, "default")
	store := agents.NewSQLStore(d)
	prov := hostServiceProvisioner{svc: hosts.NewService(hosts.NewSQLStore(d), time.Now)}
	// A pin source, like production's CA: every created token carries the
	// fingerprint the enrolling agent pins against.
	svc := agents.NewService(store, agents.WithProvisioner(prov), agents.WithCA(pinnedCA{}))
	return svc, store, d, org, zone
}

// pinnedCA stands in for internal/ca here: these tests are about the
// registry's SQL, not about certificate issuance.
type pinnedCA struct{}

func (pinnedCA) CAPin() [32]byte {
	var pin [32]byte
	for i := range pin {
		pin[i] = byte(255 - i)
	}
	return pin
}

// createHost seeds one host through the real hosts service.
func createHost(t *testing.T, d *db.DB, orgID, zoneID uuid.UUID, name string) hosts.Host {
	t.Helper()
	h, err := hosts.NewService(hosts.NewSQLStore(d), time.Now).CreateHost(db.WithOrgScope(context.Background(), orgID), orgID, hosts.HostInput{Name: name, ZoneID: zoneID})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TestAgentTokenLifecycle walks create → claim (host created) → exhausted
// and checks the stored shape: a consumed single-use token has used_at set
// and vanishes from the pending list.
func TestAgentTokenLifecycle(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-tokens")
	tok, err := svc.CreateToken(db.WithOrgScope(ctx, org), org, agents.TokenInput{ZoneID: zone, NameHint: "web-1", MaxUses: 1, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := enrolltoken.ParseAgent(enrolltoken.AgentString(tok))
	if err != nil || parsed.ID != tok.ID || !bytes.Equal(parsed.Secret, tok.Secret) {
		t.Fatalf("token = %q, %v", enrolltoken.AgentString(tok), err)
	}
	if pending, _ := svc.ListTokens(db.WithOrgScope(ctx, org), org); len(pending) != 1 {
		t.Fatalf("pending = %v", pending)
	}

	res, err := svc.ClaimToken(ctx, org, tok, "web-1")
	if err != nil || !res.Created {
		t.Fatalf("claim = %+v, %v", res, err)
	}
	h, err := hosts.NewService(hosts.NewSQLStore(d), time.Now).GetHost(db.WithOrgScope(ctx, org), org, res.HostID)
	if err != nil {
		t.Fatalf("created host: %v", err)
	}
	if h.Name != "web-1" || h.ZoneID != zone {
		t.Fatalf("host = %+v", h)
	}
	// Single use: the claim flipped used_at; a second claim is refused and
	// the token is gone from the pending list.
	if _, err := svc.ClaimToken(ctx, org, tok, "again"); !errors.Is(err, agents.ErrTokenInvalid) {
		t.Fatalf("second claim: %v", err)
	}
	if pending, _ := svc.ListTokens(db.WithOrgScope(ctx, org), org); len(pending) != 0 {
		t.Fatalf("used token still pending: %v", pending)
	}
	var usedAt pgtype.Timestamptz
	if err := d.Pool().QueryRow(ctx, `SELECT used_at FROM agent_enrollments WHERE id = $1`, tok.ID).Scan(&usedAt); err != nil {
		t.Fatal(err)
	}
	if !usedAt.Valid {
		t.Fatal("a consumed single-use token must have used_at set")
	}
}

// TestAgentTokenStoresOnlyHash: the enrollment row keeps SHA-256(secret)
// and nothing that round-trips the secret.
func TestAgentTokenStoresOnlyHash(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-hash")
	tok, err := svc.CreateToken(db.WithOrgScope(ctx, org), org, agents.TokenInput{ZoneID: zone, NameHint: "web-2", MaxUses: 1, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	var hash []byte
	if err := d.Pool().QueryRow(ctx, `SELECT secret_hash FROM agent_enrollments WHERE id = $1`, tok.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(tok.Secret)
	if !bytes.Equal(hash, want[:]) || bytes.Equal(hash, tok.Secret) {
		t.Fatalf("stored hash = %x, secret = %x", hash, tok.Secret)
	}
}

// TestAgentTokenUnknownZone: a token whose zone does not exist in the
// organization is refused at creation (the composite foreign key), not at
// claim time.
func TestAgentTokenUnknownZone(t *testing.T) {
	svc, _, _, org, _ := newAgentWorld(t, "agent-zone")
	_, err := svc.CreateToken(db.WithOrgScope(ctx, org), org, agents.TokenInput{ZoneID: uuid.Must(uuid.NewV7()), NameHint: "x", MaxUses: 1, ExpiresAt: time.Now().Add(time.Hour)})
	if !errors.Is(err, agents.ErrInvalidInput) {
		t.Fatalf("unknown zone: %v", err)
	}
}

// TestAgentClaimBoundHost: a token bound to an existing host takes that
// host's identity (no host created); a binding to a host of another
// organization is refused.
func TestAgentClaimBoundHost(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-bound")
	h := createHost(t, d, org, zone, "web-3")
	tok, err := svc.CreateToken(db.WithOrgScope(ctx, org), org, agents.TokenInput{ZoneID: zone, HostID: &h.ID, MaxUses: 1, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.ClaimToken(ctx, org, tok, "ignored")
	if err != nil || res.Created || res.HostID != h.ID {
		t.Fatalf("bound claim = %+v, %v", res, err)
	}

	// Another organization's host is invisible to the token: the composite
	// foreign key refuses the binding at creation (a foreign id reads as
	// invalid input), the claim never runs.
	other := seedOrg(t, gen.New(d.Pool()), "agent-bound-other")
	otherZone := seedZone(t, gen.New(d.Pool()), other, "default")
	foreign := createHost(t, d, other, otherZone, "foreign")
	if _, err := svc.CreateToken(db.WithOrgScope(ctx, org), org, agents.TokenInput{ZoneID: zone, HostID: &foreign.ID, MaxUses: 1, ExpiresAt: time.Now().Add(time.Hour)}); !errors.Is(err, agents.ErrInvalidInput) {
		t.Fatalf("foreign host binding: %v", err)
	}

	// A host deleted between token creation and claim: the enrollment row
	// cascades with it (the composite FK's ON DELETE), so the claim is
	// refused like any unknown token.
	tok2, err := svc.CreateToken(db.WithOrgScope(ctx, org), org, agents.TokenInput{ZoneID: zone, HostID: &h.ID, MaxUses: 1, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool().Exec(context.Background(), `DELETE FROM hosts WHERE id = $1`, h.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ClaimToken(ctx, org, tok2, "ignored"); !errors.Is(err, agents.ErrTokenInvalid) {
		t.Fatalf("vanished bound host: %v", err)
	}
}

// TestAgentClaimNameConflict: an unbound token whose resolved name is
// taken fails the claim. The hint takes precedence over the presenter's
// name, so every claim of that token resolves to the same taken name; the
// proof that the failures consumed nothing is the row's uses count.
func TestAgentClaimNameConflict(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-conflict")
	createHost(t, d, org, zone, "taken")
	tok, err := svc.CreateToken(db.WithOrgScope(ctx, org), org, agents.TokenInput{ZoneID: zone, NameHint: "taken", MaxUses: 2, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := svc.ClaimToken(ctx, org, tok, "web-5"); !errors.Is(err, agents.ErrTokenConflict) {
			t.Fatalf("conflict claim %d: %v", i, err)
		}
	}
	var uses int
	if err := d.Pool().QueryRow(context.Background(), `SELECT uses FROM agent_enrollments WHERE id = $1`, tok.ID).Scan(&uses); err != nil {
		t.Fatal(err)
	}
	if uses != 0 {
		t.Fatalf("failed claims consumed uses: uses = %d", uses)
	}
	if pending, _ := svc.ListTokens(db.WithOrgScope(ctx, org), org); len(pending) != 1 {
		t.Fatalf("a conflicted token is still pending: %v", pending)
	}
	// A different unbound token with a free hint claims its host normally.
	tok2, err := svc.CreateToken(db.WithOrgScope(ctx, org), org, agents.TokenInput{ZoneID: zone, NameHint: "web-5", MaxUses: 1, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if res, err := svc.ClaimToken(ctx, org, tok2, "web-5"); err != nil || !res.Created {
		t.Fatalf("free-hint claim = %+v, %v", res, err)
	}
}

// TestAgentTokenUseCountRace: two claims of a one-use token race; the row
// lock plus the conditional UPDATE let exactly one through, and the loser
// rolls its host creation back.
func TestAgentTokenUseCountRace(t *testing.T) {
	svc, _, _, org, zone := newAgentWorld(t, "agent-race")
	tok, err := svc.CreateToken(db.WithOrgScope(ctx, org), org, agents.TokenInput{ZoneID: zone, MaxUses: 1, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	wins, refusals := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		name := "race-" + string(rune('a'+i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.ClaimToken(ctx, org, tok, name)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, agents.ErrTokenInvalid):
				refusals++
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 || refusals != 1 {
		t.Fatalf("wins %d, refusals %d; want exactly one claim", wins, refusals)
	}
}

// TestAgentTokenGuardByDatabaseClock: the conditional UPDATE judges the
// expiry itself, so a claim through a closure that never checks the
// expiry still cannot redeem an already-expired token, and two concurrent
// claims through such a forgetful closure cannot double-consume.
func TestAgentTokenGuardByDatabaseClock(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "agent-clock")
	zone := seedZone(t, q, org, "default")
	store := agents.NewSQLStore(d)

	forgetfulClaim := func(id uuid.UUID) error {
		return store.Claim(ctx, org, id, func(ctx context.Context, t agents.Token, _ []byte) error {
			_ = t
			return nil
		})
	}
	seedToken := func(expiresIn time.Duration) uuid.UUID {
		t.Helper()
		id := uuid.Must(uuid.NewV7())
		hash := sha256.Sum256([]byte("clock-seed"))
		if _, err := store.CreateToken(context.Background(), agents.Token{
			ID: id, OrgID: org, ZoneID: zone, NameHint: "clock", MaxUses: 1,
			ExpiresAt: time.Now().Add(expiresIn), CreatedAt: time.Now(),
		}, hash[:]); err != nil {
			t.Fatal(err)
		}
		return id
	}

	t.Run("expired by the database clock", func(t *testing.T) {
		if err := forgetfulClaim(seedToken(-time.Minute)); !errors.Is(err, agents.ErrTokenInvalid) {
			t.Fatalf("expired (forgetful closure): %v", err)
		}
	})

	t.Run("concurrent claims through a forgetful closure", func(t *testing.T) {
		id := seedToken(time.Hour)
		var mu sync.Mutex
		wins, refusals := 0, 0
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := forgetfulClaim(id)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					wins++
				case errors.Is(err, agents.ErrTokenInvalid):
					refusals++
				default:
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if wins != 1 || refusals != 1 {
			t.Fatalf("wins %d, refusals %d; want exactly one consume", wins, refusals)
		}
	})
}

// TestAgentTokenMultiUseCounts: a multi-use token is claimable exactly
// max_uses times, judged by the database's own clock.
func TestAgentTokenMultiUse(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-multi")
	tok, err := svc.CreateToken(db.WithOrgScope(ctx, org), org, agents.TokenInput{ZoneID: zone, MaxUses: 3, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	var lastUsed pgtype.Timestamptz
	for i := 0; i < 3; i++ {
		res, err := svc.ClaimToken(ctx, org, tok, "fleet-"+string(rune('a'+i)))
		if err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		if !res.Created {
			t.Fatalf("claim %d did not create its host", i)
		}
		if err := d.Pool().QueryRow(ctx, `SELECT used_at FROM agent_enrollments WHERE id = $1`, tok.ID).Scan(&lastUsed); err != nil {
			t.Fatal(err)
		}
		if i < 2 && lastUsed.Valid {
			t.Fatalf("claim %d flipped used_at on a multi-use token", i)
		}
		if i == 2 && !lastUsed.Valid {
			t.Fatal("the exhausting claim must flip used_at")
		}
	}
	if _, err := svc.ClaimToken(ctx, org, tok, "x"); !errors.Is(err, agents.ErrTokenInvalid) {
		t.Fatalf("fourth claim: %v", err)
	}
}

// TestAgentRegisterRoundTrip: register an agent, resolve it by serial
// (org-unscoped, the gRPC path), touch it, list it — then replace it
// through re-registration and verify the old serial is revoked and the row
// rewritten (one row per host).
// registrationOf is the boilerplate-free registration for tests that do
// not care about the certificate's validity window.
func registrationOf(orgID, hostID uuid.UUID, name, version, serial string) agents.Registration {
	now := time.Now()
	return agents.Registration{OrgID: orgID, HostID: hostID, AgentName: name, Version: version, Serial: serial,
		NotBefore: now, NotAfter: now.Add(90 * 24 * time.Hour)}
}

func TestAgentRegisterRoundTrip(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-registry")
	h := createHost(t, d, org, zone, "web-6")
	// The certificate's own validity window travels with the
	// registration: the Agents page shows when each agent's certificate
	// expires, and nothing else in the system knows the leaf.
	notBefore := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	notAfter := notBefore.Add(90 * 24 * time.Hour)
	if err := svc.Register(ctx, agents.Registration{
		OrgID: org, HostID: h.ID, AgentName: "agent-a", Version: "v1", Serial: "4242",
		NotBefore: notBefore, NotAfter: notAfter,
	}); err != nil {
		t.Fatal(err)
	}
	id, err := svc.GetBySerial(ctx, "4242")
	if err != nil || id.HostID != h.ID || id.OrgID != org || id.Name != "agent-a" || id.Revoked {
		t.Fatalf("identity = %+v, %v", id, err)
	}
	if err := svc.Touch(ctx, "4242", "v2"); err != nil {
		t.Fatal(err)
	}
	got, err := svc.List(db.WithOrgScope(ctx, org), org)
	if err != nil || len(got) != 1 || got[0].CertSerial != "4242" || got[0].Version != "v2" || got[0].LastSeen == nil {
		t.Fatalf("after touch = %+v, %v", got, err)
	}
	if got[0].CertNotAfter == nil || !got[0].CertNotAfter.Equal(notAfter) {
		t.Fatalf("CertNotAfter = %v, want %v", got[0].CertNotAfter, notAfter)
	}
	// The Agents page names its rows by host, so the read carries the
	// host's name rather than making the page resolve every id itself.
	if got[0].HostName != "web-6" {
		t.Errorf("HostName = %q, want %q", got[0].HostName, "web-6")
	}
	one, err := svc.GetByHost(db.WithOrgScope(ctx, org), org, h.ID)
	if err != nil || one.HostName != "web-6" || one.CertNotAfter == nil {
		t.Fatalf("GetByHost = %+v, %v", one, err)
	}

	// Replace: a re-registration revokes the old serial and rewrites the
	// row (one row per host). The old serial still RESOLVES — the join is
	// by host — and carries Revoked (the caller refuses on it).
	renewedNotAfter := notAfter.Add(90 * 24 * time.Hour)
	if err := svc.Register(ctx, agents.Registration{
		OrgID: org, HostID: h.ID, AgentName: "agent-a", Version: "v3", Serial: "4243",
		NotBefore: notAfter.Add(-time.Hour), NotAfter: renewedNotAfter,
	}); err != nil {
		t.Fatal(err)
	}
	// The replacement's window is what the page shows from then on.
	if after, err := svc.List(db.WithOrgScope(ctx, org), org); err != nil ||
		len(after) != 1 || after[0].CertNotAfter == nil || !after[0].CertNotAfter.Equal(renewedNotAfter) {
		t.Fatalf("after renewal = %+v, %v", after, err)
	}
	if id, err := svc.GetBySerial(ctx, "4242"); err != nil || !id.Revoked {
		t.Fatalf("old serial = %+v (revoked=%v), %v", id, id.Revoked, err)
	}
	if id, err := svc.GetBySerial(ctx, "4243"); err != nil || id.Version != "v3" || id.Revoked {
		t.Fatalf("new identity = %+v, %v", id, err)
	}
	var count int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM host_agents WHERE host_id = $1`, h.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("host_agents rows = %d, want 1", count)
	}
	var revoked pgtype.Timestamptz
	if err := d.Pool().QueryRow(ctx, `SELECT revoked_at FROM agent_certificates WHERE serial = '4242'`).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if !revoked.Valid {
		t.Fatal("the replaced serial must be revoked in agent_certificates")
	}
	if err := d.Pool().QueryRow(ctx, `SELECT revoked_at FROM agent_certificates WHERE serial = '4243'`).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if revoked.Valid {
		t.Fatal("the new serial must stay unrevoked")
	}
}

// TestAgentTwoOrgIsolation: agents and tokens of one organization are
// invisible to another — through the org filter in every scoped statement
// and the row-level security pairs (the catalog test pins the policies
// themselves).
func TestAgentTwoOrgIsolation(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-iso")
	q := gen.New(d.Pool())
	other := seedOrg(t, q, "agent-iso-other")
	h := createHost(t, d, org, zone, "web-7")
	if err := svc.Register(ctx, registrationOf(org, h.ID, "agent-iso", "v1", "5252")); err != nil {
		t.Fatal(err)
	}
	if tok, err := svc.CreateToken(db.WithOrgScope(ctx, org), org, agents.TokenInput{ZoneID: zone, NameHint: "iso", MaxUses: 1, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	} else if _, err := svc.ClaimToken(ctx, org, tok, "iso"); err != nil {
		t.Fatal(err)
	}
	// RevokeToken: a pending token of the org is withdrawable; a used or
	// foreign one is ErrNotFound.
	rev, err := svc.CreateToken(db.WithOrgScope(ctx, org), org, agents.TokenInput{ZoneID: zone, NameHint: "withdraw", MaxUses: 1, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeToken(db.WithOrgScope(ctx, org), org, rev.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeToken(db.WithOrgScope(ctx, org), org, rev.ID); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("revoke twice: %v", err)
	}
	if err := svc.RevokeToken(db.WithOrgScope(ctx, org), org, uuid.Must(uuid.NewV7())); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("unknown token: %v", err)
	}

	// List and revoke from the other organization see nothing.
	if got, _ := svc.List(db.WithOrgScope(ctx, other), other); len(got) != 0 {
		t.Fatalf("other org sees %v", got)
	}
	if err := svc.Revoke(db.WithOrgScope(ctx, other), other, h.ID); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("cross-org revoke: %v", err)
	}
	// Claiming org A's token under org B reads as an unknown token.
	foreignTok, err := svc.CreateToken(db.WithOrgScope(ctx, org), org, agents.TokenInput{ZoneID: zone, NameHint: "foreign", MaxUses: 1, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ClaimToken(ctx, other, foreignTok, "x"); !errors.Is(err, agents.ErrTokenInvalid) {
		t.Fatalf("cross-org claim: %v", err)
	}
	if orgID, err := svc.EnrollmentOrg(ctx, foreignTok.ID); err != nil || orgID != org {
		t.Fatalf("EnrollmentOrg = %v, %v", orgID, err)
	}
	if _, err := svc.EnrollmentOrg(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, agents.ErrTokenInvalid) {
		t.Fatalf("EnrollmentOrg unknown: %v", err)
	}
	// Registering an agent for a host of another organization is refused
	// (the composite FK to hosts (org_id, id)).
	if err := svc.Register(ctx, registrationOf(other, h.ID, "agent-iso", "v1", "5253")); err == nil {
		t.Fatal("cross-org host register accepted")
	}
	// A presented serial still resolves org-unscoped (the gRPC path) —
	// and carries the org it belongs to.
	if id, err := svc.GetBySerial(ctx, "5252"); err != nil || id.OrgID != org {
		t.Fatalf("unscoped serial = %+v, %v", id, err)
	}
}

// --- facts push and auto tags (Task 8) ----------------------------------

// factsOf reads the host's facts jsonb back as a plain map.
func factsOf(t *testing.T, d *db.DB, orgID, hostID uuid.UUID) map[string]string {
	t.Helper()
	var raw []byte
	if err := d.Pool().QueryRow(ctx,
		`SELECT facts FROM hosts WHERE id = $1 AND org_id = $2`, hostID, orgID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// tagsOf reads every tag row of the host.
func tagsOf(t *testing.T, d *db.DB, orgID, hostID uuid.UUID) map[string]string {
	t.Helper()
	rows, err := gen.New(d.Pool()).ListHostTags(ctx, gen.ListHostTagsParams{OrgID: orgID, HostID: hostID})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range rows {
		if r.Source != "fact" {
			t.Fatalf("unexpected tag source %q on (%q)", r.Source, r.Key)
		}
		out[r.Key] = r.Value
	}
	return out
}

// TestRecordFactsMergesFactsAndTags: the round trip through the real
// store — reported keys replace, OMITTED keys survive with their old
// values, genuinely new keys arrive, and the three automatic tags appear
// with source='fact'.
func TestRecordFactsMergesFactsAndTags(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-facts")
	h := createHost(t, d, org, zone, "facts-1")
	first := map[string]string{
		"os": "linux", "arch": "amd64", "virt": "kvm", "hostname": "facts-1",
	}
	if err := svc.RecordFacts(ctx, org, h.ID, first); err != nil {
		t.Fatal(err)
	}
	if got := factsOf(t, d, org, h.ID); got["os"] != "linux" || got["arch"] != "amd64" {
		t.Fatalf("first push stored %v", got)
	}
	if tags := tagsOf(t, d, org, h.ID); tags["os"] != "linux" || tags["arch"] != "amd64" || tags["virt"] != "kvm" || len(tags) != 3 {
		t.Fatalf("first push tags = %v", tags)
	}

	// The second push OMITS virt and hostname entirely (not as empty
	// strings — a full-replace implementation must fail this test) and
	// adds one genuinely new key: the changed values REPLACE, the omitted
	// keys survive with their OLD values (stale facts from an older agent
	// version stay visible), the new key lands.
	second := map[string]string{"os": "linux", "arch": "arm64", "kernel": "6.1.0-generic"}
	if err := svc.RecordFacts(ctx, org, h.ID, second); err != nil {
		t.Fatal(err)
	}
	got := factsOf(t, d, org, h.ID)
	if got["arch"] != "arm64" || got["os"] != "linux" || got["kernel"] != "6.1.0-generic" {
		t.Fatalf("second push replace/add wrong: %v", got)
	}
	if got["virt"] != "kvm" || got["hostname"] != "facts-1" {
		t.Fatalf("omitted keys must survive with their old values: %v", got)
	}
	if len(got) != 5 {
		t.Fatalf("facts after second push = %v (want os, arch, virt, hostname, kernel)", got)
	}
	// Tags: arch updates in place; virt dropped from the push stays as a
	// fact tag (the merge never deletes); kernel is stored but never
	// tagged.
	if tags := tagsOf(t, d, org, h.ID); tags["arch"] != "arm64" || tags["virt"] != "kvm" || len(tags) != 3 {
		t.Fatalf("second push tags = %v", tags)
	}
}

// TestRecordFactsManualPrecedence: a manual tag of the same key wins at
// push time (the guarded upsert writes nothing for that key), and once
// the manual tag is removed the next push re-applies the fact — the
// natural re-push semantics.
func TestRecordFactsManualPrecedence(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-tags")
	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	h := createHost(t, d, org, zone, "tags-1")
	facts := map[string]string{"os": "linux", "arch": "amd64", "virt": "kvm"}

	// The operator sets os manually BEFORE any facts push exists.
	if err := hostsSvc.SetTags(db.WithOrgScope(ctx, org), org, h.ID, []hosts.Tag{
		{Key: "os", Value: "windows"}, // the operator insists
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordFacts(ctx, org, h.ID, facts); err != nil {
		t.Fatal(err)
	}
	rows, err := gen.New(d.Pool()).ListHostTags(ctx, gen.ListHostTagsParams{OrgID: org, HostID: h.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 { // manual os + fact arch + fact virt; NO fact os
		t.Fatalf("tags after guarded push = %+v", rows)
	}
	for _, r := range rows {
		if r.Key == "os" && r.Source == "fact" {
			t.Fatal("a fact tag must not be written while a manual row holds the key")
		}
		if r.Key == "os" && (r.Source != "manual" || r.Value != "windows") {
			t.Fatalf("manual os tag = %+v", r)
		}
	}

	// The manual tag is removed (a SetTags without it): the next push
	// re-applies the fact tag — and updates arch while at it.
	if err := hostsSvc.SetTags(db.WithOrgScope(ctx, org), org, h.ID, []hosts.Tag{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordFacts(ctx, org, h.ID, map[string]string{"os": "linux", "arch": "arm64", "virt": "kvm"}); err != nil {
		t.Fatal(err)
	}
	if tags := tagsOf(t, d, org, h.ID); tags["os"] != "linux" || tags["arch"] != "arm64" || tags["virt"] != "kvm" {
		t.Fatalf("tags after manual removal + re-push = %v", tags)
	}
}

// factKeys builds n distinct contract-valid fact keys with prefix, each
// mapped to value.
func factKeys(prefix string, n int, value string) map[string]string {
	out := make(map[string]string, n)
	for i := 0; i < n; i++ {
		out[fmt.Sprintf("%s_%03d", prefix, i)] = value
	}
	return out
}

// TestUpsertHostFactsReplacesWhenMergeExceedsCap (backlog 7.56): the merge
// keeps stale keys only while the merged object stays at or under
// MaxStoredFacts; a push that would take it over the cap replaces the
// stored set with the pushed one, so a host can never accumulate keys
// across pushes without bound.
func TestUpsertHostFactsReplacesWhenMergeExceedsCap(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-facts-cap")
	h := createHost(t, d, org, zone, "facts-cap")

	// 100 stored keys, built up over two pushes that each stay under the
	// per-push bound and whose merge stays under the cap.
	for _, prefix := range []string{"a", "b"} {
		if err := svc.RecordFacts(ctx, org, h.ID, factKeys(prefix, 50, "old")); err != nil {
			t.Fatal(err)
		}
	}
	if got := factsOf(t, d, org, h.ID); len(got) != 100 {
		t.Fatalf("stored %d keys after two merging pushes, want 100", len(got))
	}

	// 28 new keys: the merge lands on exactly the cap and keeps every
	// stale key, as today.
	if err := svc.RecordFacts(ctx, org, h.ID, factKeys("c", agents.MaxStoredFacts-100, "new")); err != nil {
		t.Fatal(err)
	}
	if got := factsOf(t, d, org, h.ID); len(got) != agents.MaxStoredFacts || got["a_000"] != "old" || got["c_000"] != "new" {
		t.Fatalf("merge at the cap stored %d keys (a_000=%q, c_000=%q), want %d merged",
			len(got), got["a_000"], got["c_000"], agents.MaxStoredFacts)
	}

	// 64 different keys would take the merge to 192: the push replaces
	// the stored set instead.
	pushed := factKeys("d", agents.MaxFactsKeys, "fresh")
	if err := svc.RecordFacts(ctx, org, h.ID, pushed); err != nil {
		t.Fatal(err)
	}
	got := factsOf(t, d, org, h.ID)
	if len(got) != len(pushed) {
		t.Fatalf("over-cap merge stored %d keys, want exactly the %d pushed", len(got), len(pushed))
	}
	for k, v := range pushed {
		if got[k] != v {
			t.Fatalf("over-cap merge: key %q = %q, want %q", k, got[k], v)
		}
	}
}

// TestUpsertHostFactsReplacesAnAlreadyOversizeSet (review focus): a host
// whose stored facts are already above the cap (written before the cap
// existed) does not lose its next push — the push replaces the set.
func TestUpsertHostFactsReplacesAnAlreadyOversizeSet(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-facts-oversize")
	h := createHost(t, d, org, zone, "facts-oversize")
	legacy, err := json.Marshal(factKeys("legacy", agents.MaxStoredFacts+72, "old"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool().Exec(ctx, `UPDATE hosts SET facts = $1::jsonb WHERE id = $2`, legacy, h.ID); err != nil {
		t.Fatal(err)
	}
	if got := factsOf(t, d, org, h.ID); len(got) != agents.MaxStoredFacts+72 {
		t.Fatalf("seeded %d keys, want %d", len(got), agents.MaxStoredFacts+72)
	}

	pushed := map[string]string{"os": "linux", "arch": "amd64", "virt": "kvm"}
	if err := svc.RecordFacts(ctx, org, h.ID, pushed); err != nil {
		t.Fatal(err)
	}
	if got := factsOf(t, d, org, h.ID); len(got) != len(pushed) || got["os"] != "linux" || got["arch"] != "amd64" || got["virt"] != "kvm" {
		t.Fatalf("push over an oversize set stored %v, want exactly %v", got, pushed)
	}
	if tags := tagsOf(t, d, org, h.ID); tags["os"] != "linux" || len(tags) != 3 {
		t.Fatalf("push over an oversize set tags = %v", tags)
	}
}

// TestRecordFactsBoundsRejected: the service's sentinel rejection holds
// through the SQL world — 65 keys and a 513-char value are refused and
// store NOTHING (no partial merge, no tags).
func TestRecordFactsBoundsRejected(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-facts-bounds")
	h := createHost(t, d, org, zone, "facts-bounds")
	tooMany := factKeys("k", agents.MaxFactsKeys+1, "v") // distinct valid keys
	if err := svc.RecordFacts(ctx, org, h.ID, tooMany); !errors.Is(err, agents.ErrInvalidInput) {
		t.Fatalf("65 keys: %v", err)
	}
	if err := svc.RecordFacts(ctx, org, h.ID, map[string]string{
		"os": strings.Repeat("o", agents.MaxFactsValueLen+1),
	}); !errors.Is(err, agents.ErrInvalidInput) {
		t.Fatalf("513-char value: %v", err)
	}
	if got := factsOf(t, d, org, h.ID); len(got) != 0 {
		t.Fatalf("rejected push left facts behind: %v", got)
	}
	if tags := tagsOf(t, d, org, h.ID); len(tags) != 0 {
		t.Fatalf("rejected push left tags behind: %v", tags)
	}
}

// TestRecordFactsTwoOrgIsolation: a host of another organization is
// invisible — ErrNotFound, not a leak — and its stored facts stay
// untouched.
func TestRecordFactsTwoOrgIsolation(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-facts-iso")
	q := gen.New(d.Pool())
	other := seedOrg(t, q, "agent-facts-iso-other")
	h := createHost(t, d, org, zone, "facts-iso")
	if err := svc.RecordFacts(ctx, org, h.ID, map[string]string{"os": "linux"}); err != nil {
		t.Fatal(err)
	}
	// The other org's push for the same host id is a refusal (invisible,
	// not forbidden), with RLS beneath the org filter.
	if err := svc.RecordFacts(db.WithOrgScope(ctx, other), other, h.ID, map[string]string{"os": "evil"}); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("cross-org facts push: %v", err)
	}
	if got := factsOf(t, d, org, h.ID); got["os"] != "linux" || len(got) != 1 {
		t.Fatalf("cross-org push disturbed the facts: %v", got)
	}
	// An unknown host is the same ErrNotFound.
	if err := svc.RecordFacts(ctx, org, uuid.Must(uuid.NewV7()), map[string]string{"os": "linux"}); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("unknown host push: %v", err)
	}
}

// TestAgentRenewAfterRevokeDoesNotReRegister is backlog 7.18: Renew is
// authenticated by a live serial, but a revocation can commit between that
// check and the renewal's own transaction. Renewing then re-inserted the
// host_agents row Revoke had just deleted and issued a working serial. A
// renewal now proves inside its transaction that the serial it renews
// from is still live, and fails otherwise.
func TestAgentRenewAfterRevokeDoesNotReRegister(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-renew-revoked")
	h := createHost(t, d, org, zone, "web-7")
	if err := svc.Register(ctx, registrationOf(org, h.ID, "agent-a", "v1", "7001")); err != nil {
		t.Fatal(err)
	}

	// A live serial renews: the old one is revoked, the new one resolves.
	if err := svc.Renew(ctx, registrationOf(org, h.ID, "agent-a", "v1", "7002"), "7001"); err != nil {
		t.Fatalf("Renew from a live serial: %v", err)
	}
	if id, err := svc.GetBySerial(ctx, "7002"); err != nil || id.Revoked {
		t.Fatalf("renewed identity = %+v, %v", id, err)
	}
	// The serial it renewed from is no longer live; renewing from it again
	// fails.
	if err := svc.Renew(ctx, registrationOf(org, h.ID, "agent-a", "v1", "7003"), "7001"); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("Renew from a replaced serial: err = %v, want ErrNotFound", err)
	}

	// The race: the administrator's revoke commits first.
	if err := svc.Revoke(db.WithOrgScope(ctx, org), org, h.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Renew(ctx, registrationOf(org, h.ID, "agent-a", "v1", "7004"), "7002"); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("Renew after revoke: err = %v, want ErrNotFound", err)
	}
	var rows int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM host_agents WHERE host_id = $1`, h.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("host_agents rows after revoke and renew = %d, want 0", rows)
	}
	if _, err := svc.GetBySerial(ctx, "7004"); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("serial issued after revoke resolves: err = %v, want ErrNotFound", err)
	}
}

// TestAgentRegisterAndRenewRecordTheKey: enrollment (Register) and renewal
// (Renew) write the leaf's key fingerprint with the certificate row — what
// adoption after a restore compares against (backlog 7.47).
func TestAgentRegisterAndRenewRecordTheKey(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-keys")
	h := createHost(t, d, org, zone, "web-k")
	keyOf := func(serial string) *string {
		t.Helper()
		var key *string
		if err := d.Pool().QueryRow(ctx, `SELECT public_key_sha256 FROM agent_certificates WHERE serial = $1`, serial).Scan(&key); err != nil {
			t.Fatal(err)
		}
		return key
	}
	enrolled := registrationOf(org, h.ID, "agent-k", "v1", "5001")
	enrolled.PublicKeySHA256 = strings.Repeat("ab", 32)
	if err := svc.Register(ctx, enrolled); err != nil {
		t.Fatal(err)
	}
	if got := keyOf("5001"); got == nil || *got != enrolled.PublicKeySHA256 {
		t.Fatalf("enrolled key = %v, want %s", got, enrolled.PublicKeySHA256)
	}
	renewed := registrationOf(org, h.ID, "agent-k", "v1", "5002")
	renewed.PublicKeySHA256 = strings.Repeat("cd", 32)
	if err := svc.Renew(ctx, renewed, "5001"); err != nil {
		t.Fatal(err)
	}
	if got := keyOf("5002"); got == nil || *got != renewed.PublicKeySHA256 {
		t.Fatalf("renewed key = %v, want %s", got, renewed.PublicKeySHA256)
	}
}
