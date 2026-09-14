package jobs

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// SQLQueue is the claim half of the job queue and the ONE org-unscoped
// store in the system (plan Global Constraints): a worker claims across
// organizations, so the claim, the event append, the finish and the reaper
// run on the raw pool (db.Pool()) with explicit ids and no
// row-level-security scope. The org-scoped reads the payload build needs —
// inventory, credentials, bastions, the template row — run through q.db
// inside a db.WithOrgScope bound to the claimed job's organization, so RLS
// still applies to them.
//
// worker_id stays NULL on claim: the Claimer port the gRPC server defines
// carries no worker identity (only the connection's zones and the build
// version, which Workers.Touch already records on the worker row), so the
// claim cannot name the worker; the claim audit event does.
//
// streams, the bound-stream registry, is in-process and is the source of
// truth for "a worker is still attached" — the reaper requeues stale
// claims against it. Multi-instance managers are v0.6; until then a
// restart unbinds every stream, exactly like the gRPC token registry it
// mirrors, and a cancelling job's deadline falls back to claimed_at.
type SQLQueue struct {
	db        *db.DB
	box       Sealer
	renderer  SnapshotSource
	creds     CredentialSource
	templates TemplateSource
	bastions  BastionSource
	now       func() time.Time
	rand      io.Reader
	streams   *streamRegistry
	cancels   *cancelRegistry
	// sync is the optional sink a finished sync_repo job's outcome is
	// recorded through. Nil-safe by design: it is wired after construction
	// by internal/app (SetSyncSink) because the sink is the org-scoped
	// jobs.Service, whose own construction needs this queue.
	sync SyncSink
}

// SetSyncSink injects the sink RecordSyncResult calls flow through on the
// finish path. It exists (rather than a constructor parameter) for the
// same circularity tenancy's OrgCreatedHook setter does: the concrete sink
// — the org-scoped jobs.Service — is built after the queue it reports
// through. The queue's Finish never depends on it being set.
func (q *SQLQueue) SetSyncSink(sink SyncSink) { q.sync = sink }

// NewSQLQueue wires the queue onto d: the pool for the org-unscoped claim
// path, d itself for the org-scoped payload reads.
func NewSQLQueue(d *db.DB, box Sealer, renderer SnapshotSource, creds CredentialSource, templates TemplateSource, bastions BastionSource, now func() time.Time, rand io.Reader) *SQLQueue {
	return &SQLQueue{
		db: d, box: box, renderer: renderer, creds: creds, templates: templates, bastions: bastions,
		now: now, rand: rand, streams: newStreamRegistry(), cancels: newCancelRegistry(),
	}
}

