package triggers_test

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/clientip"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/triggers"
)

type fakeRuns struct {
	mu       sync.Mutex
	requests []jobs.RunRequest
	active   bool
	err      error
}

func (f *fakeRuns) EnqueueRunWith(_ context.Context, orgID, templateID uuid.UUID, req jobs.RunRequest) ([]jobs.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.requests = append(f.requests, req)
	return []jobs.Job{{ID: uuid.Must(uuid.NewV7()), OrgID: orgID, TemplateID: &templateID, RunGroupID: uuid.Must(uuid.NewV7())}}, nil
}

func (f *fakeRuns) EnqueueSyncWith(_ context.Context, orgID, _ uuid.UUID, req jobs.RunRequest) (jobs.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	return jobs.Job{ID: uuid.Must(uuid.NewV7()), OrgID: orgID, RunGroupID: uuid.Must(uuid.NewV7())}, f.err
}

func (f *fakeRuns) HasActiveRun(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return f.active, nil
}
func (f *fakeRuns) HasActiveSync(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return f.active, nil
}

type fakeAudit struct{ events []audit.Event }

func (f *fakeAudit) Record(_ context.Context, e audit.Event) error {
	f.events = append(f.events, e)
	return nil
}

type recording struct {
	*fakeStore
	deliveries []triggers.Delivery
	noTokens   bool
	// recordErr, when set, makes RecordDelivery fail after still
	// recording the attempt — for TestReceiveKeepsTheRunWhenRecordingTheDeliveryFails.
	recordErr error
	// rateLimitNotes records every NoteRateLimited call, for
	// TestReceiveNotesTheOriginalDeliveryRowOnceTheAddressBudgetIsSpent.
	rateLimitNotes []rateLimitNote
}

type rateLimitNote struct {
	orgID, triggerID uuid.UUID
	outcome          triggers.Outcome
	at               time.Time
}

func (r *recording) NoteRateLimited(_ context.Context, orgID, triggerID uuid.UUID, outcome triggers.Outcome, at time.Time) error {
	r.rateLimitNotes = append(r.rateLimitNotes, rateLimitNote{orgID, triggerID, outcome, at})
	return nil
}

func (r *recording) TakeToken(context.Context, uuid.UUID, uuid.UUID, triggers.RateLimit, time.Time) (bool, time.Duration, error) {
	if r.noTokens {
		return false, 6 * time.Second, nil
	}
	return true, 0, nil
}

func (r *recording) RecordDelivery(_ context.Context, d triggers.Delivery) error {
	r.deliveries = append(r.deliveries, d)
	return r.recordErr
}

type receiveWorld struct {
	*world
	rec   *recording
	runs  *fakeRuns
	audit *fakeAudit
}

func newReceiveWorld(t *testing.T) *receiveWorld {
	t.Helper()
	w := newWorld(t)
	rw := &receiveWorld{world: w, rec: &recording{fakeStore: w.store}, runs: &fakeRuns{}, audit: &fakeAudit{}}
	rw.svc = triggers.NewService(triggers.Deps{
		Store: rw.rec, Templates: w.tmpls, Repos: w.repos, Box: fakeBox{},
		Runs: rw.runs, Audit: rw.audit, PublicURL: "https://pulsix.example.test",
		Now: func() time.Time { return time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC) }, Rand: rand.Reader,
	})
	return rw
}

func (rw *receiveWorld) signed(t *testing.T, c triggers.Created, body string) triggers.Request {
	t.Helper()
	h := http.Header{}
	h.Set(c.Trigger.SignatureHeader(), triggers.Sign(c.Trigger.Preset, c.Secret, "", []byte(body)))
	return triggers.Request{TriggerID: c.Trigger.ID.String(), Kind: c.Trigger.Kind, Body: []byte(body), Header: h.Get, SourceIP: netip.MustParseAddr("140.82.112.5")}
}

