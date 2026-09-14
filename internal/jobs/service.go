// Package jobs is the job queue (spec §7, backlog 2.11): job templates
// turn into per-zone jobs at enqueue time, zone workers claim them over
// gRPC, stream events while they run and report a terminal status. The
// package splits in two halves with deliberately different scoping:
//
//   - Service and SQLStore are org-scoped, built on *db.DB, so every
//     statement runs inside the request's row-level-security scope.
//   - SQLQueue is the ONE org-unscoped store in the system (plan Global
//     Constraints): a worker claims across organizations, so the claim,
//     event-append, finish and reaper statements run on the raw pool with
//     explicit org_id columns, documented at each statement.
//
// The gRPC server (internal/grpcsrv) consumes the queue through the narrow
// Claimer/EventRecorder/ResultRecorder ports; internal/app adapts.
package jobs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// Sentinel errors mapped to HTTP status by the transport layer.
var (
	ErrInvalidInput = errors.New("invalid input")
	ErrNotFound     = errors.New("not found")
	// ErrEmptyScope means a template's scope resolves to no managed host.
	ErrEmptyScope = errors.New("scope resolves to no host")
	// ErrNotRunning means the job is not in a state the operation applies
	// to: cancelling a terminal job, or finishing one that never ran.
	ErrNotRunning = errors.New("job is not running")
	// ErrNothingToRetry means a finished run has no failed or unreachable
	// host to retry.
	ErrNothingToRetry = errors.New("nothing to retry")
	// ErrUnknownStream means no live claim binding exists for the job —
	// the token is unknown, expired, already used, or the job finished.
	ErrUnknownStream = errors.New("no live stream binding for job")
	// ErrInvalidCursor is returned by DecodeCursor for a malformed cursor.
	ErrInvalidCursor = errors.New("jobs: invalid cursor")
)

// Kind is what a job does.
type Kind string

// Job kinds. run goes through EnqueueRun (job templates); every other kind
// targets explicit hosts through EnqueueHostJob.
const (
	KindRun            Kind = "run"
	KindAdhoc          Kind = "adhoc"
	KindConnectionTest Kind = "connection_test"
	KindBootstrap      Kind = "bootstrap"
	KindRotateKey      Kind = "rotate_key"
	KindSyncRepo       Kind = "sync_repo"
)

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	switch k {
	case KindRun, KindAdhoc, KindConnectionTest, KindBootstrap, KindRotateKey, KindSyncRepo:
		return true
	}
	return false
}

// IsHostKind reports whether k targets explicit hosts (everything but run).
func (k Kind) IsHostKind() bool { return k.Valid() && k != KindRun }

// Status is a job's lifecycle state.
type Status string

// Job statuses. queued → running happens in one claim transaction (the
// brief's claimed state is collapsed into running at claim time — see
// ClaimJob's comment in queries/jobs.sql); running → cancelling when an
// operator asks for a cancel; the four Terminal statuses end the run.
// claimed is part of the schema's CHECK for forward compatibility but is
// never persisted by v0.2b.
const (
	StatusQueued     Status = "queued"
	StatusClaimed    Status = "claimed"
	StatusRunning    Status = "running"
	StatusCancelling Status = "cancelling"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
	StatusCancelled  Status = "cancelled"
	StatusError      Status = "error"
)

// Valid reports whether s is one of the eight known statuses.
func (s Status) Valid() bool {
	switch s {
	case StatusQueued, StatusClaimed, StatusRunning, StatusCancelling,
		StatusSucceeded, StatusFailed, StatusCancelled, StatusError:
		return true
	}
	return false
}

// Terminal reports whether s ends a run: only these statuses come out of
// Finish, and only they accept a retry.
func (s Status) Terminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCancelled, StatusError:
		return true
	}
	return false
}

// Trigger names what put the job in the queue. v0.2b enqueues manually
// (manual); the scheduler (system) arrives later.
const (
	TriggerManual = "manual"
	TriggerSystem = "system"
)

// Stats keys the worker reports in its terminal Result (the producer side
// lands with the worker runner, Task 10). failed_hosts and unreachable_hosts
// carry host UUIDs as strings — the worker knows them from the claim
// payload's Connections map — and RetryFailed reads exactly these keys.
const (
	StatsFailedHosts      = "failed_hosts"
	StatsUnreachableHosts = "unreachable_hosts"
)

