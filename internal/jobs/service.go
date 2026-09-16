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
	// ErrNoZone means the organization has no "default" zone for a sync_repo
	// job to run in — a wiring gap the startup reconciliation pass
	// (hosts.Service.ReconcileDefaultZones) is supposed to have healed.
	ErrNoZone = errors.New("organization has no default zone")
	// ErrHostKeyChanged means every host a run's scope resolved to has a
	// changed host key (spec §5 block-on-mismatch): the enqueue fails until
	// an operator re-pins the hosts. A partially affected scope keeps the
	// good hosts and is not this error.
	ErrHostKeyChanged = errors.New("all targeted hosts have a changed host key")
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
	// Attempts counts how often the job was claimed (the claim increments
	// it); the reaper stops requeuing a stale claim at the maximum.
	Attempts   int
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
	// ActorName, WorkerName and TemplateName are the display names the
	// run view shows for those ids, resolved by SQLStore.GetJob's display
	// joins (empty when the id is NULL or the referenced row is gone — a
	// deleted template reads as no name; the API shows "(deleted)"). They
	// are display-only: nothing writes them back, and every other read
	// leaves them zero.
	ActorName    string
	WorkerName   string
	TemplateName string
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
	// TemplateID narrows to one template's jobs; RunGroupID to one
	// trigger's per-zone group (the run view's grouped-run listing).
	TemplateID *uuid.UUID
	RunGroupID *uuid.UUID
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
	// RecordSyncResult stamps a sync_repo job's terminal outcome onto the
	// repository row it ran for: last_sync carries at, last_sync_error the
	// failure reason (an empty errText clears it). ErrNotFound when no
	// repository of the organization matches — e.g. one deleted while its
	// sync was running.
	RecordSyncResult(ctx context.Context, orgID, repoID uuid.UUID, at time.Time, errText string) error
}

// Claim is one claimed job ready to ship to a worker, with the binding
// token that pairs it with its RunEvents stream.
type Claim struct {
	Job   workerpb.Job
	Token string
}

// Queue is the claim port the gRPC server calls (through internal/app's
// adapters). It runs outside any request's organization scope, but nothing
// a worker sends is trusted on its own: the gRPC layer accepts only the
// zone the worker's certificate was enrolled for, the claim filters on the
// worker's own organization, the claimed row records the worker
// (SQLQueue.ClaimFor), and the terminal result is accepted only from that
// same worker for a job of its organization and zone
// (SQLQueue.FinishFor); RunEvents is bound by the one-time claim token.
type Queue interface {
	// Claim picks one queued job of orgID for any of the zones, marks it
	// running and returns its payload plus a fresh binding token. A nil
	// claim means the queues behind the zones are empty. orgID is the
	// claiming worker's own organization: a job of any other organization
	// is never returned, whatever zone ids the caller names.
	Claim(ctx context.Context, orgID uuid.UUID, zones []uuid.UUID, version string) (*Claim, error)
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
	zones     DefaultZoneSource
	box       Sealer
	now       func() time.Time
	rand      io.Reader
	// keyGuard is the ChangedHosts source for EnqueueRun's block-on-
	// mismatch. Resolved once at construction from the HostLookup's own
	// type: hosts.Service implements it, unit-test fakes that do not
	// simply leave the guard off.
	keyGuard KeyGuardSource
	// rotationTargets is the credential-referencing store the rotation
	// enqueue needs, resolved from the queue's own type (SQLQueue
	// implements it with org-scoped reads); a queue that does not leaves
	// rotation enqueueing fail-closed.
	rotationTargets RotationTargets
	// broker is the run events' in-process SSE fan-out, created here and
	// handed to the queue after construction (SetEventBroker) so the
	// append path feeds it. Subscribe is the only reader.
	broker *Broker
}

// NewService wires the store, the claim queue, the template source, the
// scope resolver, the host lookup, the default-zone source, the sealing
// box, the clock and the entropy source for run-group ids. When the host
// lookup implements KeyGuardSource — hosts.Service always does — EnqueueRun
// blocks on changed host keys.
func NewService(store Store, queue Queue, templates TemplateSource, resolver ScopeResolver, hosts HostLookup, zones DefaultZoneSource, box Sealer, now func() time.Time, rand io.Reader) *Service {
	s := &Service{store: store, queue: queue, templates: templates, resolver: resolver, hosts: hosts, zones: zones, box: box, now: now, rand: rand}
	if g, ok := hosts.(KeyGuardSource); ok {
		s.keyGuard = g
	}
	if rt, ok := queue.(RotationTargets); ok {
		s.rotationTargets = rt
	}
	s.broker = NewBroker()
	return s
}

