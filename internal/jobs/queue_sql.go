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
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/ansiblevars"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/sshconf"
)

// SQLQueue is the claim half of the job queue and the ONE org-unscoped
// store in the system (plan Global Constraints): the claim path is not bound
// to a request's organization, so the claim, the event append, the finish
// and the reaper run on the raw pool (db.Pool()) with explicit ids and no
// row-level-security scope — the claim filters on the claiming worker's
// own organization explicitly (see Claim). The org-scoped reads the payload build needs —
// inventory, credentials, bastions, the template row — run through q.db
// inside a db.WithOrgScope bound to the claimed job's organization, so RLS
// still applies to them.
//
// worker_id records the claiming worker on ClaimFor (the gRPC path) and is
// what binds the terminal result to that worker (FinishFor); the manager's
// own unbound Claim leaves it NULL, and such a job accepts no worker's
// result.
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
	// repos resolves the repository a repo playbook's claim fetches. Nil
	// until SetRepoSource is called after construction — the same
	// circularity the sync sink has: the concrete source is the
	// repos.Service, which itself is built with the jobs service this
	// queue belongs to.
	repos RepoSource
	// pins is the optional source of host-key fingerprints (the hosts
	// service). Nil until SetKeyPins: a connection_test claim without it
	// fails closed — the probe could not run trust on first use against
	// the pin — while run and ad-hoc claims simply ship no host keys.
	pins KeyPinSource
	// keyGuard is the changed-host-key read the host-bound claims re-apply
	// (the pins source implements it — hosts.Service does); nil fails
	// run and ad-hoc claims closed.
	keyGuard KeyGuardSource
	// knownHosts is the pinned hosts' full public keys (the pins source
	// implements it); nil fails every claim for a pinned host closed.
	knownHosts KnownHostSource
	// test is the optional sink a finished connection_test job's outcome is
	// recorded through (SetTestSink, the same post-construction wiring the
	// sync sink has; the concrete sink is the hosts service).
	test    TestSink
	now     func() time.Time
	rand    io.Reader
	streams *streamRegistry
	cancels *cancelRegistry
	// sync is the optional sink a finished sync_repo job's outcome is
	// recorded through. Nil-safe by design: it is wired after construction
	// by internal/app (SetSyncSink), because the concrete sink is the
	// org-scoped jobs.Service, which itself is built with the queue it
	// reports through.
	sync SyncSink
	// rotation is the optional sink a completed rotate_key group's outcome
	// flows through (SetRotationFinisher): credentials.Service
	// finalizes the rotation — sealing the pending new key onto the
	// credential or leaving it untouched.
	rotation RotationFinisher
	// broker is the optional SSE fan-out the append path feeds
	// (SetEventBroker): the jobs service owns the in-process subscriber
	// registry and hands it over post-construction, the same circularity
	// the sync sink has. Nil-safe: AppendEvent publishes through the nil
	// receiver, which is a no-op.
	broker *Broker
}

// SetSyncSink injects the sink RecordSyncResult calls flow through on the
// finish path. It exists (rather than a constructor parameter) for the
// same circularity tenancy's OrgCreatedHook setter does: the concrete sink
// — the org-scoped jobs.Service — is built after the queue it reports
// through. The queue's Finish never depends on it being set.
func (q *SQLQueue) SetSyncSink(sink SyncSink) { q.sync = sink }

// SetEventBroker injects the SSE subscriber registry the append path
// fans events out through. It exists (rather than a constructor
// parameter) for the same circularity SetSyncSink does: the broker is
// owned by the org-scoped jobs.Service, which is built after the queue
// it tails. The queue's AppendEvent never depends on it being set.
func (q *SQLQueue) SetEventBroker(b *Broker) { q.broker = b }

// SetRepoSource injects the repository resolver a repo playbook's claim
// needs (see the repos field). A nil source makes every repo playbook's
// claim fail closed — a wiring bug, never silently shipped without the
// repo the run must fetch.
func (q *SQLQueue) SetRepoSource(source RepoSource) { q.repos = source }

// SetKeyPins injects the host-key fingerprint source the claim needs. A
// nil source makes every connection_test claim fail closed (see the pins
// field); run and ad-hoc jobs then ship no host keys.
func (q *SQLQueue) SetKeyPins(source KeyPinSource) {
	q.pins = source
	q.keyGuard, q.knownHosts = nil, nil
	if g, ok := source.(KeyGuardSource); ok {
		q.keyGuard = g
	}
	if k, ok := source.(KnownHostSource); ok {
		q.knownHosts = k
	}
}

// SetTestSink injects the sink RecordTest flows through on the finish path,
// the connection-test sibling of SetSyncSink. A nil sink records nothing —
// the wiring gap shows up as a missing test history, never as a failed job.
func (q *SQLQueue) SetTestSink(sink TestSink) { q.test = sink }