func TestReceiveTriggersARunWithMappedOverrides(t *testing.T) {
	rw := newReceiveWorld(t)
	c, err := rw.svc.Create(ctx, rw.org, rw.actor, rw.templateInput())
	if err != nil {
		t.Fatal(err)
	}
	res, err := rw.svc.Receive(ctx, rw.signed(t, c, `{"ref":"v1.2.3"}`))
	if err != nil || res.Outcome != triggers.OutcomeTriggered || res.RunID == nil {
		t.Fatalf("Receive = %+v, %v", res, err)
	}
	req := rw.runs.requests[0]
	if req.Trigger != jobs.TriggerWebhook || *req.TriggerRef != c.Trigger.ID || req.Overrides.ExtraVars["release"] != "v1.2.3" {
		t.Fatalf("run request = %+v", req)
	}
	d := rw.rec.deliveries[0]
	if d.Outcome != triggers.OutcomeTriggered || d.SourceIP != "140.82.112.5" || d.JobGroupID == nil {
		t.Fatalf("delivery = %+v", d)
	}
	e := rw.audit.events[0]
	if e.ActorKind != audit.ActorKindWebhook || *e.ActorID != c.Trigger.ID || e.Action != "webhook.run" || e.Meta["source_ip"] != "140.82.112.5" {
		t.Fatalf("audit = %+v", e)
	}
}

// TestReceiveMarksMappedValuesUntrusted is final review C1: a webhook's
// mapped values travel as untrusted overrides (the claim marks them unsafe
// for Ansible), secrets are refused and the limit is held to plain host
// patterns.
func TestReceiveMarksMappedValuesUntrusted(t *testing.T) {
	rw := newReceiveWorld(t)
	in := rw.templateInput()
	in.Mapping.Limit = "$.target"
	c, err := rw.svc.Create(ctx, rw.org, rw.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	res, err := rw.svc.Receive(ctx, rw.signed(t, c, `{"ref":"{{ lookup('pipe','id') }}","target":"web"}`))
	if err != nil || res.Outcome != triggers.OutcomeTriggered {
		t.Fatalf("Receive = %+v, %v", res, err)
	}
	ov := rw.runs.requests[0].Overrides
	if ov == nil || !ov.Untrusted || ov.ExtraVars["release"] != "{{ lookup('pipe','id') }}" || *ov.Limit != "web" {
		t.Fatalf("overrides = %+v", ov)
	}
	opts := *rw.tmpls.opts
	if len(opts) != 1 || !opts[0].RefuseSecrets || !opts[0].StrictLimit {
		t.Fatalf("ValidateOverrides options = %+v", opts)
	}
}

func TestReceiveRejections(t *testing.T) {
	rw := newReceiveWorld(t)
	in := rw.templateInput()
	in.AllowCIDRs = []string{"140.82.112.0/20"}
	c, _ := rw.svc.Create(ctx, rw.org, rw.actor, in)
	good := rw.signed(t, c, `{"ref":"x"}`)

	cases := []struct {
		name    string
		req     func() triggers.Request
		outcome triggers.Outcome
		stored  bool
	}{
		{"unknown id", func() triggers.Request { r := good; r.TriggerID = uuid.NewString(); return r }, triggers.OutcomeNotFound, false},
		{"garbage id", func() triggers.Request { r := good; r.TriggerID = "../etc"; return r }, triggers.OutcomeNotFound, false},
		{"wrong endpoint", func() triggers.Request { r := good; r.Kind = triggers.KindRepoSync; return r }, triggers.OutcomeNotFound, false},
		{"outside the allowlist", func() triggers.Request { r := good; r.SourceIP = netip.MustParseAddr("203.0.113.9"); return r }, triggers.OutcomeRejectedIP, true},
		{"bad signature", func() triggers.Request {
			r := good
			h := http.Header{}
			h.Set(c.Trigger.SignatureHeader(), "sha256=00")
			r.Header = h.Get
			return r
		}, triggers.OutcomeRejectedAuth, true},
		{"not json", func() triggers.Request { return rw.signed(t, c, `ref=x`) }, triggers.OutcomeInvalidPayload, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(rw.rec.deliveries)
			res, err := rw.svc.Receive(ctx, tc.req())
			if err != nil || res.Outcome != tc.outcome {
				t.Fatalf("Receive = %+v, %v; want %s", res, err, tc.outcome)
			}
			if stored := len(rw.rec.deliveries) > before; stored != tc.stored {
				t.Fatalf("stored = %v, want %v", stored, tc.stored)
			}
			// Whether a recorded outcome is THROTTLED to one row per
			// minute is not this layer's to state any more (backlog
			// 2.68): it is trigger_deliveries_throttle_idx's outcome
			// list, pinned against real Postgres by
			// store_sql_test.go's own throttling tests. What matters
			// here is which outcome was recorded.
			if tc.stored && rw.rec.deliveries[len(rw.rec.deliveries)-1].Outcome != tc.outcome {
				t.Fatalf("recorded outcome = %s, want %s", rw.rec.deliveries[len(rw.rec.deliveries)-1].Outcome, tc.outcome)
			}
		})
	}
	if len(rw.runs.requests) != 0 {
		t.Fatal("no rejection may enqueue")
	}
}

