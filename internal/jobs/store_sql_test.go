package jobs_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/sshconf"
)

func seedOrg(t *testing.T, q *gen.Queries, slug string) uuid.UUID {
	t.Helper()
	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

// testBoxKey builds a 32-byte master key from a test name.
func testBoxKey(name string) [32]byte {
	var key [32]byte
	copy(key[:], name)
	return key
}

func newJob(org, zone uuid.UUID, kind jobs.Kind, status jobs.Status) jobs.Job {
	tid := uuid.Must(uuid.NewV7())
	actor := uuid.Must(uuid.NewV7())
	hostID := uuid.Must(uuid.NewV7())
	j := jobs.Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: kind,
		Status: status, RunGroupID: uuid.Must(uuid.NewV7()), Trigger: jobs.TriggerManual,
		ActorID: &actor, CreatedAt: time.Now(),
	}
	switch kind {
	case jobs.KindRun:
		j.TemplateID = &tid
		j.HostIDs = []uuid.UUID{hostID}
	case jobs.KindAdhoc:
		// A host-bound claim renders exactly its hosts; none fails closed.
		j.HostIDs = []uuid.UUID{hostID}
	}
	return j
}

// emptySnapshot is the queue-side fake set for tests that only exercise
// the claim mechanics, not the payload contents: an empty inventory, no
// credentials, no templates, no bastions.
type emptySources struct{}

func (emptySources) Snapshot(context.Context, uuid.UUID, render.Scope) (render.Inventory, error) {
	return render.Inventory{Doc: map[string]any{}, Connections: map[string]render.Connection{}}, nil
}

func (emptySources) TemplateLookup(context.Context, uuid.UUID, uuid.UUID) (jobs.TemplateRow, error) {
	return jobs.TemplateRow{}, jobs.ErrNotFound
}

func (emptySources) OpenSecret(context.Context, uuid.UUID, uuid.UUID) (credentials.Secret, error) {
	return credentials.Secret{}, credentials.ErrNotFound
}

func (emptySources) Get(context.Context, uuid.UUID, uuid.UUID) (credentials.Credential, error) {
	return credentials.Credential{}, credentials.ErrNotFound
}

func (emptySources) GetBastion(context.Context, uuid.UUID, uuid.UUID) (sshconf.Bastion, error) {
	return sshconf.Bastion{}, sshconf.ErrNotFound
}

// emptyPins is the KeyPinSource fake the integration queues wire: no host
// carries a pin.
type emptyPins struct{}

func (emptyPins) ChangedHosts(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]bool, error) {
	return map[uuid.UUID]bool{}, nil
}

// ChangedBastions reports no changed jump host, the bastion mirror of
// ChangedHosts (backlog 2.40): the integration queues wire this fake as
// their pins source, and the claim path demands the guard.
func (emptyPins) ChangedBastions(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]bool, error) {
	return map[uuid.UUID]bool{}, nil
}

func (emptyPins) PinnedKeys(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]string, error) {
	return map[uuid.UUID]string{}, nil
}