// KeyGuardSource is the slice of hosts.Service EnqueueRun's block-on-
// mismatch needs: which of the given hosts' LATEST connection test reports
// a changed host key. Declared here, next to its consumer, so unit tests
// can drive fakes and internal/app wires the real hosts service.
type KeyGuardSource interface {
	ChangedHosts(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (map[uuid.UUID]bool, error)
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
	// Block on mismatch (spec §5): hosts whose latest connection test
	// reports a changed host key are excluded from the scope — a run must
	// never SSH into a host whose key stopped matching its pin. When that
	// empties the whole scope the enqueue fails with ErrHostKeyChanged.
	if s.keyGuard != nil {
		targets, err = s.excludeKeyChanged(ctx, orgID, targets)
		if err != nil {
			return nil, err
		}
		if len(targets) == 0 {
			return nil, ErrHostKeyChanged
		}
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

// excludeKeyChanged drops every targeted host whose latest connection test
// reports a changed host key from the per-zone targets. A guard failure
// fails the enqueue closed.
func (s *Service) excludeKeyChanged(ctx context.Context, orgID uuid.UUID, targets []ZoneTargets) ([]ZoneTargets, error) {
	ids := make([]uuid.UUID, 0, len(targets))
	seen := map[uuid.UUID]bool{}
	for _, t := range targets {
		for _, id := range t.HostIDs {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return targets, nil
	}
	changed, err := s.keyGuard.ChangedHosts(ctx, orgID, ids)
	if err != nil {
		return nil, fmt.Errorf("resolve changed host keys: %w", err)
	}
	if len(changed) == 0 {
		return targets, nil
	}
	out := make([]ZoneTargets, 0, len(targets))
	for _, t := range targets {
		kept := t.HostIDs[:0:0]
		for _, id := range t.HostIDs {
			if !changed[id] {
				kept = append(kept, id)
			}
		}
		if len(kept) > 0 {
			out = append(out, ZoneTargets{ZoneID: t.ZoneID, HostIDs: kept})
		}
	}
	return out, nil
}

// EnqueueHostJob enqueues connection_test / bootstrap / rotate_key / adhoc
// / sync_repo jobs for explicit hosts: one job per zone the hosts live in,
// all sharing a run group. The payload is sealed with the box (AAD
// job:<orgID>:<jobID>) before insert, because bootstrap and rotate_key
// carry secrets. The returned Job is the first of the group; callers that
// need every job of the group list by RunGroupID. trigger names what put
// the job in the queue — manual for operator actions, system for the
// background scheduler.
func (s *Service) EnqueueHostJob(ctx context.Context, orgID uuid.UUID, kind Kind, hostIDs []uuid.UUID, payload json.RawMessage, trigger string, actor *uuid.UUID) (Job, error) {
	if !kind.IsHostKind() {
		return Job{}, fmt.Errorf("%w: kind must be one of adhoc, connection_test, bootstrap, rotate_key, sync_repo (run goes through EnqueueRun)", ErrInvalidInput)
	}
	if len(hostIDs) == 0 {
		return Job{}, fmt.Errorf("%w: at least one host is required", ErrInvalidInput)
	}
	if trigger == "" {
		trigger = TriggerManual
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
	created, err := s.insertHostJobs(ctx, orgID, kind, byZone, zoneOrder, payload, trigger, actor)
	if err != nil {
		return Job{}, err
	}
	return created[0], nil
}

// insertHostJobs is the per-zone insert core EnqueueHostJob and
// EnqueueAdhoc share: one job per zone in zoneOrder's order, all sharing a
// fresh run group, the kind payload (when any) sealed with the box under
// the job's own AAD before insert. Callers have already validated their
// targets; zoneOrder is never empty for a validated enqueue.
func (s *Service) insertHostJobs(ctx context.Context, orgID uuid.UUID, kind Kind, byZone map[uuid.UUID][]uuid.UUID, zoneOrder []uuid.UUID, payload json.RawMessage, trigger string, actor *uuid.UUID) ([]Job, error) {
	group := s.newID()
	now := s.now()
	created := make([]Job, 0, len(zoneOrder))
	for _, zoneID := range zoneOrder {
		j := Job{
			ID: s.newID(), OrgID: orgID, ZoneID: zoneID,
			Kind: kind, HostIDs: byZone[zoneID],
			Status: StatusQueued, RunGroupID: group, Trigger: trigger,
			ActorID: actor, CreatedAt: now,
		}
		stored := json.RawMessage(nil)
		if len(payload) > 0 {
			sealed, err := s.sealPayload(j, payload)
			if err != nil {
				return nil, err
			}
			stored = sealed
		}
		if err := s.store.CreateJob(ctx, j, stored); err != nil {
			return nil, err
		}
		created = append(created, j)
	}
	return created, nil
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

// syncPayload is a sync_repo job's kind payload: the one reference the
// worker needs. It carries no secret, so it is stored plain — never sealed.
type syncPayload struct {
	RepoID uuid.UUID `json:"repo_id"`
}

// EnqueueSync enqueues one sync_repo job for the repository. sync_repo is
// deliberately NOT host-bound: it runs once, in the organization's default
// zone (resolved through the DefaultZoneSource port), so the git fetch
// happens regardless of how many hosts the org manages and without
// rendering any inventory snapshot. The payload is plain JSON — nothing in
// it is secret — so the claim path passes it through unsealed.
func (s *Service) EnqueueSync(ctx context.Context, orgID, repoID uuid.UUID, actor *uuid.UUID) (Job, error) {
	zoneID, err := s.defaultZone(ctx, orgID)
	if err != nil {
		return Job{}, err
	}
	payload, err := json.Marshal(syncPayload{RepoID: repoID})
	if err != nil {
		return Job{}, fmt.Errorf("encode sync payload: %w", err)
	}
	j := Job{
		ID: s.newID(), OrgID: orgID, ZoneID: zoneID,
		Kind: KindSyncRepo, HostIDs: []uuid.UUID{},
		Status: StatusQueued, RunGroupID: s.newID(), Trigger: TriggerManual,
		ActorID: actor, CreatedAt: s.now(),
	}
	if err := s.store.CreateJob(ctx, j, payload); err != nil {
		return Job{}, err
	}
	return j, nil
}

// defaultZone resolves the organization's "default" zone; ErrNoZone when it
// is genuinely absent (a startup-reconciliation failure an operator must
// heal — see ErrNoZone).
func (s *Service) defaultZone(ctx context.Context, orgID uuid.UUID) (uuid.UUID, error) {
	if s.zones == nil {
		return uuid.Nil, fmt.Errorf("%w: no zone source wired", ErrNoZone)
	}
	zones, err := s.zones.ListZones(ctx, orgID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("list zones: %w", err)
	}
	for _, z := range zones {
		if z.Slug == hosts.DefaultZoneSlug {
			return z.ID, nil
		}
	}
	return uuid.Nil, fmt.Errorf("%w: sync_repo jobs run in the default zone", ErrNoZone)
}

// RecordSyncResult stamps a sync_repo job's terminal outcome onto its
// repository row (last_sync / last_sync_error), so the repository list can
// show when the org's playbook sources last fetched. The sync sink wiring
// (SQLQueue.SetSyncSink) calls this on the finish path; it is exposed as a
// service method because the repository row is org-scoped data.
func (s *Service) RecordSyncResult(ctx context.Context, orgID, repoID uuid.UUID, at time.Time, errText string) error {
	return s.store.RecordSyncResult(ctx, orgID, repoID, at, errText)
}

// payloadCleaner is the optional queue hook Service.Cancel uses to drop a
// cancelled-before-claim job's sealed payload — the same at-rest lifetime
// rule SQLQueue.Finish applies to terminal jobs: a queued bootstrap or
// rotate_key job that never shipped carries its one-time login or pending
// key for nothing.
type payloadCleaner interface {
	ClearTerminalPayload(ctx context.Context, jobID uuid.UUID)
}

// Cancel asks a job to stop. A queued job has never been shipped, so it is
// cancelled outright; a claimed/running job enters cancelling and the
// worker confirms through its terminal ReportResult. The cancel-request
// timestamp is recorded on the queue (when it implements CancelRecorder)
// for the reaper's confirmation deadline. A job cancelled before its claim
// has its sealed payload wiped immediately (when the queue implements
// payloadCleaner): it will never run, so its secrets must not linger.
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
	// Only a job the cancel made terminal loses its payload here: a running
	// job in cancelling may still finish (a rotation or a test the worker
	// completes before it sees the directive), and its own terminal
	// transition wipes the payload when it ends.
	if status == StatusCancelled {
		if cp, ok := s.queue.(payloadCleaner); ok {
			cp.ClearTerminalPayload(ctx, id)
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
