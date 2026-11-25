// Spec 2026-11-25 B (agent enrollment inside a run): the claim of a run
// whose person confirmed the hosts it enrols mints one host-bound
// single-use enrollment token per listed host of the job without an
// agent, hands each only to its own host (inventory host variables) and
// names it in the redact list; nothing else of the job ever holds it. A
// requeue and a re-claim revoke the earlier tokens, every terminal path
// revokes the unused ones in its own transaction, and the activity log
// names counts and hosts only.
package jobs

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/inventory/render"
)

func TestEnrollingClaimHandsEachHostItsOwnToken(t *testing.T) {
	w := newEnrolWorld(t, "jobs-enrol-claim")
	// web-3 runs an agent, web-4 is not on the list, and the list's last
	// host is not one of the job's.
	j := w.queueRun(t, []string{"web-1", "web-2", "web-3", "web-4"}, w.ids("web-1", "web-2", "web-3", "elsewhere"), nil)
	claimed := w.claim(t, j)

	tokens, dicts := handedOut(t, claimed)
	if len(tokens) != 2 || tokens["web-1"] == "" || tokens["web-2"] == "" || tokens["web-1"] == tokens["web-2"] {
		t.Fatalf("tokens = %v, want one of its own for web-1 and web-2 only", tokens)
	}
	if len(dicts) != 2 || !reflectEmptyDict(dicts["web-1"]) || !reflectEmptyDict(dicts["web-2"]) {
		t.Fatalf("pulsix_agent_enroll_tokens = %v, want {} on web-1 and web-2 only", dicts)
	}
	for host, tok := range tokens {
		parsed, _ := enrolltoken.ParseAgent(tok)
		if !slices.Contains(claimed.GetRedact(), tok) || !slices.Contains(claimed.GetRedact(), base64.RawURLEncoding.EncodeToString(parsed.Secret)) {
			t.Fatalf("%s's token or its secret part is not in the redact list", host)
		}
		var hostID, jobID uuid.UUID
		var maxUses int
		var expires time.Time
		if err := w.d.Pool().QueryRow(context.Background(), `SELECT host_id, job_id, max_uses, expires_at FROM agent_enrollments WHERE id = $1`, parsed.ID).
			Scan(&hostID, &jobID, &maxUses, &expires); err != nil {
			t.Fatal(err)
		}
		if hostID != w.hosts[host] || jobID != j.ID || maxUses != 1 || expires.Before(time.Now().Add(23*time.Hour)) || expires.After(time.Now().Add(25*time.Hour)) {
			t.Fatalf("%s's token = host %s job %s uses %d expires %v; want bound to it, the job's, single use, 24 h", host, hostID, jobID, maxUses, expires)
		}
	}
	if n := w.minted(t, j.ID); n != 2 {
		t.Fatalf("minted = %d, want 2", n)
	}
	assertNoToken(t, "the job record", w.jobRecord(t, j.ID), tokens)
	acts := w.activity(t)
	assertNoToken(t, "the activity log", strings.Join(acts, "\n"), tokens)
	if len(acts) != 1 || !strings.Contains(acts[0], "enrollment tokens minted for 2 hosts by job "+j.ID.String()) ||
		!strings.Contains(acts[0], "web-1") || !strings.Contains(acts[0], "web-2") {
		t.Fatalf("activity = %q, want the mint with its count and hosts", acts)
	}
}

func reflectEmptyDict(v any) bool {
	m, ok := v.(map[string]any)
	return ok && len(m) == 0
}