// TestSQLStoreListJobsByHost: the host filter returns exactly the runs that
// targeted the host, newest first, nothing else, and pages through them
// with the same cursor as the unfiltered list (backlog 7.84: the filter
// reads job_hosts).
func TestSQLStoreListJobsByHost(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-host-filter-org")
	store := jobs.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	zone := seedZone(t, d, org)

	hostA, hostB := seedHost(t, d, org, zone, "a"), seedHost(t, d, org, zone, "b")
	mk := func(hosts ...uuid.UUID) jobs.Job {
		j := newJob(org, zone, jobs.KindRun, jobs.StatusQueued)
		j.HostIDs = hosts
		if err := store.CreateJob(scoped, j, nil); err != nil {
			t.Fatal(err)
		}
		return j
	}
	oldest := mk(hostA)
	mk(hostB)
	newest := mk(hostB, hostA)

	got, next, err := store.ListJobs(scoped, org, jobs.Filter{HostID: &hostA}, "", 10)
	if err != nil || next != "" {
		t.Fatalf("ListJobs by host: next %q, %v", next, err)
	}
	if len(got) != 2 || got[0].ID != newest.ID || got[1].ID != oldest.ID {
		t.Fatalf("by host A = %v, want [%s %s]", got, newest.ID, oldest.ID)
	}
	if all, _, err := store.ListJobs(scoped, org, jobs.Filter{}, "", 10); err != nil || len(all) != 3 {
		t.Fatalf("unfiltered = %d, %v", len(all), err)
	}
	unknown := uuid.Must(uuid.NewV7())
	if none, _, err := store.ListJobs(scoped, org, jobs.Filter{HostID: &unknown}, "", 10); err != nil || len(none) != 0 {
		t.Fatalf("unknown host = %d, %v", len(none), err)
	}

	first, cursor, err := store.ListJobs(scoped, org, jobs.Filter{HostID: &hostA}, "", 1)
	if err != nil || len(first) != 1 || first[0].ID != newest.ID || cursor == "" {
		t.Fatalf("first page by host A = %v, next %q, %v; want [%s] and a cursor", first, cursor, err, newest.ID)
	}
	second, end, err := store.ListJobs(scoped, org, jobs.Filter{HostID: &hostA}, cursor, 1)
	if err != nil || len(second) != 1 || second[0].ID != oldest.ID || end != "" {
		t.Fatalf("second page by host A = %v, next %q, %v; want [%s] and no cursor", second, end, err, oldest.ID)
	}
}

func TestSQLStoreJobCRUD(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org, other := seedOrg(t, q, "jobs-crud-org"), seedOrg(t, q, "jobs-crud-other")
	store := jobs.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	zone := seedZone(t, d, org)

	j := newJob(org, zone, jobs.KindRun, jobs.StatusQueued)
	if err := store.CreateJob(scoped, j, nil); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	got, err := store.GetJob(scoped, org, j.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.ID != j.ID || got.Kind != jobs.KindRun || got.Status != jobs.StatusQueued || got.ZoneID != zone {
		t.Fatalf("GetJob = %+v", got)
	}
	if got.TemplateID == nil || *got.TemplateID != *j.TemplateID {
		t.Fatalf("template id = %v", got.TemplateID)
	}
	if len(got.HostIDs) != 1 || got.HostIDs[0] != j.HostIDs[0] {
		t.Fatalf("host ids = %v", got.HostIDs)
	}
	if got.RunGroupID != j.RunGroupID || got.Trigger != jobs.TriggerManual {
		t.Fatalf("group/trigger = %v/%v", got.RunGroupID, got.Trigger)
	}
	if _, err := store.GetJob(scoped, other, j.ID); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("another org must not see the job: %v", err)
	}
	if _, err := store.GetJob(db.WithOrgScope(ctx, other), other, j.ID); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("scoped read from another org: %v", err)
	}

	// List: three jobs, page of two, then the rest; the id tiebreaker
	// keeps the order deterministic even when created_at collides.
	for i := 0; i < 2; i++ {
		if err := store.CreateJob(scoped, newJob(org, zone, jobs.KindAdhoc, jobs.StatusQueued), nil); err != nil {
			t.Fatal(err)
		}
	}
	page, next, err := store.ListJobs(scoped, org, jobs.Filter{}, "", 2)
	if err != nil || len(page) != 2 || next == "" {
		t.Fatalf("first page = %d, next %q, %v", len(page), next, err)
	}
	rest, next2, err := store.ListJobs(scoped, org, jobs.Filter{}, next, 2)
	if err != nil || len(rest) != 1 || next2 != "" {
		t.Fatalf("second page = %d, next %q, %v", len(rest), next2, err)
	}
	seen := map[uuid.UUID]bool{}
	for _, p := range append(page, rest...) {
		seen[p.ID] = true
	}
	if len(seen) != 3 {
		t.Fatal("pages must not overlap or lose jobs")
	}

	status := jobs.StatusQueued
	kind := jobs.KindRun
	filtered, _, err := store.ListJobs(scoped, org, jobs.Filter{Status: &status, Kind: &kind, ZoneID: &zone}, "", 10)
	if err != nil || len(filtered) != 1 {
		t.Fatalf("filtered list = %d, %v", len(filtered), err)
	}
	if unscoped, _, err := store.ListJobs(db.WithOrgScope(ctx, other), other, jobs.Filter{}, "", 10); err != nil || len(unscoped) != 0 {
		t.Fatalf("other org's list = %d, %v", len(unscoped), err)
	}

	// Cancel: queued → cancelled, running → cancelling, terminal → refused.
	queued := newJob(org, zone, jobs.KindAdhoc, jobs.StatusQueued)
	if err := store.CreateJob(scoped, queued, nil); err != nil {
		t.Fatal(err)
	}
	status, err = store.CancelJob(scoped, org, queued.ID)
	if err != nil || status != jobs.StatusCancelled {
		t.Fatalf("cancel queued = %v, %v", status, err)
	}
	running := newJob(org, zone, jobs.KindAdhoc, jobs.StatusRunning)
	if err := store.CreateJob(scoped, running, nil); err != nil {
		t.Fatal(err)
	}
	if status, err := store.CancelJob(scoped, org, running.ID); err != nil || status != jobs.StatusCancelling {
		t.Fatalf("cancel running = %v, %v", status, err)
	}
	done := newJob(org, zone, jobs.KindAdhoc, jobs.StatusSucceeded)
	if err := store.CreateJob(scoped, done, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelJob(scoped, org, done.ID); !errors.Is(err, jobs.ErrNotRunning) {
		t.Fatalf("cancel terminal: %v", err)
	}
	if _, err := store.CancelJob(scoped, org, uuid.Must(uuid.NewV7())); !errors.Is(err, jobs.ErrNotRunning) {
		t.Fatalf("cancel unknown: %v", err)
	}
}

