package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/ansiblevars"
	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/templates"
)

// MaxBodyBytes is the hook endpoints' body limit (spec §3).
const MaxBodyBytes = 1 << 20

// HookIPLimit is how many failed calls — the ones answering the uniform
// 404 — one source address may make per minute, summed over every trigger
// and both hook endpoints, before those calls answer 429. Verified
// deliveries do not count (see Receive). It sits well above a misconfigured
// CI system's retry rate and exists so a single source cannot probe ids or
// secrets at will.
const HookIPLimit = 300

// errMappingCompile marks a stored mapping that fails to compile at
// receive time even though Create/Update already validated it (for
// example after an incompatible library upgrade): a configuration fault,
// not something the sender caused, so runTemplate routes it to
// OutcomeFailed instead of OutcomeInvalidPayload (Task 10 review, Minor 4).
var errMappingCompile = errors.New("stored mapping failed to compile")

// Request is one call to a hook endpoint.
type Request struct {
	TriggerID string                   // raw path segment
	Kind      Kind                     // which endpoint was called
	Body      []byte                   // raw body, ≤ MaxBodyBytes (the HTTP layer enforces)
	Header    func(name string) string // case-insensitive header lookup
	SourceIP  netip.Addr               // clientip-resolved
	Test      bool                     // in-process test call: allowlist skipped, reason "test call"
}

// Result is what the endpoint answers; see the HTTP mapping in hook_handlers.go.
type Result struct {
	Outcome    Outcome
	RunID      *uuid.UUID    // the first job of the run (triggered)
	Field      string        // invalid_payload: the offending field, never its value
	RetryAfter time.Duration // rate_limited
}

// Receive runs spec §3's check order. The error is reserved for internal
// failures; every refusal is an Outcome.
//
// The per-address limit counts only calls that end in the uniform 404
// (unknown or malformed id, wrong endpoint, disabled, disallowed address,
// bad signature), and once an address has spent its budget every such
// call answers the same 429. A delivery that proves the trigger's secret
// is never charged or refused by it — the per-trigger token bucket limits
// those — so an anonymous flood of unknown ids cannot starve genuine
// senders that share its address, as every caller does behind a reverse
// proxy whose address is not in PULSIX_TRUSTED_PROXIES (final review I1).
func (s *Service) Receive(ctx context.Context, req Request) (Result, error) {
	res, d, err := s.receive(ctx, req)
	if err != nil || req.Test || s.d.IPLimit == nil || !res.Outcome.hidden() {
		return res, err
	}
	if !s.d.IPLimit.Allow(req.SourceIP) {
		// receive (via finish) already recorded the real reason this call
		// was refused — rejected_ip, rejected_auth or disabled —
		// trigger_deliveries_throttle_idx keeps at most one such row per
		// trigger, outcome and minute, so repeated failures from this
		// address land on the very row being amended here. Without this,
		// the sender's actual answer (429, below) would disagree with what
		// an operator reads in that row, which still names the pre-budget
		// reason and nothing about the address limit that actually decided
		// the call. NoteRateLimited matches on (org, trigger, outcome,
		// minute) rather than d.ID precisely because the id this call
		// minted for d is almost always the very insert that got
		// deduplicated into an earlier call's row within the same minute —
		// by the id it generated itself, there would usually be nothing to
		// find.
		if noteErr := s.d.Store.NoteRateLimited(db.WithOrgScope(ctx, d.OrgID), d.OrgID, d.TriggerID, res.Outcome, d.At); noteErr != nil {
			s.d.Log.ErrorContext(ctx, "note rate-limited delivery failed", "trigger", d.TriggerID, "org", d.OrgID, "err", noteErr)
		}
		return Result{Outcome: OutcomeRateLimited, RetryAfter: s.d.IPLimit.Window()}, nil
	}
	return res, nil
}