func TestReceiveDisabledRateLimitedSkippedAndFailed(t *testing.T) {
	rw := newReceiveWorld(t)
	in := rw.templateInput()
	in.OnActive = triggers.OnActiveSkip
	c, _ := rw.svc.Create(ctx, rw.org, rw.actor, in)

	rw.runs.active = true
	if res, _ := rw.svc.Receive(ctx, rw.signed(t, c, `{}`)); res.Outcome != triggers.OutcomeSkippedActive {
		t.Fatalf("on_active skip = %+v", res)
	}
	rw.runs.active = false
	rw.runs.err = jobs.ErrEmptyScope
	if res, _ := rw.svc.Receive(ctx, rw.signed(t, c, `{}`)); res.Outcome != triggers.OutcomeFailed {
		t.Fatalf("enqueue failure = %+v", res)
	}
	if last := rw.rec.deliveries[len(rw.rec.deliveries)-1]; last.Reason != "the template's scope resolves to no host" {
		t.Fatalf("failure reason = %q", last.Reason)
	}
	rw.runs.err = nil
	rw.rec.noTokens = true
	if res, _ := rw.svc.Receive(ctx, rw.signed(t, c, `{}`)); res.Outcome != triggers.OutcomeRateLimited || res.RetryAfter != 6*time.Second {
		t.Fatalf("rate limited = %+v", res)
	}
	rw.rec.noTokens = false
	in.Enabled = false
	if _, err := rw.svc.Update(ctx, rw.org, c.Trigger.ID, 1, in); err != nil {
		t.Fatal(err)
	}
	if res, _ := rw.svc.Receive(ctx, rw.signed(t, c, `{}`)); res.Outcome != triggers.OutcomeDisabled {
		t.Fatalf("disabled = %+v", res)
	}
}

// TestReceiveRecordsAnUnresolvedSharedTemplateDistinctly is
// failureReason's own test for jobs.ErrTemplateNotReady (v0.2f Task 6
// review): a webhook against a shared template projection whose playbook
// has not synced yet must record THIS failureReason case, not fall through
// to the value-free default ("could not enqueue (see the manager log)").
func TestReceiveRecordsAnUnresolvedSharedTemplateDistinctly(t *testing.T) {
	rw := newReceiveWorld(t)
	c, _ := rw.svc.Create(ctx, rw.org, rw.actor, rw.templateInput())

	rw.runs.err = jobs.ErrTemplateNotReady
	if res, _ := rw.svc.Receive(ctx, rw.signed(t, c, `{}`)); res.Outcome != triggers.OutcomeFailed {
		t.Fatalf("enqueue failure = %+v", res)
	}
	if last := rw.rec.deliveries[len(rw.rec.deliveries)-1]; last.Reason != "the template's playbook has not been registered in this organization yet" {
		t.Fatalf("failure reason = %q, want the precise ErrTemplateNotReady reason, not the value-free default", last.Reason)
	}
}

