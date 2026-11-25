package runrequests_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/runrequests"
)

type sqlFixture struct {
	d        *db.DB
	org      uuid.UUID
	user     uuid.UUID
	template uuid.UUID
	ctx      context.Context
}

func newSQLFixture(t *testing.T, d *db.DB, slug string) sqlFixture {
	t.Helper()
	ctx := context.Background()
	q := gen.New(d.Pool())
	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	user := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: user, Email: slug + "@example.test", DisplayName: slug}); err != nil {
		t.Fatal(err)
	}
	playbook, template := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if _, err := d.Pool().Exec(ctx, `INSERT INTO playbooks (id, org_id, inline, source) VALUES ($1, $2, '- hosts: all', 'editor')`, playbook, org.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool().Exec(ctx, `INSERT INTO job_templates (id, org_id, name, playbook_id, scope) VALUES ($1, $2, 'deploy', $3, '{}')`, template, org.ID, playbook); err != nil {
		t.Fatal(err)
	}
	return sqlFixture{d: d, org: org.ID, user: user, template: template, ctx: db.WithOrgScope(ctx, org.ID)}
}

// pinA is a commit an approval pins.
const pinA = "0123456789abcdef0123456789abcdef01234567"

func TestSQLStoreRoundTripDecideRevertAndRunGroup(t *testing.T) {
	d := testdb.Open(t)
	f := newSQLFixture(t, d, "runreq-a")
	store := runrequests.NewSQLStore(d)
	tok := uuid.Must(uuid.NewV7())
	now := time.Now().UTC().Truncate(time.Microsecond)
	r := runrequests.Request{
		ID: uuid.Must(uuid.NewV7()), OrgID: f.org, Kind: runrequests.KindRun, TemplateID: f.template, TemplateRevision: 3,
		Reason: "set up", RequestedByUser: f.user, RequestedByToken: &tok, Status: runrequests.StatusPending,
		ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}
	if err := store.Insert(f.ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(f.ctx, f.org, r.ID)
	if err != nil || got.TemplateRevision != 3 || got.RequestedByToken == nil || *got.RequestedByToken != tok || got.Status != runrequests.StatusPending || !got.ExpiresAt.Equal(r.ExpiresAt) {
		t.Fatalf("Get = %+v %v", got, err)
	}
	ok, err := store.Decide(f.ctx, f.org, r.ID, runrequests.StatusApproved, f.user, now, "fp-1", pinA)
	if err != nil || !ok {
		t.Fatalf("Decide = %v %v", ok, err)
	}
	if ok, _ := store.Decide(f.ctx, f.org, r.ID, runrequests.StatusRejected, f.user, now, "", ""); ok {
		t.Fatal("a decided request was decided again")
	}
	// An approval takes the request's own id as its run group and keeps
	// the approved fingerprint.
	byGroup, err := store.ByRunGroup(f.ctx, f.org, r.ID)
	if err != nil || byGroup.ID != r.ID || byGroup.ApprovedFingerprint != "fp-1" || byGroup.RunGroupID == nil || *byGroup.RunGroupID != r.ID ||
		byGroup.PinnedCommit != pinA {
		t.Fatalf("ByRunGroup = %+v %v", byGroup, err)
	}
	if err := store.RevertToPending(f.ctx, f.org, r.ID); err != nil {
		t.Fatal(err)
	}
	// The run group stays (jobs may point at it), the fingerprint and the
	// pinned commit go.
	got, _ = store.Get(f.ctx, f.org, r.ID)
	if got.Status != runrequests.StatusPending || got.DecidedBy != nil || got.ApprovedFingerprint != "" || got.RunGroupID == nil || got.PinnedCommit != "" {
		t.Fatalf("after revert = %+v", got)
	}
	list, err := store.List(f.ctx, f.org, nil, now)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %+v %v", list, err)
	}
}