// NewSQLQueue wires the queue onto d: the pool for the org-unscoped claim
// path, d itself for the org-scoped payload reads.
func NewSQLQueue(d *db.DB, box Sealer, renderer SnapshotSource, creds CredentialSource, templates TemplateSource, bastions BastionSource, now func() time.Time, rand io.Reader) *SQLQueue {
	return &SQLQueue{
		db: d, box: box, renderer: renderer, creds: creds, templates: templates, bastions: bastions,
		now: now, rand: rand, streams: newStreamRegistry(), cancels: newCancelRegistry(),
	}
}

// Claim picks one queued job of orgID for any of the zones and ships its
// payload. orgID is the claiming worker's own organization: the statement
// filters on it, so a zone id of another organization matches nothing.
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
func (q *SQLQueue) Claim(ctx context.Context, orgID uuid.UUID, zones []uuid.UUID, version string) (*Claim, error) {
	return q.claim(ctx, pgtype.UUID{}, orgID, zones, version)
}

// ClaimFor is Claim on behalf of an enrolled worker: the claimed row
// records workerID (jobs.worker_id), and only that worker's FinishFor is
// accepted for the job afterwards. The gRPC claim path uses this; Claim
// (no binding) remains for the manager's own in-process callers.
func (q *SQLQueue) ClaimFor(ctx context.Context, workerID, orgID uuid.UUID, zones []uuid.UUID, version string) (*Claim, error) {
	return q.claim(ctx, pgtype.UUID{Bytes: workerID, Valid: true}, orgID, zones, version)
}