func (s *Service) receive(ctx context.Context, req Request) (Result, Delivery, error) {
	id, err := uuid.Parse(req.TriggerID)
	if err != nil {
		return Result{Outcome: OutcomeNotFound}, Delivery{}, nil
	}
	t, sealed, err := s.d.Store.Resolve(ctx, id)
	if errors.Is(err, ErrNotFound) {
		// The signature check a known id would have run, so the
		// uniform 404 is uniform in time too (backlog 2.62).
		equaliseSignatureWork(req.Body)
		return Result{Outcome: OutcomeNotFound}, Delivery{}, nil
	}
	if err != nil {
		return Result{}, Delivery{}, err
	}
	if t.Kind != req.Kind {
		equaliseSignatureWork(req.Body)
		return Result{Outcome: OutcomeNotFound}, Delivery{}, nil
	}
	ctx = db.WithOrgScope(ctx, t.OrgID)
	now := s.d.Now()
	d := Delivery{ID: s.newID(), OrgID: t.OrgID, TriggerID: t.ID, At: now}
	if req.SourceIP.IsValid() {
		d.SourceIP = req.SourceIP.String()
	}
	if req.Test {
		d.Reason = "test call"
	}
	if !t.Enabled {
		return s.finish(ctx, d, Result{Outcome: OutcomeDisabled}, "")
	}
	if !req.Test && !allowed(t.AllowCIDRs, req.SourceIP) {
		return s.finish(ctx, d, Result{Outcome: OutcomeRejectedIP}, "")
	}
	secret, err := s.d.Box.OpenAAD(sealed, AAD(t.OrgID, t.ID))
	if err != nil {
		// The sender has not proved the secret yet: a 500 here would only
		// ever answer existing ids, so the fault hides behind the uniform
		// 404 and goes to the log (final review minor 9). Faults after the
		// signature check may answer 500 — that caller knows the secret.
		// Nothing is recorded (OutcomeNotFound never is), so there is no
		// row for the caller to later amend either.
		s.d.Log.ErrorContext(ctx, "open webhook trigger secret", "trigger", t.ID, "org", t.OrgID, "err", err)
		equaliseSignatureWork(req.Body)
		return Result{Outcome: OutcomeNotFound}, Delivery{}, nil
	}
	if !verify(t, string(secret), req.Header, req.Body, now) {
		return s.finish(ctx, d, Result{Outcome: OutcomeRejectedAuth}, "")
	}
	ok, wait, err := s.d.Store.TakeToken(ctx, t.OrgID, t.ID, t.RateLimit, now)
	if err != nil {
		return Result{}, d, err
	}
	if !ok {
		return s.finish(ctx, d, Result{Outcome: OutcomeRateLimited, RetryAfter: wait}, "")
	}
	if t.Kind == KindRepoSync {
		return s.syncRepo(ctx, t, d)
	}
	return s.runTemplate(ctx, t, d, req.Body)
}

// TestCall signs body (default {}) with the trigger's real secret and runs
// it through Receive from 127.0.0.1 with the allowlist skipped.
func (s *Service) TestCall(ctx context.Context, orgID, id uuid.UUID, body []byte) (Result, error) {
	t, err := s.d.Store.Get(ctx, orgID, id)
	if err != nil {
		return Result{}, err
	}
	_, sealed, err := s.d.Store.Resolve(ctx, id)
	if err != nil {
		return Result{}, err
	}
	secret, err := s.d.Box.OpenAAD(sealed, AAD(orgID, id))
	if err != nil {
		return Result{}, fmt.Errorf("open trigger secret: %w", err)
	}
	if len(body) == 0 {
		body = []byte("{}")
	}
	h := http.Header{}
	ts := ""
	if t.TimestampHeader != "" {
		ts = strconv.FormatInt(s.d.Now().Unix(), 10)
		h.Set(t.TimestampHeader, ts)
	}
	h.Set(t.SignatureHeader(), Sign(t.Preset, string(secret), ts, body))
	return s.Receive(ctx, Request{TriggerID: id.String(), Kind: t.Kind, Body: body, Header: h.Get,
		SourceIP: netip.MustParseAddr("127.0.0.1"), Test: true})
}