func TestClaimMintsNothingWithoutAConfirmedList(t *testing.T) {
	cases := []struct {
		name string
		list func(w *enrolWorld) []uuid.UUID
		mod  func(w *enrolWorld, j Job)
	}{
		{name: "no list", list: func(*enrolWorld) []uuid.UUID { return nil }},
		{name: "empty list", list: func(*enrolWorld) []uuid.UUID { return []uuid.UUID{} }},
		{name: "flag off", list: func(w *enrolWorld) []uuid.UUID { return w.ids("web-1", "web-2") }, mod: func(w *enrolWorld, _ Job) {
			row := w.templates.byID[w.tmpl]
			row.AgentEnrollment = false
			w.templates.byID[w.tmpl] = row
		}},
		{name: "webhook run", list: func(*enrolWorld) []uuid.UUID { return nil }, mod: func(w *enrolWorld, j Job) {
			// No webhook job is ever stored with a list; force one to
			// prove the claim does not trust the row alone.
			if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET trigger = 'webhook', enroll_host_ids = $2 WHERE id = $1`, j.ID, w.ids("web-1")); err != nil {
				panic(err)
			}
		}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newEnrolWorld(t, fmt.Sprintf("jobs-enrol-none-%d", i))
			j := w.queueRun(t, []string{"web-1", "web-2"}, tc.list(w), nil)
			if tc.mod != nil {
				tc.mod(w, j)
			}
			w.q = w.newQueue(w.agents)
			if tokens, dicts := handedOut(t, w.claim(t, j)); len(tokens) != 0 || len(dicts) != 0 {
				t.Fatalf("tokens = %v %v, want none", tokens, dicts)
			}
			if p := w.pendingOf(t, j.ID); len(p) != 0 || w.minted(t, j.ID) != 0 {
				t.Fatalf("pending = %v, minted %d; want nothing", p, w.minted(t, j.ID))
			}
		})
	}
}

// A dry run mints nothing even with a list (only a worker that supports
// check mode claims it).
func TestCheckModeClaimMintsNothing(t *testing.T) {
	w := newEnrolWorld(t, "jobs-enrol-check")
	worker := w.activeWorker(t, w.zone)
	j := w.queueRun(t, []string{"web-1"}, w.ids("web-1"), func(j *Job) { j.CheckMode = true })
	c, err := w.q.ClaimFor(context.Background(), worker.ID, worker.OrgID, []uuid.UUID{w.zone}, "v1", ClaimCaps{SupportsCheckMode: true})
	if err != nil || c == nil || c.Job.GetId() != j.ID.String() {
		t.Fatalf("ClaimFor = %+v, %v", c, err)
	}
	if tokens, _ := handedOut(t, &c.Job); len(tokens) != 0 || len(w.pendingOf(t, j.ID)) != 0 {
		t.Fatalf("a dry run got tokens %v", tokens)
	}
}

// A minting failure fails the job with a clear reason and hands nothing
// out; no token is left behind.
func TestMintFailureFailsTheJob(t *testing.T) {
	w := newEnrolWorld(t, "jobs-enrol-fail")
	w.q = w.newQueue(agents.NewService(agents.NewSQLStore(w.d))) // no CA: CreateToken refuses
	j := w.queueRun(t, []string{"web-1"}, w.ids("web-1"), nil)
	c, err := w.q.Claim(context.Background(), w.org, []uuid.UUID{w.zone}, "v1")
	if err != nil || c != nil {
		t.Fatalf("Claim = %+v, %v; want no claim", c, err)
	}
	got, err := NewSQLStore(w.d).GetJob(db.WithOrgScope(context.Background(), w.org), w.org, j.ID)
	if err != nil || got.Status != StatusError || !strings.Contains(got.Error, "enrollment token") {
		t.Fatalf("job = %s %q, %v; want error naming the enrollment tokens", got.Status, got.Error, err)
	}
	var n int
	if err := w.d.Pool().QueryRow(context.Background(), `SELECT count(*) FROM agent_enrollments WHERE job_id = $1`, j.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("tokens of the failed job = %d, %v; want none", n, err)
	}
}

// A re-claim revokes the earlier claim's unused tokens and mints fresh
// ones: a requeue by the reaper revokes them with the requeue, and a claim
// that finds some left (a requeue that did not) revokes them itself.
func TestReclaimRevokesAndMintsAfresh(t *testing.T) {
	ctx := context.Background()
	w := newEnrolWorld(t, "jobs-enrol-reclaim")
	j := w.queueRun(t, []string{"web-1", "web-2"}, w.ids("web-1", "web-2"), nil)
	first, _ := handedOut(t, w.claim(t, j))
	if len(first) != 2 {
		t.Fatalf("first claim tokens = %v", first)
	}
	if _, err := w.d.Pool().Exec(ctx, `UPDATE jobs SET claimed_at = now() - interval '2 minutes' WHERE id = $1`, j.ID); err != nil {
		t.Fatal(err)
	}
	if requeued, _, err := NewReaper(w.q, 0, 3).Sweep(ctx); err != nil || requeued != 1 {
		t.Fatalf("Sweep requeued %d, %v", requeued, err)
	}
	if p := w.pendingOf(t, j.ID); len(p) != 0 {
		t.Fatalf("the requeue left %d unused tokens", len(p))
	}
	second, _ := handedOut(t, w.claim(t, j))
	if len(second) != 2 || second["web-1"] == first["web-1"] || second["web-2"] == first["web-2"] {
		t.Fatalf("second claim tokens = %v, want fresh ones", second)
	}

	// A requeue that revoked nothing (a row moved back by hand): the claim
	// itself revokes what is left.
	if _, err := w.d.Pool().Exec(ctx, `UPDATE jobs SET status = 'queued', claimed_at = NULL, started_at = NULL WHERE id = $1`, j.ID); err != nil {
		t.Fatal(err)
	}
	third, _ := handedOut(t, w.claim(t, j))
	pending := w.pendingOf(t, j.ID)
	if len(third) != 2 || len(pending) != 2 {
		t.Fatalf("third claim tokens = %v, pending %d; want only its own two", third, len(pending))
	}
	for _, tok := range third {
		parsed, _ := enrolltoken.ParseAgent(tok)
		if !slices.Contains(pending, parsed.ID) {
			t.Fatalf("pending tokens %v are not the third claim's", pending)
		}
	}
	if n := w.minted(t, j.ID); n != 6 {
		t.Fatalf("minted = %d, want 6 over three claims", n)
	}
	acts := strings.Join(w.activity(t), "\n")
	if strings.Count(acts, "revoked 2 unused enrollment tokens of job "+j.ID.String()) != 2 {
		t.Fatalf("activity = %s, want two revocations of 2", acts)
	}
	assertNoToken(t, "the activity log", acts, first)
	assertNoToken(t, "the activity log", acts, third)
}

// A revoked worker's requeued job loses its tokens with the requeue.
func TestWorkerRevocationRequeueRevokesTokens(t *testing.T) {
	ctx := context.Background()
	w := newEnrolWorld(t, "jobs-enrol-revoked")
	worker := w.activeWorker(t, w.zone)
	j := w.queueRun(t, []string{"web-1"}, w.ids("web-1"), nil)
	c, err := w.q.ClaimFor(ctx, worker.ID, worker.OrgID, []uuid.UUID{w.zone}, "v1", ClaimCaps{})
	if err != nil || c == nil {
		t.Fatalf("ClaimFor = %+v, %v", c, err)
	}
	if requeued, _, err := w.q.RequeueWorkerJobs(ctx, w.org, worker.ID, "worker revoked", 3); err != nil || requeued != 1 {
		t.Fatalf("RequeueWorkerJobs = %d, %v", requeued, err)
	}
	if p := w.pendingOf(t, j.ID); len(p) != 0 {
		t.Fatalf("the requeue left %d unused tokens", len(p))
	}
}

// Every terminal path revokes the job's unused tokens in its own
// transaction and leaves the used ones.
func TestTerminalPathsRevokeUnusedTokens(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		drive func(t *testing.T, w *enrolWorld, j Job)
		want  Status
	}{
		{name: "succeeded", want: StatusSucceeded, drive: func(t *testing.T, w *enrolWorld, j Job) {
			if err := w.q.Finish(ctx, j.ID, StatusSucceeded, nil, ""); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "failed", want: StatusFailed, drive: func(t *testing.T, w *enrolWorld, j Job) {
			if err := w.q.Finish(ctx, j.ID, StatusFailed, nil, "boom"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "cancelled", want: StatusCancelled, drive: func(t *testing.T, w *enrolWorld, j Job) {
			if _, err := NewSQLStore(w.d).CancelJob(db.WithOrgScope(ctx, w.org), w.org, j.ID); err != nil {
				t.Fatal(err)
			}
			if err := w.q.Finish(ctx, j.ID, StatusCancelled, nil, ""); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unconfirmed cancel", want: StatusFailed, drive: func(t *testing.T, w *enrolWorld, j Job) {
			if _, err := NewSQLStore(w.d).CancelJob(db.WithOrgScope(ctx, w.org), w.org, j.ID); err != nil {
				t.Fatal(err)
			}
			w.q.NoteCancelRequested(j.ID, time.Now().Add(-2*cancelConfirmAfter))
			if _, failed, err := NewReaper(w.q, 0, 3).Sweep(ctx); err != nil || failed != 1 {
				t.Fatalf("Sweep failed %d, %v", failed, err)
			}
		}},
		{name: "reaper gives up", want: StatusError, drive: func(t *testing.T, w *enrolWorld, j Job) {
			if _, err := w.d.Pool().Exec(ctx, `UPDATE jobs SET claimed_at = now() - interval '2 minutes' WHERE id = $1`, j.ID); err != nil {
				t.Fatal(err)
			}
			if _, failed, err := NewReaper(w.q, 0, 1).Sweep(ctx); err != nil || failed != 1 {
				t.Fatalf("Sweep failed %d, %v", failed, err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newEnrolWorld(t, "jobs-enrol-end-"+strings.ReplaceAll(tc.name, " ", "-"))
			j := w.queueRun(t, []string{"web-1", "web-2"}, w.ids("web-1", "web-2"), nil)
			tokens, _ := handedOut(t, w.claim(t, j))
			used, err := enrolltoken.ParseAgent(tokens["web-1"])
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.agents.ClaimToken(ctx, w.org, used, ""); err != nil {
				t.Fatalf("enrol web-1: %v", err)
			}
			tc.drive(t, w, j)
			if got := w.statusOf(t, j.ID); got != tc.want {
				t.Fatalf("status = %s, want %s", got, tc.want)
			}
			if p := w.pendingOf(t, j.ID); len(p) != 0 {
				t.Fatalf("%d unused tokens survived the end", len(p))
			}
			got, err := NewSQLStore(w.d).GetJob(db.WithOrgScope(ctx, w.org), w.org, j.ID)
			if err != nil || got.EnrollTokensMinted != 2 || got.EnrollTokensUsed != 1 {
				t.Fatalf("counts = %d minted, %d used, %v; want 2, 1", got.EnrollTokensMinted, got.EnrollTokensUsed, err)
			}
			acts := strings.Join(w.activity(t), "\n")
			if !strings.Contains(acts, "revoked 1 unused enrollment tokens of job "+j.ID.String()) {
				t.Fatalf("activity = %s, want the revocation", acts)
			}
			assertNoToken(t, "the activity log", acts, tokens)
			assertNoToken(t, "the job record", w.jobRecord(t, j.ID), tokens)
		})
	}
}

// Each run mints for its own confirmed list: a retry's job enrols the
// hosts its person confirmed, not its original's.
func TestEachRunMintsForItsOwnList(t *testing.T) {
	w := newEnrolWorld(t, "jobs-enrol-retry")
	first := w.queueRun(t, []string{"web-1", "web-2"}, w.ids("web-1", "web-2"), nil)
	firstTokens, _ := handedOut(t, w.claim(t, first))
	if err := w.q.Finish(context.Background(), first.ID, StatusFailed, nil, "web-2 failed"); err != nil {
		t.Fatal(err)
	}
	retry := w.queueRun(t, []string{"web-2"}, w.ids("web-2"), nil)
	retryTokens, _ := handedOut(t, w.claim(t, retry))
	if len(firstTokens) != 2 || len(retryTokens) != 1 || retryTokens["web-2"] == "" || retryTokens["web-2"] == firstTokens["web-2"] {
		t.Fatalf("tokens = %v then %v, want the retry's own for web-2", firstTokens, retryTokens)
	}
}

// The hand-out mints only for the claim that is still the job's: a job
// requeued (or ended) before its claim's tokens were minted gets none, so
// nothing is minted that the requeue could no longer revoke.
func TestHandOverMintsNothingForAClaimThatMovedOn(t *testing.T) {
	w := newEnrolWorld(t, "jobs-enrol-moved")
	j := w.queueRun(t, []string{"web-1"}, w.ids("web-1"), nil)
	claimed := w.claim(t, j)
	tokens, _ := handedOut(t, claimed)
	if len(tokens) != 1 {
		t.Fatalf("tokens = %v", tokens)
	}
	got, err := NewSQLStore(w.d).GetJob(db.WithOrgScope(context.Background(), w.org), w.org, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET status = 'queued' WHERE id = $1`, j.ID); err != nil {
		t.Fatal(err)
	}
	inv, _ := enrolRenderer{names: map[uuid.UUID]string{w.hosts["web-1"]: "web-1"}}.Snapshot(context.Background(), w.org, render.Scope{HostIDs: w.ids("web-1")})
	err = w.q.handOverEnrollTokens(db.WithOrgScope(context.Background(), w.org), got, &inv, "")
	if !errors.Is(err, errClaimMoved) {
		t.Fatalf("hand-out for a requeued job = %v, want errClaimMoved", err)
	}
	if len(inv.Secrets) != 0 || w.minted(t, j.ID) != 1 {
		t.Fatalf("secrets %d, minted %d; want nothing more", len(inv.Secrets), w.minted(t, j.ID))
	}
	// A later claim's attempt does not match an earlier claim's either.
	if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET status = 'running', attempts = attempts + 1 WHERE id = $1`, j.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.q.handOverEnrollTokens(db.WithOrgScope(context.Background(), w.org), got, &inv, ""); !errors.Is(err, errClaimMoved) {
		t.Fatalf("hand-out for an earlier attempt = %v, want errClaimMoved", err)
	}
}

// A claim whose job moved on while it built the payload (a requeue and
// another claim got there first) ends without a job for its worker and
// leaves the job, the run and the tokens of the claim that now owns it
// alone (review finding 1).
func TestAClaimThatMovedOnLeavesTheNewClaimAlone(t *testing.T) {
	ctx := context.Background()
	w := newEnrolWorld(t, "jobs-enrol-moved-claim")
	j := w.queueRun(t, []string{"web-1"}, w.ids("web-1"), nil)
	var second *Claim
	var secondErr error
	fired := false
	w.q = w.newHookedQueue(w.agents, func() {
		if fired {
			return
		}
		fired = true
		// The first claim's worker went away: its job is requeued, and
		// another claim takes it, mints and hands out its own tokens.
		if _, err := w.d.Pool().Exec(ctx, `UPDATE jobs SET status = 'queued', claimed_at = NULL, started_at = NULL WHERE id = $1`, j.ID); err != nil {
			secondErr = err
			return
		}
		second, secondErr = w.q.Claim(ctx, w.org, []uuid.UUID{w.zone}, "v1")
	})
	first, err := w.q.Claim(ctx, w.org, []uuid.UUID{w.zone}, "v1")
	if err != nil || first != nil {
		t.Fatalf("the moved-on claim = %+v, %v; want no job and no error", first, err)
	}
	if secondErr != nil || second == nil || second.Job.GetId() != j.ID.String() {
		t.Fatalf("the second claim = %+v, %v", second, secondErr)
	}
	tokens, _ := handedOut(t, &second.Job)
	parsed, err := enrolltoken.ParseAgent(tokens["web-1"])
	if err != nil {
		t.Fatalf("second claim's tokens = %v", tokens)
	}
	got, err := NewSQLStore(w.d).GetJob(db.WithOrgScope(ctx, w.org), w.org, j.ID)
	if err != nil || got.Status != StatusRunning || got.Error != "" || got.Attempts != 2 {
		t.Fatalf("job = %s %q attempts %d, %v; want the second claim's run untouched", got.Status, got.Error, got.Attempts, err)
	}
	if p := w.pendingOf(t, j.ID); len(p) != 1 || p[0] != parsed.ID {
		t.Fatalf("pending = %v, want the second claim's token %s", p, parsed.ID)
	}
}

// failingRevoke is the world's registry whose revocation fails for one job.
type failingRevoke struct {
	agentTokens
	job uuid.UUID
}

func (f failingRevoke) RevokeJobTokens(ctx context.Context, orgID, jobID uuid.UUID) (int, error) {
	if jobID == f.job {
		return 0, errors.New("revoke failed")
	}
	return f.agentTokens.RevokeJobTokens(ctx, orgID, jobID)
}

// The reaper requeues each stale job in a transaction of its own, bound to
// the job's organization: one job's failed revocation leaves that job as
// it was and requeues the others (review finding 2).
func TestReaperRequeuesEachJobOnItsOwn(t *testing.T) {
	ctx := context.Background()
	w := newEnrolWorld(t, "jobs-enrol-reaper-own")
	stuck := w.queueRun(t, []string{"web-1"}, w.ids("web-1"), nil)
	w.claim(t, stuck)
	other := w.queueRun(t, []string{"web-2"}, w.ids("web-2"), nil)
	w.claim(t, other)
	if _, err := w.d.Pool().Exec(ctx, `UPDATE jobs SET claimed_at = now() - interval '2 minutes' WHERE id = ANY($1)`, []uuid.UUID{stuck.ID, other.ID}); err != nil {
		t.Fatal(err)
	}
	w.q.SetEnrollment(failingRevoke{agentTokens: agentTokens{svc: w.agents}, job: stuck.ID}, audit.NewRecorder(audit.NewSQLStore(w.d), time.Now))

	requeued, _, err := NewReaper(w.q, 0, 3).Sweep(ctx)
	if err == nil || requeued != 1 {
		t.Fatalf("Sweep = %d requeued, %v; want the other job requeued and the failure reported", requeued, err)
	}
	if got := w.statusOf(t, other.ID); got != StatusQueued || len(w.pendingOf(t, other.ID)) != 0 {
		t.Fatalf("the other job is %s with %d tokens, want queued with none", got, len(w.pendingOf(t, other.ID)))
	}
	if got := w.statusOf(t, stuck.ID); got != StatusRunning || len(w.pendingOf(t, stuck.ID)) != 1 {
		t.Fatalf("the failing job is %s with %d tokens, want it left running with its token", got, len(w.pendingOf(t, stuck.ID)))
	}
}

// The claim mints only for listed hosts the play runs on: the run's
// effective limit narrows them by name, and a limit that names a group
// (or cannot be decided) narrows nothing (review finding 5).
func TestClaimMintsOnlyForHostsTheLimitKeeps(t *testing.T) {
	cases := []struct {
		limit string
		want  []string
	}{
		{limit: "web-1", want: []string{"web-1"}},
		{limit: "web-*:!web-2", want: []string{"web-1"}},
		{limit: "web*:!web-2", want: []string{"web-1", "web-2"}}, // matches the group web too
		{limit: "db*", want: nil},
		{limit: "web", want: []string{"web-1", "web-2"}},            // the group of that name
		{limit: "web-[12]&web-2", want: []string{"web-1", "web-2"}}, // undecidable
	}
	for i, tc := range cases {
		t.Run(tc.limit, func(t *testing.T) {
			w := newEnrolWorld(t, fmt.Sprintf("jobs-enrol-limit-%d", i))
			row := w.templates.byID[w.tmpl]
			row.Limit = tc.limit
			w.templates.byID[w.tmpl] = row
			w.q = w.newQueue(w.agents)
			j := w.queueRun(t, []string{"web-1", "web-2"}, w.ids("web-1", "web-2"), nil)
			tokens, _ := handedOut(t, w.claim(t, j))
			var got []string
			for host := range tokens {
				got = append(got, host)
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) || w.minted(t, j.ID) != len(tc.want) {
				t.Fatalf("tokens for %v (minted %d), want %v", got, w.minted(t, j.ID), tc.want)
			}
		})
	}
}