func (q *SQLQueue) claim(ctx context.Context, workerID pgtype.UUID, orgID uuid.UUID, zones []uuid.UUID, _ string) (*Claim, error) {
	if len(zones) == 0 {
		return nil, nil
	}
	tx, err := q.db.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin claim transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := gen.New(tx).ClaimJob(ctx, gen.ClaimJobParams{WorkerID: workerID, OrgID: orgID, ZoneIds: zones})
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
	q.streams.register(job.ID, job.OrgID, token, job.Attempts, q.now())
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
// put secret material into an error. It is a terminal transition like any
// other (see terminate): the sealed payload is wiped and the sinks run, so
// an unbuildable bootstrap or rotation job neither keeps its secret at rest
// nor leaves its rotation group unfinalized.
func (q *SQLQueue) failJob(ctx context.Context, jobID uuid.UUID, cause error) error {
	_, err := q.terminate(ctx, terminalTransition{
		jobID: jobID, status: StatusError, errText: cause.Error(),
		from: []Status{StatusRunning},
	})
	if err != nil {
		return fmt.Errorf("mark job errored: %w", err)
	}
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
	// A connection_test job is also deliberately not snapshot-bound: the
	// probe target is fully resolved into the kind payload at claim time —
	// the effective connection comes from the same rendered route run jobs
	// claim, but the probe consumes a self-contained payload and the
	// inventory document (hostvars with revealed secrets) is discarded.
	if job.Kind == KindConnectionTest {
		kindPayload, hostKeys, err := q.connectionTestPayload(ctx, job)
		if err != nil {
			return err
		}
		var redact redactSet
		redact.addKindPayload(kindPayload)
		*out = workerpb.Job{
			Id: job.ID.String(), Kind: string(job.Kind),
			OrgId: job.OrgID.String(), ZoneId: job.ZoneID.String(),
			InventoryDoc: "{}", HostKeys: hostKeys, KindPayload: kindPayload,
			Redact: redact.list,
		}
		return nil
	}
	// The bootstrap and rotate_key jobs are self-contained the same way:
	// the kind payload carries the target, the one-time login (bootstrap)
	// or the old credential and pending new key (rotate_key), and the
	// pinned host key — the worker's executors reshape the claim into a
	// one-host run and drive the embedded system playbooks. The rendered
	// route resolves the target's connection (address, port, jump chain);
	// the inventory document is discarded.
	if job.Kind == KindBootstrap || job.Kind == KindRotateKey || job.Kind == KindRevokeKey {
		kindPayload, pins, err := q.systemPayload(ctx, job)
		if err != nil {
			return err
		}
		var redact redactSet
		redact.addKindPayload(kindPayload)
		*out = workerpb.Job{
			Id: job.ID.String(), Kind: string(job.Kind),
			OrgId: job.OrgID.String(), ZoneId: job.ZoneID.String(),
			InventoryDoc: "{}", HostKeys: pins.fingerprints, KnownHosts: pins.publicKeys, KindPayload: kindPayload,
			Redact: redact.list,
		}
		return nil
	}
	// A sync_repo job is deliberately not host-bound: it touches no managed
	// host, so the whole host-bound payload machinery — the inventory
	// snapshot, connections and opened credentials — is skipped. A broken
	// jump chain or an empty org must never fail a git sync, and no
	// inventory secrets ship on a job that cannot use them. The claim-time
	// enrichment (Task 11) resolves the repository the job syncs — url,
	// branch, subpath and the git credential as `git:<repo-id>` — the same
	// way a repo playbook's run claim carries its fetch target.
	if job.Kind == KindSyncRepo {
		kindPayload, credentials, err := q.syncRepoPayload(ctx, job)
		if err != nil {
			return err
		}
		var redact redactSet
		for _, c := range credentials {
			redact.addSecretJSON(c)
		}
		*out = workerpb.Job{
			Id: job.ID.String(), Kind: string(job.Kind),
			OrgId: job.OrgID.String(), ZoneId: job.ZoneID.String(),
			InventoryDoc: "{}", Credentials: credentials, KindPayload: kindPayload,
			Redact: redact.list,
		}
		return nil
	}
	// The claim renders EXACTLY the job's own hosts (final review C1). The
	// template's scope was resolved at enqueue time and split per zone into
	// job.HostIDs — rendering the template scope again here would run every
	// zone's job against every zone's hosts, widen a retry back to the whole
	// template and resurrect hosts the enqueue excluded for a changed host
	// key. An empty host list is a corrupt row, never "the whole org".
	if len(job.HostIDs) == 0 {
		return fmt.Errorf("%w: %s job %s carries no target hosts", ErrInvalidInput, job.Kind, job.ID)
	}
	hostIDs, err := q.excludeChangedAtClaim(ctx, job)
	if err != nil {
		return err
	}
	scope := render.Scope{HostIDs: hostIDs}
	playbook, limit, sshUser := "", "", ""
	var extraVars map[string]string
	var defaultCred *uuid.UUID
	var redact redactSet
	if job.Kind == KindRun {
		if job.TemplateID == nil {
			return fmt.Errorf("run job %s carries no template", job.ID)
		}
		tmpl, err := q.templates.TemplateLookup(ctx, job.OrgID, *job.TemplateID)
		if err != nil {
			return fmt.Errorf("look up template: %w", err)
		}
		playbook = playbookRef(tmpl)
		// The template's limit stays an ansible host pattern applied on top
		// of the job's rendered hosts: it can only narrow a job, never widen
		// it past job.HostIDs (the inventory holds nothing else).
		limit = tmpl.Limit
		if extraVars, err = stringMap(tmpl.ExtraVars); err != nil {
			return fmt.Errorf("template extra vars: %w", err)
		}
		// A template stored before the connection-security variables were
		// refused must not run with them (re-review N1).
		for name := range extraVars {
			if ansiblevars.Forbidden(name) {
				return fmt.Errorf("%w: template extra var %s controls connection security; remove it from the template", ErrInvalidInput, name)
			}
		}
		if err := redact.addExtraVarSecrets(tmpl.ExtraVarsSchema, tmpl.ExtraVars); err != nil {
			return err
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
		// The connection the claim ships carries the EFFECTIVE login
		// credential: the host's own, or the template's default when the
		// host has none — the worker maps it to the key file it writes.
		if c.CredentialID == nil && defaultCred != nil {
			c.CredentialID = defaultCred
		}
		if c.CredentialID != nil {
			credIDs[*c.CredentialID] = true
		}
		// The rendered chain names bastion ids; each one's login
		// credential must ship too, or the worker cannot hop. The claim
		// also carries the ids themselves, aligned with the bastions
		// list, so the worker can map each hop's key file.
		bastionCredIDs := make([]string, 0, len(c.Bastions))
		for _, bastionID := range c.Bastions {
			bastion, err := q.bastions.GetBastion(ctx, job.OrgID, bastionID)
			if err != nil {
				return fmt.Errorf("resolve bastion %s: %w", bastionID, err)
			}
			if bastion.CredentialID != nil {
				credIDs[*bastion.CredentialID] = true
				bastionCredIDs = append(bastionCredIDs, bastion.CredentialID.String())
			} else {
				bastionCredIDs = append(bastionCredIDs, "")
			}
		}
		b, err := json.Marshal(claimConnection{
			HostID: c.HostID, CredentialID: c.CredentialID, Bastions: c.Bastions,
			BastionCredentialIDs: bastionCredIDs, BastionSource: c.BastionSource,
		})
		if err != nil {
			return fmt.Errorf("encode connection for host %s: %w", name, err)
		}
		connections[name] = string(b)
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
	} else if repoID, sub, isRepo := parsePlaybookRef(playbook); isRepo {
		// A repo playbook's claim carries the repository the worker must
		// fetch (the claim-time enrichment the Task 6 review flagged) and
		// the repo's git credential as `git:<repo-id>`: the worker clones
		// itself, and it needs url, branch and the playbook's file path.
		var err error
		if kindPayload, err = q.repoKindPayload(ctx, job, repoID, sub, credentials); err != nil {
			return err
		}
	}
	templateID := ""
	if job.TemplateID != nil {
		templateID = job.TemplateID.String()
	}
	pins, err := q.claimHostKeys(ctx, job, inv.Connections)
	if err != nil {
		return err
	}
	for _, c := range credentials {
		redact.addSecretJSON(c)
	}
	redact.add(inv.Secrets...)
	*out = workerpb.Job{
		Id: job.ID.String(), Kind: string(job.Kind),
		OrgId: job.OrgID.String(), ZoneId: job.ZoneID.String(),
		TemplateId: templateID, Playbook: playbook, Limit: limit,
		ExtraVars: extraVars, SshUsername: sshUser,
		InventoryDoc: string(doc), Connections: connections, Credentials: credentials,
		HostKeys:    pins.fingerprints,
		KnownHosts:  pins.publicKeys,
		KindPayload: kindPayload,
		Redact:      redact.list,
	}
	return nil
}

// excludeChangedAtClaim re-applies the enqueue's block-on-mismatch (spec §5)
// at claim time: a host whose latest connection test reports a changed key
// after the job was enqueued must not be dialed. Hosts are dropped from the
// job's scope; when none is left the claim fails the job with
// ErrHostKeyChanged. The guard is mandatory for host-bound claims — an
// unwired guard fails closed.
func (q *SQLQueue) excludeChangedAtClaim(ctx context.Context, job Job) ([]uuid.UUID, error) {
	if q.keyGuard == nil {
		return nil, fmt.Errorf("%s claim %s: no host key guard wired", job.Kind, job.ID)
	}
	changed, err := q.keyGuard.ChangedHosts(ctx, job.OrgID, job.HostIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve changed host keys: %w", err)
	}
	kept := make([]uuid.UUID, 0, len(job.HostIDs))
	for _, id := range job.HostIDs {
		if !changed[id] {
			kept = append(kept, id)
		}
	}
	if len(kept) == 0 {
		return nil, fmt.Errorf("%w: job %s", ErrHostKeyChanged, job.ID)
	}
	return kept, nil
}

// claimPins is what a host-bound claim ships about pinned host keys, both
// maps keyed by host name: the fingerprints (host_keys) and the full public
// keys the worker writes into the run's known_hosts (known_hosts).
type claimPins struct {
	fingerprints map[string]string
	publicKeys   map[string]string
}

// claimHostKeys collects the pins of the job's rendered hosts. Every pinned
// host must come with its full public key: the worker enforces a pin
// through known_hosts, which a fingerprint cannot fill. A pin made before
// full keys were recorded fails the claim with the remedy (a connection
// test confirming the pin stores the key); so does an unwired key source.
func (q *SQLQueue) claimHostKeys(ctx context.Context, job Job, conns map[string]render.Connection) (claimPins, error) {
	out := claimPins{fingerprints: map[string]string{}, publicKeys: map[string]string{}}
	if len(conns) == 0 {
		return out, nil
	}
	if q.pins == nil {
		return claimPins{}, fmt.Errorf("%s claim %s: no host key pin source wired", job.Kind, job.ID)
	}
	ids := make([]uuid.UUID, 0, len(conns))
	names := make(map[uuid.UUID][]string, len(conns))
	for name, c := range conns {
		if _, seen := names[c.HostID]; !seen {
			ids = append(ids, c.HostID)
		}
		names[c.HostID] = append(names[c.HostID], name)
	}
	pinned, err := q.pins.PinnedKeys(ctx, job.OrgID, ids)
	if err != nil {
		return claimPins{}, fmt.Errorf("resolve host key pins: %w", err)
	}
	if len(pinned) == 0 {
		return out, nil
	}
	if q.knownHosts == nil {
		return claimPins{}, fmt.Errorf("%s claim %s: no pinned host key source wired", job.Kind, job.ID)
	}
	keys, err := q.knownHosts.PinnedPublicKeys(ctx, job.OrgID, ids)
	if err != nil {
		return claimPins{}, fmt.Errorf("resolve pinned host public keys: %w", err)
	}
	for hostID, fingerprint := range pinned {
		pub := keys[hostID]
		if pub == "" {
			return claimPins{}, fmt.Errorf("host %s was pinned before full host keys were recorded, so its pin cannot be enforced: run a connection test for the host (it completes the pin) and retry", hostID)
		}
		for _, name := range names[hostID] {
			out.fingerprints[name] = fingerprint
			out.publicKeys[name] = pub
		}
	}
	return out, nil
}

// connectionTestPayload resolves the job's single host into the
// self-contained kind payload the sshprobe executor consumes:
//
//	{"host":{"id","name","address","port","username"},
//	 "credential":"<revealed Secret JSON or empty>",
//	 "bastions":[{"id","address","port","username","credential":"<Secret JSON>"}…]}
//
// The effective connection — login credential and jump chain — comes from
// the same rendered snapshot run jobs claim, so the probe tests exactly
// the route a run would take (host SSH setting → group → zone → none, the
// bastion's own chain expanded). Nothing secret ships outside the payload:
// the inventory document is discarded, and the credentials map stays
// empty. The second return is the host_keys map: the pinned fingerprint
// for the host when it carries one.
func (q *SQLQueue) connectionTestPayload(ctx context.Context, job Job) (string, map[string]string, error) {
	if len(job.HostIDs) != 1 {
		return "", nil, fmt.Errorf("%w: connection_test targets exactly one host, job %s carries %d", ErrInvalidInput, job.ID, len(job.HostIDs))
	}
	// The pin needs the hosts store: nil pins source is a wiring bug that
	// would silently run the probe unpinned — fail closed.
	if q.pins == nil {
		return "", nil, fmt.Errorf("connection test claim: no host key pin source wired")
	}
	hostID := job.HostIDs[0]
	inv, err := q.renderer.Snapshot(ctx, job.OrgID, render.Scope{HostIDs: job.HostIDs})
	if err != nil {
		return "", nil, fmt.Errorf("render connection: %w", err)
	}
	var conn render.Connection
	connName := ""
	for name, c := range inv.Connections {
		if c.HostID == hostID {
			conn, connName = c, name
			break
		}
	}
	if connName == "" {
		return "", nil, fmt.Errorf("connection test %s: host %s renders no connection (not managed, or scope mismatch)", job.ID, hostID)
	}
	host := probeHost{
		ID:      hostID,
		Name:    connName,
		Address: connAddress(inv.Doc, connName),
	}
	if host.Address == "" {
		host.Address = host.Name
	}
	host.Port, host.Username = connPortAndUser(inv.Doc, connName)

	// A rotation group's follow-up test carries the pending new key sealed
	// in its own payload: the probe authenticates with the NEW key, so the
	// group's verification is genuinely about the rotation's outcome — the
	// credential row still holds the old secret until the group finalizes.
	// The only writer of such a payload is the rotation enqueue; any other
	// (or unparseable) payload keeps the rendered credential, and a sealed
	// payload that does not open under this job's AAD still fails the claim.
	override := credentials.Secret{}
	if len(job.Payload) > 0 {
		plain, err := q.openKindPayload(job)
		if err != nil {
			return "", nil, err
		}
		var rp rotationJobPayload
		if json.Unmarshal([]byte(plain), &rp) == nil && rp.CredentialID != uuid.Nil && rp.NewPrivateKey.PrivateKey != "" {
			override = rp.NewPrivateKey
			// The new key is installed for the credential's own account —
			// the same resolution the rotate_key payload uses.
			if cred, err := q.creds.Get(ctx, job.OrgID, rp.CredentialID); err == nil && cred.Username != "" {
				host.Username = cred.Username
			}
		}
	}

	// The login credential and every hop's credential are opened here;
	// they travel only inside the kind payload.
	credential := ""
	switch {
	case override.PrivateKey != "":
		secret, err := secretJSON(override)
		if err != nil {
			return "", nil, err
		}
		credential = secret
	case conn.CredentialID != nil:
		secret, err := q.creds.OpenSecret(ctx, job.OrgID, *conn.CredentialID)
		if err != nil {
			return "", nil, fmt.Errorf("open credential %s: %w", *conn.CredentialID, err)
		}
		if credential, err = secretJSON(secret); err != nil {
			return "", nil, err
		}
	}
	hops := make([]probeBastion, 0, len(conn.Bastions))
	for _, bastionID := range conn.Bastions {
		b, err := q.bastions.GetBastion(ctx, job.OrgID, bastionID)
		if err != nil {
			return "", nil, fmt.Errorf("resolve bastion %s: %w", bastionID, err)
		}
		hopCredential := ""
		if b.CredentialID != nil {
			secret, err := q.creds.OpenSecret(ctx, job.OrgID, *b.CredentialID)
			if err != nil {
				return "", nil, fmt.Errorf("open bastion credential %s: %w", *b.CredentialID, err)
			}
			if hopCredential, err = secretJSON(secret); err != nil {
				return "", nil, err
			}
		}
		hops = append(hops, probeBastion{ID: bastionID, Address: b.Address, Port: b.Port, Username: b.Username, Credential: hopCredential})
	}

	// The pinned fingerprint, if the host carries one.
	hostKeys := map[string]string{}
	pinned, err := q.pins.PinnedKeys(ctx, job.OrgID, []uuid.UUID{hostID})
	if err != nil {
		return "", nil, fmt.Errorf("resolve host key pins: %w", err)
	}
	if fingerprint, ok := pinned[hostID]; ok {
		hostKeys[connName] = fingerprint
	}

	payload := map[string]any{"host": host, "credential": credential, "bastions": hops}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", nil, fmt.Errorf("encode connection-test kind payload: %w", err)
	}
	return string(b), hostKeys, nil
}

// probeHost is the host object of the kind payload.
type probeHost struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Address  string    `json:"address"`
	Port     int       `json:"port"`
	Username string    `json:"username"`
}