func (s *Service) runTemplate(ctx context.Context, t Trigger, d Delivery, body []byte) (Result, Delivery, error) {
	ov, field, err := s.mapPayload(ctx, t, body)
	if err != nil {
		if errors.Is(err, errMappingCompile) {
			s.d.Log.ErrorContext(ctx, "webhook trigger's stored mapping failed to compile", "trigger", t.ID, "org", t.OrgID, "err", err)
			return s.finish(ctx, d, Result{Outcome: OutcomeFailed}, "the trigger's mapping is invalid (see the manager log)")
		}
		if field == "" {
			return Result{}, d, err
		}
		return s.finish(ctx, d, Result{Outcome: OutcomeInvalidPayload, Field: field}, "invalid value at "+field)
	}
	// Check-then-enqueue, deliberately not serialised (backlog 2.62).
	// Two deliveries landing in the same instant — or one racing the
	// schedule ticker's own overlap=skip for the same template — can
	// both read "nothing active" and both enqueue. Closing that means
	// holding a per-template advisory lock across the active check AND
	// the whole enqueue, and the enqueue is not one transaction: it
	// resolves the template's scope against the inventory and creates a
	// job per zone. Serialising all of that behind one lock to turn a
	// rare duplicate into no duplicate is a poor trade — the guard
	// exists to stop runs PILING UP, which it still does, and a run
	// that does slip through is one extra run, visible in the log with
	// its own trigger.
	if t.OnActive == OnActiveSkip {
		active, err := s.d.Runs.HasActiveRun(ctx, t.OrgID, *t.TemplateID)
		if err != nil {
			return Result{}, d, err
		}
		if active {
			return s.finish(ctx, d, Result{Outcome: OutcomeSkippedActive}, "a run of the template is still active")
		}
	}
	var runOv *jobs.RunOverrides
	if !ov.Empty() {
		// A payload is untrusted input: the claim marks every mapped string
		// unsafe for Ansible, so a "{{ lookup(…) }}" in a branch name stays
		// text instead of running on the worker (final review C1).
		runOv = &jobs.RunOverrides{Limit: ov.Limit, ExtraVars: ov.ExtraVars, Untrusted: true}
	}
	ref := t.ID
	created, err := s.d.Runs.EnqueueRunWith(ctx, t.OrgID, *t.TemplateID, jobs.RunRequest{Trigger: jobs.TriggerWebhook, TriggerRef: &ref, Overrides: runOv})
	if err != nil {
		s.d.Log.ErrorContext(ctx, "webhook run could not be enqueued", "trigger", t.ID, "org", t.OrgID, "err", err)
		return s.finish(ctx, d, Result{Outcome: OutcomeFailed}, failureReason(err))
	}
	runID := created[0].ID
	d.JobGroupID = &created[0].RunGroupID
	s.auditRun(ctx, t, d, "job_template", t.TemplateID.String())
	return s.finish(ctx, d, Result{Outcome: OutcomeTriggered, RunID: &runID}, "")
}

func (s *Service) syncRepo(ctx context.Context, t Trigger, d Delivery) (Result, Delivery, error) {
	if t.OnActive == OnActiveSkip {
		active, err := s.d.Runs.HasActiveSync(ctx, t.OrgID, *t.RepoID)
		if err != nil {
			return Result{}, d, err
		}
		if active {
			return s.finish(ctx, d, Result{Outcome: OutcomeSkippedActive}, "a sync of the repository is still active")
		}
	}
	ref := t.ID
	j, err := s.d.Runs.EnqueueSyncWith(ctx, t.OrgID, *t.RepoID, jobs.RunRequest{Trigger: jobs.TriggerWebhook, TriggerRef: &ref})
	if err != nil {
		s.d.Log.ErrorContext(ctx, "webhook sync could not be enqueued", "trigger", t.ID, "org", t.OrgID, "err", err)
		return s.finish(ctx, d, Result{Outcome: OutcomeFailed}, failureReason(err))
	}
	runID := j.ID
	d.JobGroupID = &j.RunGroupID
	s.auditRun(ctx, t, d, "repo", t.RepoID.String())
	return s.finish(ctx, d, Result{Outcome: OutcomeTriggered, RunID: &runID}, "")
}