// Job is one unit of work for a zone worker.
type Job struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	ZoneID     uuid.UUID
	Kind       Kind
	TemplateID *uuid.UUID
	// HostIDs are the explicit targets of the host kinds; for run jobs the
	// per-zone subset of the template's scope the job covers.
	HostIDs    []uuid.UUID
	Status     Status
	WorkerID   *uuid.UUID
	ClaimedAt  *time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	Stats      map[string]any
	Error      string
	// RunGroupID groups the per-zone jobs of one trigger, so the run view
	// can show them together and RetryFailed can find its template.
	RunGroupID uuid.UUID
	Trigger    string
	ActorID    *uuid.UUID
	CreatedAt  time.Time
	// Payload is the kind-specific JSON. For the host kinds that can carry
	// secrets (bootstrap login, rotation keys) the service seals it with
	// the box before insert and the queue opens it at claim time; the
	// stored form is a base64 JSON string. Never logged, never served.
	Payload json.RawMessage
}

// Event is one run event a worker streamed in, as stored in job_events.
type Event struct {
	Seq     uint64
	Host    string
	Task    string
	State   string
	Payload json.RawMessage
	At      time.Time
}

// JobAAD is the additional data a job's sealed payload is boxed with, so a
// ciphertext copied into another row or organization no longer opens.
func JobAAD(orgID, jobID uuid.UUID) []byte {
	return []byte("job:" + orgID.String() + ":" + jobID.String())
}

// Filter narrows List. A zero Filter matches every job of the organization.
type Filter struct {
	ZoneID *uuid.UUID
	Status *Status
	Kind   *Kind
}

// DefaultListLimit and MaxListLimit bound List's page size, the same
// clamping shape hosts.Service.ListHosts uses.
const (
	DefaultListLimit = 20
	MaxListLimit     = 100
)

func clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultListLimit
	case limit > MaxListLimit:
		return MaxListLimit
	default:
		return limit
	}
}