// probeBastion is one hop of the kind payload's chain, its credential
// inline as Secret JSON (or empty when the hop carries none).
type probeBastion struct {
	ID         uuid.UUID `json:"id"`
	Address    string    `json:"address"`
	Port       int       `json:"port"`
	Username   string    `json:"username"`
	Credential string    `json:"credential"`
}

// connAddress reads the host's ansible_host from the rendered snapshot —
// the address ansible itself will dial.
func connAddress(doc map[string]any, name string) string {
	vars, _ := docHostVars(doc)[name]
	if vars == nil {
		return ""
	}
	s, _ := vars["ansible_host"].(string)
	return s
}

// connPortAndUser reads the host's effective port and login user the same
// way the renderer wrote them: ansible_port only when it differs from the
// default, ansible_user from the host's own setting or its credential.
func connPortAndUser(doc map[string]any, name string) (port int, username string) {
	vars, _ := docHostVars(doc)[name]
	if vars != nil {
		switch v := vars["ansible_port"].(type) {
		case int:
			port = v
		case float64:
			port = int(v)
		}
		username, _ = vars["ansible_user"].(string)
	}
	if port == 0 {
		port = sshconf.DefaultPort
	}
	return port, username
}

func docHostVars(doc map[string]any) map[string]map[string]any {
	meta, _ := doc["_meta"].(map[string]any)
	hostvars, _ := meta["hostvars"].(map[string]any)
	out := map[string]map[string]any{}
	for name, v := range hostvars {
		m, _ := v.(map[string]any)
		out[name] = m
	}
	return out
}