// mapPayload applies the mapping and validates the result like a manual
// run, with secrets refused and the limit held to plain host patterns (no
// "@file", regex or subscript patterns — ansiblevars.CheckUntrustedLimit).
// A non-empty field marks a payload problem; an empty field with an error
// is an internal failure.
func (s *Service) mapPayload(ctx context.Context, t Trigger, body []byte) (templates.Overrides, string, error) {
	if t.Mapping.empty() {
		return templates.Overrides{}, "", nil
	}
	c, err := compileMapping(t.Mapping)
	if err != nil {
		return templates.Overrides{}, "", fmt.Errorf("%w: %w", errMappingCompile, err)
	}
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return templates.Overrides{}, "body", errors.New("the body is not JSON")
	}
	ov, field, err := c.apply(doc)
	if err != nil || ov.Empty() {
		return ov, field, err
	}
	// A sender-chosen "__ansible…" key, or nesting past
	// ansiblevars.MaxUntrustedDepth, would make Ansible's JSON load of the
	// extra-vars file fail and fall back to YAML, which drops every unsafe
	// marker (final re-reviews 1 and 2). The claim checks again.
	for _, name := range slices.Sorted(maps.Keys(ov.ExtraVars)) {
		if err := ansiblevars.CheckUntrusted(ov.ExtraVars[name]); err != nil {
			return templates.Overrides{}, "extra_vars." + name, err
		}
	}
	checked, err := s.d.Templates.ValidateOverrides(ctx, t.OrgID, *t.TemplateID, ov, templates.OverrideOptions{RefuseSecrets: true, StrictLimit: true})
	var ve *schema.ValidationError
	switch {
	case err == nil:
		return checked, "", nil
	case errors.As(err, &ve) && len(ve.Problems) > 0:
		return templates.Overrides{}, "extra_vars." + firstSegment(ve.Problems[0].Path), err
	case errors.Is(err, templates.ErrInvalidInput):
		return templates.Overrides{}, "limit", err
	}
	return templates.Overrides{}, "", err
}

func (s *Service) finish(ctx context.Context, d Delivery, res Result, reason string) (Result, Delivery, error) {
	d.Outcome = res.Outcome
	switch {
	case d.Reason == "":
		d.Reason = reason
	case reason != "":
		d.Reason += "; " + reason
	}
	if err := s.d.Store.RecordDelivery(ctx, d); err != nil {
		// Recording is best-effort bookkeeping, not part of the request's
		// own effect — the same principle the audit middleware applies
		// elsewhere: an outcome that was already decided (a run already
		// enqueued, a rejection already refused) must not turn into a 500
		// merely because writing its delivery row failed, or a sender
		// that retries on 5xx (GitHub, GitLab, ...) would start a
		// duplicate run (Task 10 review, Minor 6).
		s.d.Log.ErrorContext(ctx, "record delivery failed", "trigger", d.TriggerID, "org", d.OrgID, "outcome", res.Outcome, "err", err)
	}
	return res, d, nil
}

// firstSegment returns path up to (not including) its first "." or "[", so
// a nested validation problem never echoes a key or array index taken from
// the sender's own payload — only the configured top-level mapping target
// reaches the 422 response and the delivery reason (Task 10 review, Minor 3).
func firstSegment(path string) string {
	if i := strings.IndexAny(path, ".["); i >= 0 {
		return path[:i]
	}
	return path
}

func (s *Service) auditRun(ctx context.Context, t Trigger, d Delivery, objectType, objectID string) {
	if s.d.Audit == nil {
		return
	}
	orgID, actor := t.OrgID, t.ID
	meta := map[string]any{"trigger_id": t.ID.String(), "source_ip": d.SourceIP}
	if d.JobGroupID != nil {
		meta["job_group_id"] = d.JobGroupID.String()
	}
	if d.Reason == "test call" {
		meta["test_call"] = true
	}
	if err := s.d.Audit.Record(ctx, audit.Event{
		OrgID: &orgID, ActorKind: audit.ActorKindWebhook, ActorID: &actor, Action: "webhook.run",
		ObjectType: objectType, ObjectID: objectID, Summary: fmt.Sprintf("webhook %q started a run", t.Name), Meta: meta,
	}); err != nil {
		s.d.Log.ErrorContext(ctx, "audit webhook run", "trigger", t.ID, "err", err)
	}
}

func allowed(cidrs []netip.Prefix, ip netip.Addr) bool {
	if len(cidrs) == 0 {
		return true
	}
	if !ip.IsValid() {
		return false
	}
	for _, p := range cidrs {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// failureReason is the generic reason a failed delivery records.
func failureReason(err error) string {
	switch {
	case errors.Is(err, jobs.ErrEmptyScope):
		return "the template's scope resolves to no host"
	case errors.Is(err, jobs.ErrHostKeyChanged):
		return "every targeted host has a changed host key"
	case errors.Is(err, jobs.ErrNoZone):
		return "the organization has no default zone"
	// schedules/service.go's own case, same sentinel, same reasoning: a
	// projection of an installation-owned definition (spec v0.2f) whose
	// playbook has not synced in this organization yet.
	case errors.Is(err, jobs.ErrTemplateNotReady):
		return "the template's playbook has not been registered in this organization yet"
	}
	return "could not enqueue (see the manager log)"
}