func TestReceiveRepoSyncAndTestCall(t *testing.T) {
	rw := newReceiveWorld(t)
	// PresetPulsix, not PresetGitLab: Task 9 review Minor 3 (fixed on this
	// branch before this task) restricts a timestamp header to the pulsix
	// preset, since GitLab's token does not bind one. The test's own
	// intent — repo_sync plus TestCall exercising the timestamp window —
	// does not depend on which preset, so it stays on the one the fix
	// allows.
	c, err := rw.svc.Create(ctx, rw.org, rw.actor, triggers.Input{Kind: triggers.KindRepoSync, RepoID: &rw.repo, Name: "sync", Enabled: true, Preset: triggers.PresetPulsix,
		AllowCIDRs: []string{"10.0.0.0/8"}, TimestampHeader: "X-Pulsix-Timestamp"})
	if err != nil {
		t.Fatal(err)
	}
	// The test call signs with the real secret and a fresh timestamp; the
	// allowlist (which would refuse 127.0.0.1) is not evaluated.
	res, err := rw.svc.TestCall(ctx, rw.org, c.Trigger.ID, nil)
	if err != nil || res.Outcome != triggers.OutcomeTriggered {
		t.Fatalf("TestCall = %+v, %v", res, err)
	}
	if rw.rec.deliveries[0].Reason != "test call" || rw.audit.events[0].ObjectType != "repo" {
		t.Fatalf("delivery = %+v, audit = %+v", rw.rec.deliveries[0], rw.audit.events[0])
	}
	if _, err := rw.svc.TestCall(ctx, uuid.Must(uuid.NewV7()), c.Trigger.ID, nil); !errors.Is(err, triggers.ErrNotFound) {
		t.Fatalf("another org's test call: %v", err)
	}
}

func (rw *receiveWorld) withIPLimit(limit int) {
	now := func() time.Time { return time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC) }
	rw.svc = triggers.NewService(triggers.Deps{
		Store: rw.rec, Templates: rw.tmpls, Repos: rw.repos, Box: fakeBox{},
		Runs: rw.runs, Audit: rw.audit, PublicURL: "https://pulsix.example.test",
		IPLimit: clientip.NewLimiter(limit, time.Minute, now), Now: now, Rand: rand.Reader,
	})
}

// TestReceiveLimitsFailedCallsPerSourceAddress: the per-address budget is
// charged by calls that end in the uniform 404 — unknown and malformed
// ids, wrong endpoint, disabled, disallowed address, bad signature — and
// once it is spent they all answer 429 alike, so the limit still reveals
// nothing about which ids exist. Nothing is recorded for an unknown id.
func TestReceiveLimitsFailedCallsPerSourceAddress(t *testing.T) {
	rw := newReceiveWorld(t)
	rw.withIPLimit(2)
	c, err := rw.svc.Create(ctx, rw.org, rw.actor, rw.templateInput())
	if err != nil {
		t.Fatal(err)
	}
	src := netip.MustParseAddr("203.0.113.7")
	unknown := triggers.Request{TriggerID: uuid.Must(uuid.NewV7()).String(), Kind: triggers.KindTemplateRun, Header: http.Header{}.Get, SourceIP: src}
	malformed := unknown
	malformed.TriggerID = "not-a-uuid"
	badSig := rw.signed(t, c, `{"ref":"v1"}`)
	badSig.Body = []byte(`{"ref":"v2"}`)
	badSig.SourceIP = src
	for i, want := range []triggers.Outcome{triggers.OutcomeNotFound, triggers.OutcomeNotFound} {
		if res, err := rw.svc.Receive(ctx, []triggers.Request{unknown, malformed}[i]); err != nil || res.Outcome != want {
			t.Fatalf("call %d = %+v, %v", i, res, err)
		}
	}
	for name, req := range map[string]triggers.Request{"unknown": unknown, "malformed": malformed, "bad signature": badSig} {
		res, err := rw.svc.Receive(ctx, req)
		if err != nil || res.Outcome != triggers.OutcomeRateLimited || res.RetryAfter != time.Minute {
			t.Fatalf("%s after the budget = %+v, %v; want the same 429 for every failure", name, res, err)
		}
	}
	// Another source keeps its own budget.
	other := unknown
	other.SourceIP = netip.MustParseAddr("192.0.2.9")
	if res, _ := rw.svc.Receive(ctx, other); res.Outcome != triggers.OutcomeNotFound {
		t.Fatalf("another address = %+v", res)
	}
}