// secretJSON encodes a revealed secret as its JSON form, the claim's wire
// format for credential strings.
func secretJSON(secret credentials.Secret) (string, error) {
	b, err := json.Marshal(secret)
	if err != nil {
		return "", fmt.Errorf("encode credential: %w", err)
	}
	return string(b), nil
}

// playbookRef renders the playbook reference the worker resolves: an
// editor playbook ships inline, a repo playbook as "repo:<repo-id>:<path>".
func playbookRef(t TemplateRow) string {
	if t.PlaybookRepo != nil {
		return "repo:" + t.PlaybookRepo.String() + ":" + t.PlaybookPath
	}
	return t.PlaybookInline
}

// parsePlaybookRef splits a playbook reference into its repo id and the
// playbook's path; a reference without the repo: prefix is inline (not a
// repo, no error).
func parsePlaybookRef(ref string) (uuid.UUID, string, bool) {
	rest, isRepo := strings.CutPrefix(ref, "repo:")
	if !isRepo {
		return uuid.Nil, "", false
	}
	idStr, sub, ok := strings.Cut(rest, ":")
	if !ok {
		return uuid.Nil, "", true
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return uuid.Nil, "", true
	}
	return id, sub, true
}

// repoKindPayload resolves a repo playbook's repository into the claim's
// kind payload and opens the repo's git credential into the credentials
// map as `git:<repo-id>`. A missing repo source is a wiring bug and fails
// closed; an unknown repository is a dangling playbook reference (the
// playbooks table's foreign key keeps the window narrow).
func (q *SQLQueue) repoKindPayload(ctx context.Context, job Job, repoID uuid.UUID, path string, credentials map[string]string) (string, error) {
	if q.repos == nil {
		return "", fmt.Errorf("repo playbook %s: no repo source wired", repoID)
	}
	repo, err := q.repos.Get(ctx, job.OrgID, repoID)
	if err != nil {
		return "", fmt.Errorf("resolve repo %s: %w", repoID, err)
	}
	sub := repo.Subpath
	if sub == "" {
		sub = path
	} else if path != "" {
		sub += "/" + path
	}
	if repo.CredentialID != nil {
		secret, err := q.creds.OpenSecret(ctx, job.OrgID, *repo.CredentialID)
		if err != nil {
			return "", fmt.Errorf("open repo credential %s: %w", *repo.CredentialID, err)
		}
		b, err := json.Marshal(secret)
		if err != nil {
			return "", fmt.Errorf("encode repo credential %s: %w", *repo.CredentialID, err)
		}
		credentials["git:"+repoID.String()] = string(b)
	}
	payload := map[string]any{"repo": map[string]any{
		"id": repoID.String(), "url": repo.URL, "branch": repo.Branch, "subpath": sub,
	}}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode repo kind payload: %w", err)
	}
	return string(b), nil
}