// TestJobsReferenceAZoneOfTheirOrganization (final review triage): a job's
// zone must be a zone of the job's own organization — the composite
// foreign key every org-owned reference uses — so a job can never be
// queued into another organization's zone (whose workers would claim it).
func TestJobsReferenceAZoneOfTheirOrganization(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org, other := seedOrg(t, q, "jobs-zone-fk-org"), seedOrg(t, q, "jobs-zone-fk-other")
	foreignZone := seedZone(t, d, other)
	store := jobs.NewSQLStore(d)
	for name, zone := range map[string]uuid.UUID{
		"another organization's zone": foreignZone,
		"a zone that does not exist":  uuid.Must(uuid.NewV7()),
	} {
		t.Run(name, func(t *testing.T) {
			err := store.CreateJob(db.WithOrgScope(ctx, org), newJob(org, zone, jobs.KindAdhoc, jobs.StatusQueued), nil)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
				t.Fatalf("CreateJob = %v, want a foreign key violation", err)
			}
		})
	}
	if err := store.CreateJob(db.WithOrgScope(ctx, org), newJob(org, seedZone(t, d, org), jobs.KindAdhoc, jobs.StatusQueued), nil); err != nil {
		t.Fatalf("a job in its own organization's zone: %v", err)
	}
}

// seedZone inserts one zone of org and returns its id: jobs reference a
// zone of their own organization.
func seedZone(t *testing.T, d *db.DB, org uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := d.Pool().Exec(context.Background(),
		`INSERT INTO zones (id, org_id, slug, name) VALUES ($1, $2, $3, 'test zone')`, id, org, "z-"+id.String()[24:]); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedHost inserts one host of org in zone and returns its id: the runs
// list of a host reads job_hosts, which links only hosts that exist.
func seedHost(t *testing.T, d *db.DB, org, zone uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := d.Pool().Exec(context.Background(),
		`INSERT INTO hosts (id, org_id, zone_id, name, address) VALUES ($1, $2, $3, $4, '10.0.0.1')`, id, org, zone, name); err != nil {
		t.Fatal(err)
	}
	return id
}