// TestReceiveNotesTheOriginalDeliveryRowOnceTheAddressBudgetIsSpent (2.70):
// once the per-address budget is spent, the sender sees 429 even though
// finish() already recorded the real refusal (rejected_auth here); without
// the note, the delivery row an operator reads would permanently disagree
// with what the sender was actually told.
func TestReceiveNotesTheOriginalDeliveryRowOnceTheAddressBudgetIsSpent(t *testing.T) {
	rw := newReceiveWorld(t)
	rw.withIPLimit(1)
	c, err := rw.svc.Create(ctx, rw.org, rw.actor, rw.templateInput())
	if err != nil {
		t.Fatal(err)
	}
	badSig := rw.signed(t, c, `{"ref":"v1"}`)
	badSig.Body = []byte(`{"ref":"v2"}`)
	badSig.SourceIP = netip.MustParseAddr("203.0.113.9")

	// The first call spends the one-call budget and is answered on its own
	// merits.
	if res, err := rw.svc.Receive(ctx, badSig); err != nil || res.Outcome != triggers.OutcomeRejectedAuth {
		t.Fatalf("first call = %+v, %v", res, err)
	}
	if len(rw.rec.rateLimitNotes) != 0 {
		t.Fatalf("no note expected before the budget is spent: %+v", rw.rec.rateLimitNotes)
	}

	// The second call from the same address exhausts the budget.
	res, err := rw.svc.Receive(ctx, badSig)
	if err != nil || res.Outcome != triggers.OutcomeRateLimited {
		t.Fatalf("second call = %+v, %v", res, err)
	}
	if len(rw.rec.rateLimitNotes) != 1 {
		t.Fatalf("rate limit notes = %+v, want exactly one", rw.rec.rateLimitNotes)
	}
	note := rw.rec.rateLimitNotes[0]
	if note.orgID != rw.org || note.triggerID != c.Trigger.ID || note.outcome != triggers.OutcomeRejectedAuth {
		t.Fatalf("note = %+v, want the real (pre-budget) outcome rejected_auth for this trigger/org", note)
	}
}