func TestSQLStoreIsolatesOrganizations(t *testing.T) {
	d := testdb.Open(t)
	a := newSQLFixture(t, d, "runreq-iso-a")
	b := newSQLFixture(t, d, "runreq-iso-b")
	store := runrequests.NewSQLStore(d)
	r := runrequests.Request{ID: uuid.Must(uuid.NewV7()), OrgID: a.org, Kind: runrequests.KindRun, TemplateID: a.template,
		Reason: "x", RequestedByUser: a.user, Status: runrequests.StatusPending, ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now()}
	if err := store.Insert(a.ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(b.ctx, a.org, r.ID); !errors.Is(err, runrequests.ErrNotFound) {
		t.Fatalf("org a's request read in org b's scope = %v, want ErrNotFound", err)
	}
	if list, err := store.List(b.ctx, b.org, nil, time.Now()); err != nil || len(list) != 0 {
		t.Fatalf("org b's list = %+v %v", list, err)
	}
}

func (f sqlFixture) insert(t *testing.T, store *runrequests.SQLStore, token *uuid.UUID, created time.Time) runrequests.Request {
	t.Helper()
	r := runrequests.Request{ID: uuid.Must(uuid.NewV7()), OrgID: f.org, Kind: runrequests.KindRun, TemplateID: f.template,
		Reason: "x", RequestedByUser: f.user, RequestedByToken: token, Status: runrequests.StatusPending,
		ExpiresAt: created.Add(runrequests.TTL), CreatedAt: created}
	if err := store.Insert(f.ctx, r); err != nil {
		t.Fatal(err)
	}
	return r
}

// TestSQLStoreFiltersStatusInTheQuery: the status filter runs before the
// list's cap of 500 rows (backlog 2.120), so the pending request behind 501
// newer decided ones is still found, and pending and expired are told
// apart on the caller's clock.
func TestSQLStoreFiltersStatusInTheQuery(t *testing.T) {
	d := testdb.Open(t)
	f := newSQLFixture(t, d, "runreq-filter")
	store := runrequests.NewSQLStore(d)
	now := time.Now().UTC().Truncate(time.Microsecond)
	expired := f.insert(t, store, nil, now.Add(-2*time.Hour))
	pending := f.insert(t, store, nil, now.Add(-time.Minute))
	if _, err := d.Pool().Exec(context.Background(), `
		INSERT INTO run_requests (id, org_id, kind, template_id, template_revision, reason, requested_by_user, status, decided_by, decided_at, expires_at, created_at)
		SELECT gen_random_uuid(), $1, 'run', $2, 1, 'x', $3, 'rejected', $3, $4::timestamptz, $4::timestamptz + interval '1 hour', $4::timestamptz + n * interval '1 millisecond'
		FROM generate_series(1, 501) AS n`, f.org, f.template, f.user, now); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		status runrequests.Status
		want   uuid.UUID
	}{{runrequests.StatusPending, pending.ID}, {runrequests.StatusExpired, expired.ID}} {
		st := tc.status
		got, err := store.List(f.ctx, f.org, &st, now)
		if err != nil || len(got) != 1 || got[0].ID != tc.want {
			t.Fatalf("%s = %d rows %v, want only %s", st, len(got), err, tc.want)
		}
	}
	st := runrequests.StatusRejected
	if got, err := store.List(f.ctx, f.org, &st, now); err != nil || len(got) != 500 {
		t.Fatalf("rejected = %d rows %v, want the cap of 500", len(got), err)
	}
	if got, err := store.List(f.ctx, f.org, nil, now); err != nil || len(got) != 500 {
		t.Fatalf("all = %d rows %v, want the cap of 500", len(got), err)
	}
}

// TestSQLStoreCountsPending: unexpired pending requests of the
// organization, of one token when it is given.
func TestSQLStoreCountsPending(t *testing.T) {
	d := testdb.Open(t)
	f := newSQLFixture(t, d, "runreq-count")
	other := newSQLFixture(t, d, "runreq-count-other")
	store := runrequests.NewSQLStore(d)
	now := time.Now().UTC().Truncate(time.Microsecond)
	tok, tok2 := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	f.insert(t, store, &tok, now.Add(-time.Minute))
	f.insert(t, store, &tok, now.Add(-time.Minute))
	f.insert(t, store, &tok2, now.Add(-time.Minute))
	f.insert(t, store, nil, now.Add(-time.Minute))
	f.insert(t, store, &tok, now.Add(-runrequests.TTL)) // expires exactly now
	decided := f.insert(t, store, &tok, now.Add(-time.Minute))
	if ok, err := store.Decide(f.ctx, f.org, decided.ID, runrequests.StatusRejected, f.user, now, "", ""); err != nil || !ok {
		t.Fatalf("Decide = %v %v", ok, err)
	}
	other.insert(t, store, &tok, now.Add(-time.Minute))
	for _, tc := range []struct {
		name  string
		token *uuid.UUID
		want  int
	}{{"organization", nil, 4}, {"token", &tok, 2}, {"other token", &tok2, 1}} {
		if n, err := store.CountPending(f.ctx, f.org, tc.token, now); err != nil || n != tc.want {
			t.Fatalf("%s = %d %v, want %d", tc.name, n, err, tc.want)
		}
	}
}