// Claim picks one queued job for any of the zones and ships its payload.
//
// One transaction claims the row (FOR UPDATE SKIP LOCKED, see ClaimJob in
// queries/jobs.sql) and marks it running — the brief's claimed state is
// collapsed into running in the same statement, with claimed_at and
// started_at set together: nothing between the two states ever observes a
// job, and the schema's CHECK keeps both spellings legal. The payload is
// built after the commit, so the render never holds the row lock; a
// payload that cannot be built (a broken inventory, a missing credential)
// fails the job with the reason instead of shipping a partial one, and the
// claim reports an empty queue — the worker polls again and gets the next
// job.
func (q *SQLQueue) Claim(ctx context.Context, zones []uuid.UUID, _ string) (*Claim, error) {
	if len(zones) == 0 {
		return nil, nil
	}
	tx, err := q.db.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin claim transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := gen.New(tx).ClaimJob(ctx, zones)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit claim: %w", err)
	}
	job := toJob(row)
	claim := &Claim{Token: ""}
	if err := q.buildPayload(db.WithOrgScope(ctx, job.OrgID), job, &claim.Job); err != nil {
		if failErr := q.failJob(ctx, job.ID, err); failErr != nil {
			return nil, fmt.Errorf("fail unbuildable job %s (payload: %v): %w", job.ID, err, failErr)
		}
		return nil, nil
	}
	token, err := q.newToken()
	if err != nil {
		return nil, err
	}
	claim.Token = token
	q.streams.register(job.ID, job.OrgID, token, q.now())
	return claim, nil
}
func (q *SQLQueue) newToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := io.ReadFull(q.rand, buf); err != nil {
		return "", fmt.Errorf("generate binding token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// failJob marks a claimed job errored after a failed payload build. The
// error text names hosts, groups and ids only — the payload builders never
// put secret material into an error.
func (q *SQLQueue) failJob(ctx context.Context, jobID uuid.UUID, cause error) error {
	_, err := q.db.Pool().Exec(ctx,
		`UPDATE jobs SET status = 'error', error = $2, finished_at = $3 WHERE id = $1 AND status = 'running'`,
		jobID, cause.Error(), q.now())
	if err != nil {
		return fmt.Errorf("mark job errored: %w", err)
	}
	q.streams.remove(jobID)
	return nil
}

// buildPayload assembles the workerpb.Job for one claimed job into out:
// the rendered snapshot (secrets revealed) for the job's scope, the
// connection map, every referenced credential's opened secret, and the
// kind-specific payload. ctx must already carry the job's org scope
// (db.WithOrgScope). The result travels only inside the claim response —
// never log it. It writes through out because a protobuf message must
// never be copied by value (vet copylocks).
func (q *SQLQueue) buildPayload(ctx context.Context, job Job, out *workerpb.Job) error {
	// A sync_repo job is deliberately not host-bound: it touches no managed
	// host, so the whole host-bound payload machinery — the inventory
	// snapshot, connections and opened credentials — is skipped. A broken
	// jump chain or an empty org must never fail a git sync, and no
	// inventory secrets ship on a job that cannot use them.
	if job.Kind == KindSyncRepo {
		kindPayload, err := q.openKindPayload(job)
		if err != nil {
			return err
		}
		*out = workerpb.Job{
			Id: job.ID.String(), Kind: string(job.Kind),
			OrgId: job.OrgID.String(), ZoneId: job.ZoneID.String(),
			InventoryDoc: "{}", KindPayload: kindPayload,
		}
		return nil
	}
	scope := render.Scope{HostIDs: job.HostIDs}
	playbook, limit, sshUser := "", "", ""
	var extraVars map[string]string
	var defaultCred *uuid.UUID
	if job.Kind == KindRun {
		if job.TemplateID == nil {
			return fmt.Errorf("run job %s carries no template", job.ID)
		}
		tmpl, err := q.templates.TemplateLookup(ctx, job.OrgID, *job.TemplateID)
		if err != nil {
			return fmt.Errorf("look up template: %w", err)
		}
		scope, err = parseScope(tmpl.Scope)
		if err != nil {
			return fmt.Errorf("template scope: %w", err)
		}
		playbook = playbookRef(tmpl)
		limit = tmpl.Limit
		if extraVars, err = stringMap(tmpl.ExtraVars); err != nil {
			return fmt.Errorf("template extra vars: %w", err)
		}
		if tmpl.CredentialID != nil {
			defaultCred = tmpl.CredentialID
			c, err := q.creds.Get(ctx, job.OrgID, *tmpl.CredentialID)
			if err != nil {
				return fmt.Errorf("look up template credential: %w", err)
			}
			sshUser = c.Username
		}
	}
	inv, err := q.renderer.Snapshot(ctx, job.OrgID, scope)
	if err != nil {
		return fmt.Errorf("render snapshot: %w", err)
	}
	doc, err := json.Marshal(inv.Doc)
	if err != nil {
		return fmt.Errorf("encode inventory document: %w", err)
	}
	connections := make(map[string]string, len(inv.Connections))
	credIDs := map[uuid.UUID]bool{}
	if defaultCred != nil {
		credIDs[*defaultCred] = true
	}
	for name, c := range inv.Connections {
		b, err := json.Marshal(c)
		if err != nil {
			return fmt.Errorf("encode connection for host %s: %w", name, err)
		}
		connections[name] = string(b)
		if c.CredentialID != nil {
			credIDs[*c.CredentialID] = true
		}
		// The rendered chain names bastion ids; each one's login
		// credential must ship too, or the worker cannot hop.
		for _, bastionID := range c.Bastions {
			bastion, err := q.bastions.GetBastion(ctx, job.OrgID, bastionID)
			if err != nil {
				return fmt.Errorf("resolve bastion %s: %w", bastionID, err)
			}
			if bastion.CredentialID != nil {
				credIDs[*bastion.CredentialID] = true
			}
		}
	}
	credentials := make(map[string]string, len(credIDs))
	for id := range credIDs {
		secret, err := q.creds.OpenSecret(ctx, job.OrgID, id)
		if err != nil {
			return fmt.Errorf("open credential %s: %w", id, err)
		}
		b, err := json.Marshal(secret)
		if err != nil {
			return fmt.Errorf("encode credential %s: %w", id, err)
		}
		credentials[id.String()] = string(b)
	}
	kindPayload := ""
	if job.Kind != KindRun {
		if kindPayload, err = q.openKindPayload(job); err != nil {
			return err
		}
	}
	templateID := ""
	if job.TemplateID != nil {
		templateID = job.TemplateID.String()
	}
	*out = workerpb.Job{
		Id: job.ID.String(), Kind: string(job.Kind),
		OrgId: job.OrgID.String(), ZoneId: job.ZoneID.String(),
		TemplateId: templateID, Playbook: playbook, Limit: limit,
		ExtraVars: extraVars, SshUsername: sshUser,
		InventoryDoc: string(doc), Connections: connections, Credentials: credentials,
		HostKeys:    map[string]string{}, // host-key pins arrive with Task 10; the field is plumbed now
		KindPayload: kindPayload,
	}
	return nil
}

// playbookRef renders the playbook reference the worker resolves: an
// editor playbook ships inline, a repo playbook as "repo:<repo-id>:<path>".
func playbookRef(t TemplateRow) string {
	if t.PlaybookRepo != nil {
		return "repo:" + t.PlaybookRepo.String() + ":" + t.PlaybookPath
	}
	return t.PlaybookInline
}

// stringMap decodes an extra_vars jsonb into the wire map: every value
// JSON-encoded, so a string stays a string and an object stays an object.
func stringMap(raw json.RawMessage) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("%w: extra vars are not a JSON object", ErrInvalidInput)
	}
	out := make(map[string]string, len(values))
	for k, v := range values {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("encode extra var %s: %w", k, err)
		}
		out[k] = string(b)
	}
	return out, nil
}

