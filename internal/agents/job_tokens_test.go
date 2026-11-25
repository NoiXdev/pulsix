package agents_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/enrolltoken"
)

// insertRunJob seeds one run job row the job-bound tokens link to.
func insertRunJob(t *testing.T, d *db.DB, org, zone uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := d.Pool().Exec(context.Background(),
		`INSERT INTO jobs (id, org_id, zone_id, kind, run_group_id) VALUES ($1, $2, $3, 'run', $1)`, id, org, zone); err != nil {
		t.Fatal(err)
	}
	return id
}

// The tokens an enrolling run hands its hosts (spec 2026-11-25 B): one
// host-bound single-use token per target without an agent, in the job's
// zone, linked to the job, with the lifetime asked for; a target that runs
// an agent gets none. Each token is the printed agent token the role takes.
func TestMintJobTokens(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-job-tokens")
	scoped := db.WithOrgScope(ctx, org)
	web1 := createHost(t, d, org, zone, "web-1")
	web2 := createHost(t, d, org, zone, "web-2")
	withAgent := createHost(t, d, org, zone, "web-3")
	if err := svc.Register(ctx, registrationOf(org, withAgent.ID, "web-3", "v1", "9701")); err != nil {
		t.Fatal(err)
	}
	job := insertRunJob(t, d, org, zone)
	expires := time.Now().Add(24 * time.Hour).Truncate(time.Microsecond)

	got, err := svc.MintJobTokens(scoped, org, agents.JobTokensInput{
		ZoneID: zone, JobID: job, ExpiresAt: expires,
		Targets: []agents.JobTokenTarget{{HostID: web1.ID, Name: "web-1"}, {HostID: withAgent.ID, Name: "web-3"}, {HostID: web2.ID, Name: "web-2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].HostID != web1.ID || got[0].Name != "web-1" || got[1].HostID != web2.ID || got[1].Name != "web-2" {
		t.Fatalf("minted = %+v, want web-1 and web-2 in order", got)
	}
	if got[0].Token == got[1].Token {
		t.Fatal("two hosts got the same token")
	}
	pending, err := svc.ListTokens(scoped, org)
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range got {
		parsed, err := enrolltoken.ParseAgent(m.Token)
		if err != nil {
			t.Fatalf("token %d is not an agent token: %v", i, err)
		}
		idx := slices.IndexFunc(pending, func(p agents.Token) bool { return p.ID == parsed.ID })
		if idx < 0 {
			t.Fatalf("token %d is not pending", i)
		}
		p := pending[idx]
		if p.HostID == nil || *p.HostID != m.HostID || p.ZoneID != zone || p.MaxUses != 1 || p.JobID == nil || *p.JobID != job ||
			!p.ExpiresAt.Equal(expires) || p.NameHint != m.Name {
			t.Fatalf("stored token %d = %+v, want bound to %s, single use, job %s, expiring %v", i, p, m.HostID, job, expires)
		}
	}

	// The token works like one a person created for the host.
	parsed, _ := enrolltoken.ParseAgent(got[0].Token)
	res, err := svc.ClaimToken(ctx, org, parsed, "")
	if err != nil || res.HostID != web1.ID || res.Created {
		t.Fatalf("claim = %+v, %v; want the bound host", res, err)
	}
}

// RevokeJobTokens deletes the job's unused tokens and only those: a used
// one stays (consumed), and another job's or a person's token is not the
// job's to revoke.
func TestRevokeJobTokens(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-job-revoke")
	scoped := db.WithOrgScope(ctx, org)
	web1 := createHost(t, d, org, zone, "web-1")
	web2 := createHost(t, d, org, zone, "web-2")
	job, other := insertRunJob(t, d, org, zone), insertRunJob(t, d, org, zone)
	expires := time.Now().Add(24 * time.Hour)
	mint := func(jobID uuid.UUID) []agents.JobToken {
		t.Helper()
		out, err := svc.MintJobTokens(scoped, org, agents.JobTokensInput{ZoneID: zone, JobID: jobID, ExpiresAt: expires,
			Targets: []agents.JobTokenTarget{{HostID: web1.ID, Name: "web-1"}, {HostID: web2.ID, Name: "web-2"}}})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	mine := mint(job)
	mint(other)
	if _, err := svc.CreateToken(scoped, org, agents.TokenInput{ZoneID: zone, HostID: &web2.ID, MaxUses: 1, ExpiresAt: expires}); err != nil {
		t.Fatal(err)
	}
	used, _ := enrolltoken.ParseAgent(mine[0].Token)
	if _, err := svc.ClaimToken(ctx, org, used, ""); err != nil {
		t.Fatal(err)
	}

	n, err := svc.RevokeJobTokens(scoped, org, job)
	if err != nil || n != 1 {
		t.Fatalf("revoked %d, %v; want the one unused token", n, err)
	}
	pending, err := svc.ListTokens(scoped, org)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pending {
		if p.JobID != nil && *p.JobID == job {
			t.Fatalf("an unused token of the job survived: %+v", p)
		}
	}
	if len(pending) != 3 {
		t.Fatalf("pending = %d tokens, want the other job's two and the person's one", len(pending))
	}
	var usedRows int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM agent_enrollments WHERE job_id = $1 AND used_at IS NOT NULL`, job).Scan(&usedRows); err != nil || usedRows != 1 {
		t.Fatalf("used tokens of the job = %d, %v; want 1 kept", usedRows, err)
	}
	if n, err := svc.RevokeJobTokens(scoped, org, job); err != nil || n != 0 {
		t.Fatalf("second revoke = %d, %v; want nothing left", n, err)
	}
}

// A host name that is no valid token name hint mints a token without one:
// the token is bound, so the hint names nothing at the claim.
func TestMintJobTokensWithoutAHintForALongName(t *testing.T) {
	svc, _, d, org, zone := newAgentWorld(t, "agent-job-hint")
	scoped := db.WithOrgScope(ctx, org)
	h := createHost(t, d, org, zone, "web-1")
	job := insertRunJob(t, d, org, zone)
	long := "a-very-long-host-name-that-is-longer-than-sixty-four-characters-for-sure"
	got, err := svc.MintJobTokens(scoped, org, agents.JobTokensInput{ZoneID: zone, JobID: job, ExpiresAt: time.Now().Add(time.Hour),
		Targets: []agents.JobTokenTarget{{HostID: h.ID, Name: long}}})
	if err != nil || len(got) != 1 || got[0].Name != long {
		t.Fatalf("minted = %+v, %v", got, err)
	}
	pending, _ := svc.ListTokens(scoped, org)
	if len(pending) != 1 || pending[0].NameHint != "" {
		t.Fatalf("pending = %+v, want one token without a hint", pending)
	}
}