// EncodeCursor/DecodeCursor are the one keyset-cursor format the store
// implementations use, over (created_at, id) descending — the same shape
// hosts.EncodeCursor gives (name, id). An empty cursor decodes to the zero
// time and uuid.Nil with no error: the "first page" case.
func EncodeCursor(at time.Time, id uuid.UUID) string {
	raw := at.Format(time.RFC3339Nano) + "|" + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func DecodeCursor(cursor string) (time.Time, uuid.UUID, error) {
	if cursor == "" {
		return time.Time{}, uuid.Nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	// Neither an RFC3339Nano timestamp nor a UUID contains "|", so the
	// first "|" separates the keyset pair.
	atStr, idStr, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	at, err := time.Parse(time.RFC3339Nano, atStr)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	return at, id, nil
}

// Store persists jobs org-scoped. Every lookup that finds nothing returns
// ErrNotFound.
type Store interface {
	// CreateJob inserts one queued job; payload is the (already sealed)
	// kind-specific JSON or nil.
	CreateJob(ctx context.Context, j Job, payload json.RawMessage) error
	GetJob(ctx context.Context, orgID, id uuid.UUID) (Job, error)
	// ListJobs returns a keyset-paginated page ordered by (created_at, id)
	// descending, plus the cursor for the next page (empty when there is
	// none).
	ListJobs(ctx context.Context, orgID uuid.UUID, f Filter, cursor string, limit int) ([]Job, string, error)
	// CancelJob atomically moves the job out of the cancellable set:
	// queued → cancelled (never shipped, terminal at once), claimed/running
	// → cancelling. ErrNotRunning when no row matched — after the caller's
	// own GetJob that means the job left the cancellable set (or was
	// deleted in between, which is the same "nothing to cancel" answer).
	CancelJob(ctx context.Context, orgID, id uuid.UUID) (Status, error)
	// ListJobEvents returns the job's events with seq > after, ordered by
	// seq. The org_id filter is what keeps one tenant from reading
	// another's events on the RLS-free job_events table.
	ListJobEvents(ctx context.Context, orgID, jobID uuid.UUID, after int64) ([]Event, error)
}

// Claim is one claimed job ready to ship to a worker, with the binding
// token that pairs it with its RunEvents stream.
type Claim struct {
	Job   workerpb.Job
	Token string
}

// Queue is the claim port the gRPC server calls (through internal/app's
// adapters). Org-unscoped by design: the claim is keyed by zone, and the
// worker's zones are trusted because the connection already authenticated
// as a registered worker.
type Queue interface {
	// Claim picks one queued job for any of the zones, marks it running
	// and returns its payload plus a fresh binding token. A nil claim
	// means the queues behind the zones are empty.
	Claim(ctx context.Context, zones []uuid.UUID, version string) (*Claim, error)
	// BindStream validates a claim's binding token and returns the stream
	// the run writes into. ErrUnknownStream for an unknown, expired or
	// already-consumed token.
	BindStream(ctx context.Context, jobID uuid.UUID, token string) (Stream, error)
	// Finish records the terminal status. Idempotent: finishing an already
	// terminal job is a no-op (a worker retrying ReportResult after a
	// failed audit write must not corrupt the record).
	Finish(ctx context.Context, jobID uuid.UUID, status Status, stats map[string]any, errText string) error
}

// CancelRecorder is the optional queue hook Service.Cancel uses to record
// when a cancel was requested, so the reaper can fail a cancelling job
// whose worker never confirmed. The in-process timestamp shares the
// registry's multi-instance limitation, documented at SQLQueue.
type CancelRecorder interface {
	NoteCancelRequested(jobID uuid.UUID, at time.Time)
}

// TemplateRow is the job-template row the enqueue and claim paths need,
// joined with its playbook. The templates service (Task 7) owns the full
// CRUD surface; this task reads the row directly.
type TemplateRow struct {
	ID    uuid.UUID
	OrgID uuid.UUID
	// PlaybookID references the playbooks row; the playbook fields below
	// come from that row in the same lookup.
	PlaybookID     uuid.UUID
	PlaybookRepo   *uuid.UUID
	PlaybookPath   string
	PlaybookInline string
	// Scope is the render.Scope wire format (group_id/host_ids) as stored.
	Scope           json.RawMessage
	Limit           string
	ExtraVars       json.RawMessage
	ExtraVarsSchema json.RawMessage
	CredentialID    *uuid.UUID
}

// TemplateSource looks one template up, ErrNotFound when it does not exist
// in the organization.
type TemplateSource interface {
	TemplateLookup(ctx context.Context, orgID, id uuid.UUID) (TemplateRow, error)
}

// ZoneTargets is the set of hosts a job covers in one zone.
type ZoneTargets struct {
	ZoneID  uuid.UUID
	HostIDs []uuid.UUID
}

// ScopeResolver turns a template's scope into the per-zone host lists the
// enqueue path splits jobs along.
type ScopeResolver interface {
	Resolve(ctx context.Context, orgID uuid.UUID, scope render.Scope) ([]ZoneTargets, error)
}

// HostLookup is the slice of hosts.Service the host kinds need: every
// target must exist and be managed.
type HostLookup interface {
	GetHost(ctx context.Context, orgID, id uuid.UUID) (hosts.Host, error)
}

// Sealer is the slice of crypto.Box both halves need: the service seals
// host-job payloads, the queue opens them at claim time.
type Sealer interface {
	SealAAD(plain, aad []byte) ([]byte, error)
	OpenAAD(sealed, aad []byte) ([]byte, error)
}

// Service is the org-scoped half of the job queue.
type Service struct {
	store     Store
	queue     Queue
	templates TemplateSource
	resolver  ScopeResolver
	hosts     HostLookup
	box       Sealer
	now       func() time.Time
	rand      io.Reader
}

// NewService wires the store, the claim queue, the template source, the
// scope resolver, the host lookup, the sealing box, the clock and the
// entropy source for run-group ids.
func NewService(store Store, queue Queue, templates TemplateSource, resolver ScopeResolver, hosts HostLookup, box Sealer, now func() time.Time, rand io.Reader) *Service {
	return &Service{store: store, queue: queue, templates: templates, resolver: resolver, hosts: hosts, box: box, now: now, rand: rand}
}

// scopeJSON is the render.Scope wire format as stored in
// job_templates.scope (plan: group_id/host_ids/null).
type scopeJSON struct {
	GroupID *uuid.UUID  `json:"group_id"`
	HostIDs []uuid.UUID `json:"host_ids"`
}

func parseScope(raw json.RawMessage) (render.Scope, error) {
	var sj scopeJSON
	if err := json.Unmarshal(raw, &sj); err != nil {
		return render.Scope{}, fmt.Errorf("%w: scope is not a valid scope document", ErrInvalidInput)
	}
	return render.Scope{GroupID: sj.GroupID, HostIDs: sj.HostIDs}, nil
}

// EnqueueRun validates the template, resolves its scope into per-zone host
// lists and inserts one job per involved zone, all sharing a run group.
// The snapshot is NOT rendered here: it is rendered at claim time, so the
// worker runs against the inventory as it looks when the run starts, and
// nothing secret is ever stored on the job row.
func (s *Service) EnqueueRun(ctx context.Context, orgID, templateID uuid.UUID, actor *uuid.UUID) ([]Job, error) {
	tmpl, err := s.templates.TemplateLookup(ctx, orgID, templateID)
	if err != nil {
		return nil, err
	}
	scope, err := parseScope(tmpl.Scope)
	if err != nil {
		return nil, err
	}
	return s.enqueueRunScope(ctx, orgID, tmpl.ID, scope, actor)
}

// newID generates a UUIDv7 from the injected entropy source. An entropy
// failure is fatal, matching uuid.Must's contract everywhere else in the
// codebase.
func (s *Service) newID() uuid.UUID {
	id, err := uuid.NewV7FromReader(s.rand)
	if err != nil {
		panic(err) //nolint:forbidigo // entropy failure is unrecoverable, like uuid.Must
	}
	return id
}

// enqueueRunScope is the zone-splitting core EnqueueRun and RetryFailed
// share.
func (s *Service) enqueueRunScope(ctx context.Context, orgID, templateID uuid.UUID, scope render.Scope, actor *uuid.UUID) ([]Job, error) {
	targets, err := s.resolver.Resolve(ctx, orgID, scope)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, ErrEmptyScope
	}
	group := s.newID()
	now := s.now()
	tmplID := templateID
	out := make([]Job, 0, len(targets))
	for _, t := range targets {
		j := Job{
			ID: s.newID(), OrgID: orgID, ZoneID: t.ZoneID,
			Kind: KindRun, TemplateID: &tmplID, HostIDs: t.HostIDs,
			Status: StatusQueued, RunGroupID: group, Trigger: TriggerManual,
			ActorID: actor, CreatedAt: now,
		}
		if err := s.store.CreateJob(ctx, j, nil); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

// EnqueueHostJob enqueues connection_test / bootstrap / rotate_key / adhoc
// / sync_repo jobs for explicit hosts: one job per zone the hosts live in,
// all sharing a run group. The payload is sealed with the box (AAD
// job:<orgID>:<jobID>) before insert, because bootstrap and rotate_key
// carry secrets. The returned Job is the first of the group; callers that
// need every job of the group list by RunGroupID.
func (s *Service) EnqueueHostJob(ctx context.Context, orgID uuid.UUID, kind Kind, hostIDs []uuid.UUID, payload json.RawMessage, actor *uuid.UUID) (Job, error) {
	if !kind.IsHostKind() {
		return Job{}, fmt.Errorf("%w: kind must be one of adhoc, connection_test, bootstrap, rotate_key, sync_repo (run goes through EnqueueRun)", ErrInvalidInput)
	}
	if len(hostIDs) == 0 {
		return Job{}, fmt.Errorf("%w: at least one host is required", ErrInvalidInput)
	}
	// Validate every host first, so a bad id fails the whole enqueue
	// before any job row exists.
	byZone := map[uuid.UUID][]uuid.UUID{}
	var zoneOrder []uuid.UUID
	for _, id := range hostIDs {
		h, err := s.hosts.GetHost(ctx, orgID, id)
		if err != nil {
			// A host from another org must be as invisible as one that
			// does not exist: map hosts' own sentinel onto this package's.
			if errors.Is(err, hosts.ErrNotFound) {
				err = ErrNotFound
			}
			return Job{}, fmt.Errorf("host %s: %w", id, err)
		}
		if !h.Managed {
			return Job{}, fmt.Errorf("%w: host %s is not managed", ErrInvalidInput, id)
		}
		if _, ok := byZone[h.ZoneID]; !ok {
			zoneOrder = append(zoneOrder, h.ZoneID)
		}
		if !slices.Contains(byZone[h.ZoneID], id) {
			byZone[h.ZoneID] = append(byZone[h.ZoneID], id)
		}
	}
	group := s.newID()
	now := s.now()
	var first Job
	for i, zoneID := range zoneOrder {
		j := Job{
			ID: s.newID(), OrgID: orgID, ZoneID: zoneID,
			Kind: kind, HostIDs: byZone[zoneID],
			Status: StatusQueued, RunGroupID: group, Trigger: TriggerManual,
			ActorID: actor, CreatedAt: now,
		}
		stored := json.RawMessage(nil)
		if len(payload) > 0 {
			sealed, err := s.sealPayload(j, payload)
			if err != nil {
				return Job{}, err
			}
			stored = sealed
		}
		if err := s.store.CreateJob(ctx, j, stored); err != nil {
			return Job{}, err
		}
		if i == 0 {
			first = j
		}
	}
	return first, nil
}

// sealPayload seals the kind-specific JSON and returns the stored jsonb
// form: a JSON string carrying the base64 ciphertext.
func (s *Service) sealPayload(j Job, payload json.RawMessage) (json.RawMessage, error) {
	sealed, err := s.box.SealAAD(payload, JobAAD(j.OrgID, j.ID))
	if err != nil {
		return nil, fmt.Errorf("seal job payload: %w", err)
	}
	enc := base64.StdEncoding.EncodeToString(sealed)
	return json.RawMessage(`"` + enc + `"`), nil
}

// Cancel asks a job to stop. A queued job has never been shipped, so it is
// cancelled outright; a claimed/running job enters cancelling and the
// worker confirms through its terminal ReportResult. The cancel-request
// timestamp is recorded on the queue (when it implements CancelRecorder)
// for the reaper's confirmation deadline.
func (s *Service) Cancel(ctx context.Context, orgID, id uuid.UUID) error {
	if _, err := s.store.GetJob(ctx, orgID, id); err != nil {
		return err
	}
	status, err := s.store.CancelJob(ctx, orgID, id)
	if err != nil {
		return err
	}
	if status == StatusCancelling {
		if cr, ok := s.queue.(CancelRecorder); ok {
			cr.NoteCancelRequested(id, s.now())
		}
	}
	return nil
}

// RetryFailed re-runs only the hosts a finished run failed on: it collects
// the failed and unreachable host ids from the finished job's stats,
// narrows the template's scope to them and enqueues a new run group. Only
// terminal failed/error run jobs can be retried.
func (s *Service) RetryFailed(ctx context.Context, orgID, id uuid.UUID, actor *uuid.UUID) ([]Job, error) {
	j, err := s.store.GetJob(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if j.Kind != KindRun {
		return nil, fmt.Errorf("%w: only run jobs can be retried", ErrInvalidInput)
	}
	if !j.Status.Terminal() {
		return nil, fmt.Errorf("%w: job has not finished yet", ErrNotRunning)
	}
	if j.Status != StatusFailed && j.Status != StatusError {
		return nil, fmt.Errorf("%w: only failed or errored runs can be retried", ErrNothingToRetry)
	}
	if j.TemplateID == nil {
		return nil, fmt.Errorf("%w: job carries no template", ErrInvalidInput)
	}
	retry := failedHostIDs(j.Stats, j.HostIDs)
	if len(retry) == 0 {
		return nil, ErrNothingToRetry
	}
	tmpl, err := s.templates.TemplateLookup(ctx, orgID, *j.TemplateID)
	if err != nil {
		return nil, err
	}
	return s.enqueueRunScope(ctx, orgID, tmpl.ID, render.Scope{HostIDs: retry}, actor)
}

// failedHostIDs intersects the stats' failed/unreachable host ids with the
// job's own targets, so a worker reporting stale ids can never widen a
// retry beyond the original scope.
func failedHostIDs(stats map[string]any, targets []uuid.UUID) []uuid.UUID {
	want := make(map[uuid.UUID]bool, len(targets))
	for _, id := range targets {
		want[id] = true
	}
	var out []uuid.UUID
	seen := map[uuid.UUID]bool{}
	for _, key := range []string{StatsFailedHosts, StatsUnreachableHosts} {
		raw, ok := stats[key]
		if !ok {
			continue
		}
		list, ok := raw.([]any)
		if !ok {
			continue
		}
		for _, v := range list {
			s, ok := v.(string)
			if !ok {
				continue
			}
			id, err := uuid.Parse(s)
			if err != nil || !want[id] || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// Get returns one job, ErrNotFound when it does not exist in the org.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (Job, error) {
	return s.store.GetJob(ctx, orgID, id)
}

// List returns a keyset-paginated page of the org's jobs, newest first.
func (s *Service) List(ctx context.Context, orgID uuid.UUID, f Filter, cursor string, limit int) ([]Job, string, error) {
	return s.store.ListJobs(ctx, orgID, f, cursor, clampLimit(limit))
}

// Events returns the job's events with seq greater than after, ordered by
// seq. The job is resolved first, so a job id from another organization is
// exactly as invisible as one that does not exist — and the event read
// below is filtered by the resolved org, never by the caller's claim.
func (s *Service) Events(ctx context.Context, orgID, id uuid.UUID, after int64) ([]Event, error) {
	if _, err := s.store.GetJob(ctx, orgID, id); err != nil {
		return nil, err
	}
	return s.store.ListJobEvents(ctx, orgID, id, after)
}
