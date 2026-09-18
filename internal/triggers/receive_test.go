package triggers_test

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/clientip"
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
	throttled  []bool
	noTokens   bool
}

func (r *recording) TakeToken(context.Context, uuid.UUID, uuid.UUID, triggers.RateLimit, time.Time) (bool, time.Duration, error) {
	if r.noTokens {
		return false, 6 * time.Second, nil
	}
	return true, 0, nil
}

func (r *recording) RecordDelivery(_ context.Context, d triggers.Delivery, throttle bool) error {
	r.deliveries = append(r.deliveries, d)
	r.throttled = append(r.throttled, throttle)
	return nil
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
	if d.Outcome != triggers.OutcomeTriggered || d.SourceIP != "140.82.112.5" || d.JobGroupID == nil || rw.rec.throttled[0] {
		t.Fatalf("delivery = %+v", d)
	}
	e := rw.audit.events[0]
	if e.ActorKind != audit.ActorKindWebhook || *e.ActorID != c.Trigger.ID || e.Action != "webhook.run" || e.Meta["source_ip"] != "140.82.112.5" {
		t.Fatalf("audit = %+v", e)
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
			if tc.stored && !rw.rec.throttled[len(rw.rec.throttled)-1] {
				t.Fatal("rejections are recorded throttled")
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

func TestReceiveLimitsDeliveriesPerSourceAddress(t *testing.T) {
	rw := newReceiveWorld(t)
	now := func() time.Time { return time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC) }
	rw.svc = triggers.NewService(triggers.Deps{
		Store: rw.rec, Templates: rw.tmpls, Repos: rw.repos, Box: fakeBox{},
		Runs: rw.runs, Audit: rw.audit, PublicURL: "https://pulsix.example.test",
		IPLimit: clientip.NewLimiter(2, time.Minute, now), Now: now, Rand: rand.Reader,
	})
	c, err := rw.svc.Create(ctx, rw.org, rw.actor, rw.templateInput())
	if err != nil {
		t.Fatal(err)
	}
	req := rw.signed(t, c, `{"ref":"v1"}`)
	unknown := triggers.Request{TriggerID: uuid.Must(uuid.NewV7()).String(), Kind: triggers.KindTemplateRun, Header: http.Header{}.Get, SourceIP: req.SourceIP}
	if res, _ := rw.svc.Receive(ctx, unknown); res.Outcome != triggers.OutcomeNotFound {
		t.Fatalf("unknown id = %+v", res)
	}
	if res, _ := rw.svc.Receive(ctx, req); res.Outcome != triggers.OutcomeTriggered {
		t.Fatalf("second delivery = %+v", res)
	}
	res, err := rw.svc.Receive(ctx, req)
	if err != nil || res.Outcome != triggers.OutcomeRateLimited || res.RetryAfter != time.Minute {
		t.Fatalf("third delivery from the same address = %+v, %v", res, err)
	}
	if len(rw.rec.deliveries) != 1 {
		t.Fatalf("an address-limited delivery must record nothing: %+v", rw.rec.deliveries)
	}
	// Another source and the in-process test call are not affected.
	other := req
	other.SourceIP = netip.MustParseAddr("192.0.2.9")
	if res, _ := rw.svc.Receive(ctx, other); res.Outcome != triggers.OutcomeTriggered {
		t.Fatalf("another address = %+v", res)
	}
	if res, err := rw.svc.TestCall(ctx, rw.org, c.Trigger.ID, nil); err != nil || res.Outcome != triggers.OutcomeTriggered {
		t.Fatalf("test call = %+v, %v", res, err)
	}
}