// claimConnection is the connection JSON one claim ships for a host: the
// rendered Connection (internal/inventory/render) plus the claim-time
// additions — the effective login credential and, aligned with the
// bastions list, each bastion's login credential id.
type claimConnection struct {
	HostID       uuid.UUID   `json:"host_id"`
	CredentialID *uuid.UUID  `json:"credential_id,omitempty"`
	Bastions     []uuid.UUID `json:"bastions,omitempty"`
	// BastionCredentialIDs[i] is the credential bastions[i] logs in with;
	// an empty string when that bastion carries none.
	BastionCredentialIDs []string `json:"bastion_credential_ids,omitempty"`
	BastionSource        string   `json:"bastion_source"`
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
//
// Atomic: the terminal status, every finish-path sink (the repository's
// sync state, the connection-test row, the rotation group's finalize) and
// the wipe of the sealed payload run in ONE transaction bound to the job's
// organization (db.WithWriteTx — the sinks' stores join it through the
// ctx). A sink failure or a context cancelled mid-finish rolls all of it
// back and returns the error: the job stays running with its payload, and
// the retried Finish completes everything. Without that, a crash between
// the status write and a sink left a succeeded job whose rotation key was
// never sealed, whose test row was missing or whose one-time login stayed
// at rest — and the retry saw a terminal job and did nothing.
//
// Members of a rotation group serialize on a per-group advisory
// transaction lock taken before the status write: with the status and the
// group's completeness count in one transaction, two members finishing
// concurrently would otherwise each count the other as still running and
// neither would finalize the group.
func (q *SQLQueue) Finish(ctx context.Context, jobID uuid.UUID, status Status, stats map[string]any, errText string) error {
	return q.finish(ctx, nil, jobID, status, stats, errText)
}

// WorkerRef is the enrolled worker a result comes from, as the gRPC layer
// authenticated it (registry id, organization, zone).
type WorkerRef struct {
	ID     uuid.UUID
	OrgID  uuid.UUID
	ZoneID uuid.UUID
}

// ErrNotClaimedByWorker is FinishFor's refusal: the job is unknown, belongs
// to another organization or zone, was never claimed by a worker, or was
// claimed by a different worker (including one whose claim the reaper
// took back). Deliberately one error for all of them, so a worker cannot
// probe which job ids exist elsewhere.
var ErrNotClaimedByWorker = errors.New("job is not claimed by this worker")

// FinishFor is Finish on behalf of an enrolled worker — the only form the
// gRPC result path uses. It acts only on a job whose org_id and zone_id
// are the worker's and whose worker_id is the worker's own (set by
// ClaimFor); anything else returns ErrNotClaimedByWorker without touching
// the row, the sinks or the payload. The binding is checked before the
// transaction and enforced again in the status UPDATE's WHERE clause, so a
// reaper requeue in between cannot slip a stranger's result through. An
// already terminal job the worker itself finished stays the idempotent
// no-op.
func (q *SQLQueue) FinishFor(ctx context.Context, worker WorkerRef, jobID uuid.UUID, status Status, stats map[string]any, errText string) error {
	return q.finish(ctx, &worker, jobID, status, stats, errText)
}

// finish is Finish/FinishFor; worker nil means the manager's own unbound
// finish.
func (q *SQLQueue) finish(ctx context.Context, worker *WorkerRef, jobID uuid.UUID, status Status, stats map[string]any, errText string) error {
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
	// org_id and the claiming worker are read before the transaction; the
	// binding is enforced again in the status UPDATE's WHERE clause.
	var orgID, zoneID uuid.UUID
	var claimedBy *uuid.UUID
	if err := q.db.Pool().QueryRow(ctx,
		`SELECT org_id, zone_id, worker_id FROM jobs WHERE id = $1`, jobID,
	).Scan(&orgID, &zoneID, &claimedBy); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if worker != nil {
				return ErrNotClaimedByWorker
			}
			return ErrNotFound
		}
		return fmt.Errorf("look up job: %w", err)
	}
	if worker != nil && (orgID != worker.OrgID || zoneID != worker.ZoneID || claimedBy == nil || *claimedBy != worker.ID) {
		slog.WarnContext(ctx, "job result refused: job not claimed by the reporting worker",
			"job", jobID, "worker", worker.ID, "worker_org", worker.OrgID)
		return ErrNotClaimedByWorker
	}
	// A nil boundWorker matches every row (the manager's own finish).
	var boundWorker *uuid.UUID
	if worker != nil {
		boundWorker = &worker.ID
	}
	_, err := q.terminate(ctx, terminalTransition{
		jobID: jobID, status: status, stats: statsJSON, errText: errText,
		from:   []Status{StatusRunning, StatusCancelling},
		worker: boundWorker,
		// Nothing matched: an already terminal job is the idempotent no-op,
		// a re-claimed one refuses the stranger, anything else did not run.
		onMiss: func(ctx context.Context) error {
			var current string
			var nowClaimedBy *uuid.UUID
			if err := q.db.QueryRow(ctx, `SELECT status, worker_id FROM jobs WHERE id = $1`, jobID).Scan(&current, &nowClaimedBy); err != nil {
				return fmt.Errorf("look up job status: %w", err)
			}
			if boundWorker != nil && (nowClaimedBy == nil || *nowClaimedBy != *boundWorker) {
				return ErrNotClaimedByWorker
			}
			if Status(current).Terminal() {
				return nil
			}
			return fmt.Errorf("%w: job is %s", ErrNotRunning, current)
		},
	})
	return err
}