// TestSQLStoreReadsTheRequestingTokens: an organization token or a personal
// token, by id, with what makes it unable to ask any more.
func TestSQLStoreReadsTheRequestingTokens(t *testing.T) {
	d := testdb.Open(t)
	f := newSQLFixture(t, d, "runreq-tokens")
	store := runrequests.NewSQLStore(d)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Pool().Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO org_memberships (org_id, user_id, role) VALUES ($1, $2, 'operator')`, f.org, f.user)
	expiry := time.Now().UTC().Truncate(time.Microsecond).Add(time.Hour)
	id := func() uuid.UUID { return uuid.Must(uuid.NewV7()) }
	active, revoked, expiring, personal, blocked, gone := id(), id(), id(), id(), id(), id()
	orgToken := func(tid uuid.UUID, name string, revokedAt *time.Time, expiresAt *time.Time) {
		exec(`INSERT INTO api_tokens (id, user_id, org_id, name, role, secret_hash, expires_at, revoked_at) VALUES ($1, $2, $3, $4, 'operator', $5, $6, $7)`,
			tid, f.user, f.org, name, tid[:], expiresAt, revokedAt)
	}
	at := time.Now()
	orgToken(active, "org-active", nil, nil)
	orgToken(revoked, "org-revoked", &at, nil)
	orgToken(expiring, "org-expiring", nil, &expiry)
	// The blocked token belongs to another member: an admin's block is per
	// person, so it would stop every token of its owner here.
	blockedOwner := id()
	exec(`INSERT INTO users (id, email, display_name) VALUES ($1, 'runreq-tokens-2@example.test', 'b')`, blockedOwner)
	exec(`INSERT INTO org_memberships (org_id, user_id, role) VALUES ($1, $2, 'operator')`, f.org, blockedOwner)
	for tid, owner := range map[uuid.UUID]uuid.UUID{personal: f.user, blocked: blockedOwner} {
		exec(`INSERT INTO personal_tokens (id, user_id, name, secret_hash) VALUES ($1, $2, $3, $4)`, tid, owner, "personal-"+tid.String()[:8], tid[:])
	}
	exec(`INSERT INTO personal_token_grants (token_id, user_id, org_id, role) VALUES ($1, $2, $3, 'operator')`, personal, f.user, f.org)
	exec(`INSERT INTO personal_token_grants (token_id, user_id, org_id, role, blocked_at) VALUES ($1, $2, $3, 'operator', now())`, blocked, blockedOwner, f.org)

	got, err := store.Tokens(f.ctx, f.org, []uuid.UUID{active, revoked, expiring, personal, blocked, gone})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("tokens = %+v, want five (the deleted one missing)", got)
	}
	check := func(name string, tid uuid.UUID, wantName string, wantRevoked bool, wantExpiry *time.Time) {
		t.Helper()
		rec := got[tid]
		if rec.Name != wantName || rec.Revoked != wantRevoked || (wantExpiry == nil) != (rec.ExpiresAt == nil) ||
			(wantExpiry != nil && !rec.ExpiresAt.Equal(*wantExpiry)) {
			t.Fatalf("%s = %+v", name, rec)
		}
	}
	check("active", active, "org-active", false, nil)
	check("revoked", revoked, "org-revoked", true, nil)
	check("expiring", expiring, "org-expiring", false, &expiry)
	check("personal", personal, "personal-"+personal.String()[:8], false, nil)
	// A blocked grant stops the token in this organization: it reads as
	// revoked here.
	check("blocked personal", blocked, "personal-"+blocked.String()[:8], true, nil)

	// With personal tokens switched off in the organization, none can ask.
	exec(`INSERT INTO org_token_settings (org_id, personal_tokens_allowed) VALUES ($1, false)`, f.org)
	if got, err := store.Tokens(f.ctx, f.org, []uuid.UUID{personal}); err != nil || !got[personal].Revoked {
		t.Fatalf("personal token with personal tokens off = %+v %v, want revoked", got, err)
	}
	exec(`DELETE FROM org_token_settings WHERE org_id = $1`, f.org)
	// Another organization's token is not this organization's to name.
	other := newSQLFixture(t, d, "runreq-tokens-other")
	if got, err := store.Tokens(other.ctx, other.org, []uuid.UUID{active}); err != nil || len(got) != 0 {
		t.Fatalf("another organization's token = %+v %v, want none", got, err)
	}

	// A personal token that lost its grant here (the membership ended)
	// cannot ask here either.
	exec(`DELETE FROM personal_token_grants WHERE token_id = $1`, personal)
	if got, err := store.Tokens(f.ctx, f.org, []uuid.UUID{personal}); err != nil || !got[personal].Revoked {
		t.Fatalf("personal token without a grant = %+v %v, want revoked", got, err)
	}
	if got, err := store.Tokens(f.ctx, f.org, nil); err != nil || len(got) != 0 {
		t.Fatalf("no ids = %+v %v", got, err)
	}
}

// TestSQLStoreTokenRulesFollowTheOwner: the rules that stop a token at
// sign-in also stop it here (GetAPITokenByHash, GetPersonalTokenByHash,
// GetPersonalTokenGrant): a deactivated owner stops both kinds, and an
// admin's block of any one grant of the owner stops every personal token
// of theirs in the organization, even one whose own grant is not blocked.
func TestSQLStoreTokenRulesFollowTheOwner(t *testing.T) {
	d := testdb.Open(t)
	f := newSQLFixture(t, d, "runreq-owner")
	store := runrequests.NewSQLStore(d)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Pool().Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	id := func() uuid.UUID { return uuid.Must(uuid.NewV7()) }
	member := func(email string) uuid.UUID {
		u := id()
		exec(`INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'x')`, u, email)
		exec(`INSERT INTO org_memberships (org_id, user_id, role) VALUES ($1, $2, 'operator')`, f.org, u)
		return u
	}
	personalToken := func(owner uuid.UUID, blocked bool) uuid.UUID {
		tid := id()
		exec(`INSERT INTO personal_tokens (id, user_id, name, secret_hash) VALUES ($1, $2, 'p', $3)`, tid, owner, tid[:])
		exec(`INSERT INTO personal_token_grants (token_id, user_id, org_id, role, blocked_at)
			VALUES ($1, $2, $3, 'operator', CASE WHEN $4::boolean THEN now() END)`, tid, owner, f.org, blocked)
		return tid
	}
	revoked := func(name string, tid uuid.UUID) bool {
		t.Helper()
		got, err := store.Tokens(f.ctx, f.org, []uuid.UUID{tid})
		rec, ok := got[tid]
		if err != nil || !ok {
			t.Fatalf("%s = %+v %v, want a record", name, got, err)
		}
		return rec.Revoked
	}

	disabled := member("runreq-owner-disabled@example.test")
	orgTok := id()
	exec(`INSERT INTO api_tokens (id, user_id, org_id, name, role, secret_hash) VALUES ($1, $2, $3, 'o', 'operator', $4)`, orgTok, disabled, f.org, orgTok[:])
	persTok := personalToken(disabled, false)
	if revoked("org token of an active owner", orgTok) || revoked("personal token of an active owner", persTok) {
		t.Fatal("an active owner's tokens read as revoked")
	}
	exec(`UPDATE users SET disabled_at = now() WHERE id = $1`, disabled)
	if !revoked("org token of a deactivated owner", orgTok) || !revoked("personal token of a deactivated owner", persTok) {
		t.Fatal("a deactivated owner's tokens still read as able to ask")
	}

	blockedOwner := member("runreq-owner-blocked@example.test")
	open := personalToken(blockedOwner, false)
	if revoked("personal token before the block", open) {
		t.Fatal("an unblocked personal token reads as revoked")
	}
	personalToken(blockedOwner, true) // another token of the owner, blocked here
	if !revoked("personal token of a blocked owner", open) {
		t.Fatal("a personal token whose owner is blocked through another grant still reads as able to ask")
	}
}

// TestSQLStoreInsertCappedIsExact: 30 concurrent creates of one token end
// with exactly the cap inserted and the rest refused; another token and
// requests without a token are not held back by it.
func TestSQLStoreInsertCappedIsExact(t *testing.T) {
	d := testdb.Open(t)
	f := newSQLFixture(t, d, "runreq-cap")
	store := runrequests.NewSQLStore(d)
	tok, other := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	now := time.Now().UTC().Truncate(time.Microsecond)
	mk := func(token *uuid.UUID) runrequests.Request {
		return runrequests.Request{ID: uuid.Must(uuid.NewV7()), OrgID: f.org, Kind: runrequests.KindRun, TemplateID: f.template,
			Reason: "x", RequestedByUser: f.user, RequestedByToken: token, Status: runrequests.StatusPending,
			ExpiresAt: now.Add(runrequests.TTL), CreatedAt: now}
	}
	const attempts = 30
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.InsertCapped(f.ctx, mk(&tok), runrequests.MaxPendingPerToken, now)
		}()
	}
	wg.Wait()
	close(errs)
	ok, refused := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, runrequests.ErrTooManyPending):
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != runrequests.MaxPendingPerToken || refused != attempts-runrequests.MaxPendingPerToken {
		t.Fatalf("inserted %d, refused %d; want %d and %d", ok, refused, runrequests.MaxPendingPerToken, attempts-runrequests.MaxPendingPerToken)
	}
	if n, err := store.CountPending(f.ctx, f.org, &tok, now); err != nil || n != runrequests.MaxPendingPerToken {
		t.Fatalf("pending = %d %v, want %d", n, err, runrequests.MaxPendingPerToken)
	}
	if err := store.InsertCapped(f.ctx, mk(&other), runrequests.MaxPendingPerToken, now); err != nil {
		t.Fatalf("another token: %v", err)
	}
	for i := range runrequests.MaxPendingPerToken + 1 {
		if err := store.InsertCapped(f.ctx, mk(nil), runrequests.MaxPendingPerToken, now); err != nil {
			t.Fatalf("without a token, request %d: %v", i+1, err)
		}
	}
}

func TestSQLStoreKeepsTheOverrides(t *testing.T) {
	d := testdb.Open(t)
	f := newSQLFixture(t, d, "runreq-overrides")
	store := runrequests.NewSQLStore(d)
	now := time.Now().UTC().Truncate(time.Microsecond)
	host := uuid.Must(uuid.NewV7())
	limit := "web-1"
	with := runrequests.Request{
		ID: uuid.Must(uuid.NewV7()), OrgID: f.org, Kind: runrequests.KindRun, TemplateID: f.template, TemplateRevision: 1,
		Reason: "dry run", RequestedByUser: f.user, Status: runrequests.StatusPending, ExpiresAt: now.Add(5 * time.Minute), CreatedAt: now,
		Overrides: &runrequests.Overrides{Check: true, Diff: true, ExtraVars: map[string]any{"version": "1.2", "n": json.Number("3"), "big": json.Number("9007199254740993")},
			HostIDs: []uuid.UUID{host}, Limit: &limit},
	}
	without := with
	without.ID, without.Overrides = uuid.Must(uuid.NewV7()), nil
	for _, r := range []runrequests.Request{with, without} {
		if err := store.Insert(f.ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.Get(f.ctx, f.org, with.ID)
	if err != nil {
		t.Fatal(err)
	}
	ov := got.Overrides
	// Numbers come back exact (2^53+1 does not survive a float64).
	if ov == nil || !ov.Check || !ov.Diff || ov.ExtraVars["version"] != "1.2" || ov.ExtraVars["n"] != json.Number("3") ||
		ov.ExtraVars["big"] != json.Number("9007199254740993") ||
		len(ov.HostIDs) != 1 || ov.HostIDs[0] != host || ov.Limit == nil || *ov.Limit != limit || !got.ExpiresAt.Equal(with.ExpiresAt) {
		t.Fatalf("overrides = %+v (expires %v)", ov, got.ExpiresAt)
	}
	if got, err := store.Get(f.ctx, f.org, without.ID); err != nil || got.Overrides != nil {
		t.Fatalf("request without overrides = %+v %v", got.Overrides, err)
	}
	var raw *string
	if err := d.Pool().QueryRow(context.Background(), `SELECT overrides::text FROM run_requests WHERE id = $1`, without.ID).Scan(&raw); err != nil || raw != nil {
		t.Fatalf("stored overrides of a plain request = %v %v, want NULL", raw, err)
	}
}