// openKindPayload decrypts a host job's sealed payload into the wire
// kind_payload. The stored form is a JSON string carrying the base64
// ciphertext (see Service.sealPayload); a payload that is not a JSON
// string is passed through as plain kind JSON, which is what kinds
// without secrets store.
func (q *SQLQueue) openKindPayload(job Job) (string, error) {
	if len(job.Payload) == 0 {
		return "", nil
	}
	var sealed string
	if err := json.Unmarshal(job.Payload, &sealed); err != nil {
		return string(job.Payload), nil
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", fmt.Errorf("decode job payload: %w", err)
	}
	plain, err := q.box.OpenAAD(raw, JobAAD(job.OrgID, job.ID))
	if err != nil {
		return "", fmt.Errorf("open job payload: %w", err)
	}
	return string(plain), nil
}

// Finish records the terminal status of a run. Idempotent by design: a
// worker that retries ReportResult after an audit failure must not corrupt
// the record, so finishing an already terminal job is a no-op. Only
// running or cancelling jobs accept a finish; a queued or requeued one
// reports ErrNotRunning, so a late report from a worker whose claim the
// reaper took back is visible instead of silently overwriting the fresh
// claim.
func (q *SQLQueue) Finish(ctx context.Context, jobID uuid.UUID, status Status, stats map[string]any, errText string) error {
	if !status.Terminal() {
		return fmt.Errorf("%w: %q is not a terminal status", ErrInvalidInput, status)
	}
	statsJSON := []byte("{}")
	if stats != nil {
		b, err := json.Marshal(stats)
		if err != nil {
			return fmt.Errorf("encode job stats: %w", err)
		}
		statsJSON = b
	}
	tag, err := q.db.Pool().Exec(ctx,
		`UPDATE jobs SET status = $2, stats = $3, error = $4, finished_at = $5
		 WHERE id = $1 AND status IN ('running', 'cancelling')`,
		jobID, string(status), statsJSON, errText, q.now())
	if err != nil {
		return fmt.Errorf("finish job: %w", err)
	}
	if tag.RowsAffected() == 1 {
		q.streams.remove(jobID)
		q.cancels.remove(jobID)
		q.recordSync(ctx, jobID, status, errText)
		return nil
	}
	var current string
	if err := q.db.Pool().QueryRow(ctx, `SELECT status FROM jobs WHERE id = $1`, jobID).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("look up job status: %w", err)
	}
	if Status(current).Terminal() {
		return nil
	}
	return fmt.Errorf("%w: job is %s", ErrNotRunning, current)
}

// NoteCancelRequested records when a cancel was requested — the anchor for
// the reaper's confirmation deadline. In-process by the same reasoning as
// the stream registry: after a manager restart the reaper falls back to
// the job's claimed_at, which can only make the deadline earlier, never
// later.
func (q *SQLQueue) NoteCancelRequested(jobID uuid.UUID, at time.Time) {
	q.cancels.note(jobID, at)
}

// recordSync hands a finished sync_repo job's outcome to the optional
// sync sink. A terminal record already stands — the sink is bookkeeping,
// not bookkeeping-breaking — so a sink failure is logged, never returned.
// A cancelled sync records nothing: it never ran, and recording last_sync
// for it would look like a successful fetch. A non-sync job, or a job
// already terminal when Finish retried, records nothing.
func (q *SQLQueue) recordSync(ctx context.Context, jobID uuid.UUID, status Status, errText string) {
	if q.sync == nil || status == StatusCancelled {
		return
	}
	var orgID uuid.UUID
	var kind string
	var payload json.RawMessage
	if err := q.db.Pool().QueryRow(ctx,
		`SELECT org_id, kind, payload FROM jobs WHERE id = $1`, jobID,
	).Scan(&orgID, &kind, &payload); err != nil {
		slog.ErrorContext(ctx, "record sync result: look up finished job", "err", err, "job", jobID)
		return
	}
	if Kind(kind) != KindSyncRepo {
		return
	}
	var sp syncPayload
	if err := json.Unmarshal(payload, &sp); err != nil || sp.RepoID == uuid.Nil {
		slog.ErrorContext(ctx, "record sync result: job payload carries no repo id", "job", jobID)
		return
	}
	if err := q.sync.RecordSyncResult(ctx, orgID, sp.RepoID, q.now(), errText); err != nil {
		// The job's terminal status is already recorded; a failed
		// bookkeeping write is logged so an operator sees the gap, the
		// same tradeoff the audit middleware makes.
		slog.ErrorContext(ctx, "record sync result", "err", err, "job", jobID)
	}
}