// finishGroupLockKeyPrefix namespaces the per-run-group finish lock within
// hashtext's key space (the same scheme inventory's tree lock uses).
const finishGroupLockKeyPrefix = "job-run-group-finish:"

// sinkGone reports whether a sink failed because the row it records onto
// is gone (the host, repository or credential was deleted while the job
// ran). That is permanent, not retryable: failing the finish for it would
// wedge the job in running forever, so it is logged and skipped.
func sinkGone(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, hosts.ErrNotFound) || errors.Is(err, credentials.ErrNotFound)
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
// sync sink, inside Finish's transaction (ctx carries it). A database or
// sink failure is returned and rolls the whole finish back; a data anomaly
// (a sync job without a repo id) and a repository deleted meanwhile are
// logged and skipped — retrying could never fix them. A cancelled sync
// records nothing: it never ran, and recording last_sync for it would look
// like a successful fetch. A non-sync job records nothing.
func (q *SQLQueue) recordSync(ctx context.Context, jobID uuid.UUID, status Status, errText string) error {
	if q.sync == nil || status == StatusCancelled {
		return nil
	}
	var orgID uuid.UUID
	var kind string
	var payload json.RawMessage
	if err := q.db.QueryRow(ctx,
		`SELECT org_id, kind, payload FROM jobs WHERE id = $1`, jobID,
	).Scan(&orgID, &kind, &payload); err != nil {
		return fmt.Errorf("record sync result: look up finished job: %w", err)
	}
	if Kind(kind) != KindSyncRepo {
		return nil
	}
	var sp syncPayload
	if err := json.Unmarshal(payload, &sp); err != nil || sp.RepoID == uuid.Nil {
		slog.ErrorContext(ctx, "record sync result: job payload carries no repo id", "job", jobID)
		return nil
	}
	if err := q.sync.RecordSyncResult(ctx, orgID, sp.RepoID, q.now(), errText); err != nil {
		if sinkGone(err) {
			slog.WarnContext(ctx, "record sync result: repository no longer exists", "job", jobID)
			return nil
		}
		return fmt.Errorf("record sync result: %w", err)
	}
	return nil
}