// TestReceiveFloodOfUnknownIDsDoesNotStarveVerifiedDeliveries is final
// review I1: behind a reverse proxy every caller can share one address.
// An anonymous flood of unknown ids from it must not lock out a sender
// that proves the trigger's secret: verified deliveries are neither
// charged nor refused by the per-address budget (the per-trigger token
// bucket limits them).
func TestReceiveFloodOfUnknownIDsDoesNotStarveVerifiedDeliveries(t *testing.T) {
	rw := newReceiveWorld(t)
	rw.withIPLimit(5)
	c, err := rw.svc.Create(ctx, rw.org, rw.actor, rw.templateInput())
	if err != nil {
		t.Fatal(err)
	}
	proxy := netip.MustParseAddr("172.31.250.250")
	for i := 0; i < 50; i++ {
		flood := triggers.Request{TriggerID: uuid.Must(uuid.NewV7()).String(), Kind: triggers.KindTemplateRun, Header: http.Header{}.Get, SourceIP: proxy}
		if _, err := rw.svc.Receive(ctx, flood); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 10; i++ {
		req := rw.signed(t, c, `{"ref":"v1"}`)
		req.SourceIP = proxy
		res, err := rw.svc.Receive(ctx, req)
		if err != nil || res.Outcome != triggers.OutcomeTriggered {
			t.Fatalf("verified delivery %d during the flood = %+v, %v", i, res, err)
		}
	}
	if len(rw.runs.requests) != 10 {
		t.Fatalf("runs = %d, want 10", len(rw.runs.requests))
	}
	// The in-process test call never touches the budget either.
	if res, err := rw.svc.TestCall(ctx, rw.org, c.Trigger.ID, nil); err != nil || res.Outcome != triggers.OutcomeTriggered {
		t.Fatalf("test call = %+v, %v", res, err)
	}
}

// TestReceiveHidesASecretFaultBehindTheUniform404 is final review minor 9:
// a fault after the lookup but before the sender proved the secret must
// answer like an unknown id, not with a 500 only existing ids produce.
func TestReceiveHidesASecretFaultBehindTheUniform404(t *testing.T) {
	rw := newReceiveWorld(t)
	c, err := rw.svc.Create(ctx, rw.org, rw.actor, rw.templateInput())
	if err != nil {
		t.Fatal(err)
	}
	rw.rec.secrets[c.Trigger.ID] = []byte("sealed under another aad")
	res, err := rw.svc.Receive(ctx, rw.signed(t, c, `{"ref":"v1"}`))
	if err != nil || res.Outcome != triggers.OutcomeNotFound {
		t.Fatalf("Receive = %+v, %v; want the uniform not-found", res, err)
	}
	if len(rw.runs.requests) != 0 {
		t.Fatal("no run may start")
	}
}

// TestReceiveTreatsAStoredMappingCompileFailureAsAServerFault covers Task
// 10 review Minor 4: a mapping that compiled fine when the trigger was
// saved (Create validates it) but no longer does by the time a delivery
// arrives — say, after an incompatible library upgrade — is a
// configuration fault, not the sender's. Create's own validation refuses
// such a mapping outright, so the stored trigger is corrupted directly to
// reach this state.
func TestReceiveTreatsAStoredMappingCompileFailureAsAServerFault(t *testing.T) {
	rw := newReceiveWorld(t)
	c, err := rw.svc.Create(ctx, rw.org, rw.actor, rw.templateInput())
	if err != nil {
		t.Fatal(err)
	}
	corrupt := rw.rec.byID[c.Trigger.ID]
	corrupt.Mapping = triggers.Mapping{ExtraVars: map[string]string{"release": "not a jsonpath"}}
	rw.rec.byID[c.Trigger.ID] = corrupt

	res, err := rw.svc.Receive(ctx, rw.signed(t, c, `{"ref":"v1"}`))
	if err != nil || res.Outcome != triggers.OutcomeFailed || res.Field != "" {
		t.Fatalf("Receive = %+v, %v; want a server-side failure, not invalid_payload", res, err)
	}
	last := rw.rec.deliveries[len(rw.rec.deliveries)-1]
	if last.Reason == "" || strings.Contains(last.Reason, "not a jsonpath") {
		t.Fatalf("delivery reason = %q; must be generic, never the compile error", last.Reason)
	}
	if len(rw.runs.requests) != 0 {
		t.Fatal("a mapping fault must not enqueue a run")
	}
}

// TestReceiveKeepsTheRunWhenRecordingTheDeliveryFails covers Task 10
// review Minor 6: once a run is enqueued, a failure to write its delivery
// row must not turn the response into a 500 — a sender that retries on 5xx
// (GitHub, GitLab, ...) would otherwise start a duplicate run.
func TestReceiveKeepsTheRunWhenRecordingTheDeliveryFails(t *testing.T) {
	rw := newReceiveWorld(t)
	c, err := rw.svc.Create(ctx, rw.org, rw.actor, rw.templateInput())
	if err != nil {
		t.Fatal(err)
	}
	rw.rec.recordErr = errors.New("simulated delivery insert failure")
	res, err := rw.svc.Receive(ctx, rw.signed(t, c, `{"ref":"v1"}`))
	if err != nil || res.Outcome != triggers.OutcomeTriggered || res.RunID == nil {
		t.Fatalf("Receive = %+v, %v; a delivery-log failure must not turn an already-enqueued run into an error", res, err)
	}
	if len(rw.runs.requests) != 1 {
		t.Fatalf("run requests = %d, want exactly 1 (no duplicate enqueue on retry)", len(rw.runs.requests))
	}
}

// TestReceiveRefusesReservedAnsibleKeys is final re-review New Breakage 1:
// an object key starting with "__ansible", at any depth of a mapped value,
// would make Ansible's JSON load of the extra-vars file fail and fall back
// to YAML, which drops every unsafe marker. The call is refused with a 422
// naming the mapping target field, and no run is enqueued.
func TestReceiveRefusesReservedAnsibleKeys(t *testing.T) {
	rw := newReceiveWorld(t)
	c, err := rw.svc.Create(ctx, rw.org, rw.actor, rw.templateInput())
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"ref":{"__ansible_unsafe":"x"}}`,
		`{"ref":{"labels":[{"ok":1},{"__ansible_vault":5}]}}`,
		`{"ref":{"a":{"b":{"__ansible_type":"x"}}}}`,
	} {
		res, err := rw.svc.Receive(ctx, rw.signed(t, c, body))
		if err != nil || res.Outcome != triggers.OutcomeInvalidPayload || res.Field != "extra_vars.release" {
			t.Fatalf("Receive(%s) = %+v, %v; want invalid_payload at extra_vars.release", body, res, err)
		}
		if d := rw.rec.deliveries[len(rw.rec.deliveries)-1]; strings.Contains(d.Reason, "__ansible") {
			t.Fatalf("the reason must not echo the payload: %q", d.Reason)
		}
	}
	if len(rw.runs.requests) != 0 {
		t.Fatalf("a refused payload must not enqueue: %+v", rw.runs.requests)
	}
	// A plain nested object still maps.
	if res, err := rw.svc.Receive(ctx, rw.signed(t, c, `{"ref":{"labels":[{"env":"prod"}]}}`)); err != nil || res.Outcome != triggers.OutcomeTriggered {
		t.Fatalf("plain object = %+v, %v", res, err)
	}
}

// TestReceiveRefusesDeeplyNestedValues is final re-review 2, New Breakage
// 1: a mapped value nested deeper than ansiblevars.MaxUntrustedDepth would
// make Ansible's JSON load of the extra-vars file fail on Python's
// recursion limit and fall back to YAML, dropping every unsafe marker. It
// answers 422 naming the mapping target field; nothing is enqueued.
func TestReceiveRefusesDeeplyNestedValues(t *testing.T) {
	rw := newReceiveWorld(t)
	c, err := rw.svc.Create(ctx, rw.org, rw.actor, rw.templateInput())
	if err != nil {
		t.Fatal(err)
	}
	nested := func(depth int) string {
		return `{"ref":` + strings.Repeat("[", depth) + `"x"` + strings.Repeat("]", depth) + `}`
	}
	res, err := rw.svc.Receive(ctx, rw.signed(t, c, nested(300)))
	if err != nil || res.Outcome != triggers.OutcomeInvalidPayload || res.Field != "extra_vars.release" {
		t.Fatalf("Receive(depth 300) = %+v, %v; want invalid_payload at extra_vars.release", res, err)
	}
	if len(rw.runs.requests) != 0 {
		t.Fatalf("a refused payload must not enqueue: %+v", rw.runs.requests)
	}
	if res, err := rw.svc.Receive(ctx, rw.signed(t, c, nested(32))); err != nil || res.Outcome != triggers.OutcomeTriggered {
		t.Fatalf("Receive(depth 32) = %+v, %v; the cap itself is allowed", res, err)
	}
}

// TestReceiveInvalidPayloadNamesOnlyTheMappingTarget is the other half of
// the redaction: firstSegment is correct in isolation, and this pins that
// it is what the caller actually relies on. The validation error's own
// message carries the full path, so an answer built from the error — the
// obvious refactor — would put the sender's own keys and indices into the
// 422 and into the delivery row an operator reads.
func TestReceiveInvalidPayloadNamesOnlyTheMappingTarget(t *testing.T) {
	rw := newReceiveWorld(t)
	c, err := rw.svc.Create(ctx, rw.org, rw.actor, rw.templateInput())
	if err != nil {
		t.Fatal(err)
	}
	*rw.tmpls.refuse = &schema.ValidationError{Problems: []schema.Problem{
		{Path: "release.__ansible_unsafe[7]", Message: "expected a string"},
	}}

	res, err := rw.svc.Receive(ctx, rw.signed(t, c, `{"ref":"v1.2.3"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != triggers.OutcomeInvalidPayload {
		t.Fatalf("outcome = %s, want %s", res.Outcome, triggers.OutcomeInvalidPayload)
	}
	if res.Field != "extra_vars.release" {
		t.Fatalf("field = %q, want the mapping target alone", res.Field)
	}
	d := rw.rec.deliveries[0]
	if d.Reason != "invalid value at extra_vars.release" {
		t.Fatalf("delivery reason = %q", d.Reason)
	}
	for _, leaked := range []string{"__ansible_unsafe", "[7]", "expected a string"} {
		if strings.Contains(res.Field, leaked) || strings.Contains(d.Reason, leaked) {
			t.Fatalf("%q reached the answer or the delivery log: field=%q reason=%q", leaked, res.Field, d.Reason)
		}
	}
	// Nothing ran: an invalid payload must not reach the queue.
	if len(rw.runs.requests) != 0 {
		t.Fatalf("enqueued %d runs for an invalid payload", len(rw.runs.requests))
	}
}