// recordTest hands a finished connection_test job's outcome to the optional
// test sink, the connection-test sibling of recordSync, inside Finish's
// transaction. The probe's result travels as the job's final run event
// ("ok" or "failed"), whose payload is the Result JSON — parsed here into
// the sink's narrow arguments. A cancelled test records nothing (it never
// ran); a job without a result event, a malformed one, or a host deleted
// meanwhile is logged and skipped; a database or sink failure rolls the
// finish back.
func (q *SQLQueue) recordTest(ctx context.Context, jobID uuid.UUID, status Status) error {
	if q.test == nil || status == StatusCancelled {
		return nil
	}
	var orgID uuid.UUID
	var kind string
	var hostIDs []uuid.UUID
	if err := q.db.QueryRow(ctx,
		`SELECT org_id, kind, host_ids FROM jobs WHERE id = $1`, jobID,
	).Scan(&orgID, &kind, &hostIDs); err != nil {
		return fmt.Errorf("record test result: look up finished job: %w", err)
	}
	if Kind(kind) != KindConnectionTest {
		return nil
	}
	if len(hostIDs) != 1 {
		slog.ErrorContext(ctx, "record test result: job carries no single host", "job", jobID, "hosts", len(hostIDs))
		return nil
	}
	var at time.Time
	var state string
	var payload json.RawMessage
	if err := q.db.QueryRow(ctx,
		`SELECT at, state, payload FROM job_events WHERE job_id = $1 AND state IN ('ok','failed') ORDER BY seq DESC LIMIT 1`,
		jobID,
	).Scan(&at, &state, &payload); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The job finished without a result event (the executor's
			// parse/transport error path); there is nothing to record.
			return nil
		}
		return fmt.Errorf("record test result: look up result event: %w", err)
	}
	var res testResultPayload
	if err := json.Unmarshal(payload, &res); err != nil {
		slog.ErrorContext(ctx, "record test result: result event is not a result", "err", err, "job", jobID)
		return nil
	}
	if err := q.test.RecordConnectionTest(ctx, orgID, hostIDs[0], at, res.ok(), res.HostKey, res.HostPublicKey, res.HostKeyState, res.Checks); err != nil {
		if sinkGone(err) {
			slog.WarnContext(ctx, "record test result: host no longer exists", "job", jobID)
			return nil
		}
		return fmt.Errorf("record test result: %w", err)
	}
	return nil
}

// testResultPayload is the manager-side mirror of the worker's probe
// result, parsed from the final event's payload. Its ok() must stay the
// sshprobe.Result.OK formula: reachable, authenticated, passwordless sudo
// and Python present — all four, or the test is not ok (a failed sudo or
// Python still yields ok=false; the checks list carries the details).
type testResultPayload struct {
	Reachable     bool                  `json:"reachable"`
	HostKey       string                `json:"host_key"`
	HostPublicKey string                `json:"host_public_key"`
	HostKeyState  string                `json:"host_key_state"`
	Auth          bool                  `json:"auth"`
	Sudo          bool                  `json:"sudo"`
	Python        bool                  `json:"python"`
	Checks        []hosts.HostTestCheck `json:"checks"`
	Err           string                `json:"err,omitempty"`
}

func (r testResultPayload) ok() bool {
	return r.Reachable && r.Auth && r.Sudo && r.Python
}
