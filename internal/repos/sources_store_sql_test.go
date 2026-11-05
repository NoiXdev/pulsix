package repos_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/varsource"
)

const (
	commitA = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	commitB = "9f8e7d6c5b4a39281706f5e4d3c2b1a098765432"
)

// seedSourcesRepo inserts one repository row the variable sources hang off.
func seedSourcesRepo(t *testing.T, d *db.DB, orgID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := d.Exec(db.WithOrgScope(ctx, orgID),
		`INSERT INTO git_repos (id, org_id, name, url) VALUES ($1, $2, $3, 'https://git.example.com/acme/infra.git')`,
		id, orgID, name); err != nil {
		t.Fatal(err)
	}
	return id
}

// playbookSource is one read playbook declaring the named string fields.
func playbookSource(path string, fields ...string) varsource.Playbook {
	pb := varsource.Playbook{
		Path: path, Fields: []schema.Field{}, SetInPlaybook: []string{},
		Roles: []varsource.RoleUse{}, Files: []varsource.FileUse{}, Notes: []varsource.Note{},
	}
	for _, f := range fields {
		pb.Fields = append(pb.Fields, schema.Field{Name: f, Type: schema.TypeString})
	}
	return pb
}

func fieldNames(pb varsource.Playbook) []string {
	out := make([]string, len(pb.Fields))
	for i, f := range pb.Fields {
		out[i] = f.Name
	}
	return out
}

func playbookPaths(pbs []varsource.Playbook) []string {
	out := make([]string, len(pbs))
	for i, pb := range pbs {
		out[i] = pb.Path
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestReplaceVarSourcesReplacesThePreviousRecord(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := repos.NewSQLStore(d)
	org := seedOrg(t, q, "var-sources-org")
	other := seedOrg(t, q, "var-sources-other")
	scoped := db.WithOrgScope(ctx, org)
	repoID := seedSourcesRepo(t, d, org, "infra")

	t0 := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	recA := repos.SourcesRecord{
		Commit: commitA, SyncedAt: t0, WorkerReads: true, Incomplete: true,
		LeftOut: []string{"huge.yml"},
		Notes:   []varsource.Note{varsource.ReadFailed("one file could not be read")},
	}
	if err := store.ReplaceVarSources(scoped, org, repoID, recA, []varsource.Playbook{
		playbookSource("deploy.yml", "app_version"), playbookSource("site.yml", "env"),
	}); err != nil {
		t.Fatalf("ReplaceVarSources(A): %v", err)
	}
	got, err := store.GetVarSources(scoped, org, repoID)
	if err != nil {
		t.Fatalf("GetVarSources(A): %v", err)
	}
	if got.Commit != commitA || !got.SyncedAt.Equal(t0) || !got.WorkerReads || !got.Incomplete ||
		!sameStrings(got.LeftOut, []string{"huge.yml"}) || len(got.Notes) != 1 || got.Notes[0] != recA.Notes[0] {
		t.Fatalf("record A = %+v", got)
	}

	t1 := t0.Add(time.Hour)
	recB := repos.SourcesRecord{Commit: commitB, SyncedAt: t1, WorkerReads: true}
	if err := store.ReplaceVarSources(scoped, org, repoID, recB, []varsource.Playbook{
		playbookSource("site.yml", "env", "region"),
	}); err != nil {
		t.Fatalf("ReplaceVarSources(B): %v", err)
	}
	got, err = store.GetVarSources(scoped, org, repoID)
	if err != nil {
		t.Fatalf("GetVarSources(B): %v", err)
	}
	if got.Commit != commitB || !got.SyncedAt.Equal(t1) || !got.WorkerReads || got.Incomplete ||
		len(got.LeftOut) != 0 || len(got.Notes) != 0 {
		t.Fatalf("record B = %+v", got)
	}
	pbs, err := store.ListPlaybookSources(scoped, org, repoID)
	if err != nil {
		t.Fatalf("ListPlaybookSources: %v", err)
	}
	if len(pbs) != 1 || pbs[0].Path != "site.yml" || !sameStrings(fieldNames(pbs[0]), []string{"env", "region"}) {
		t.Fatalf("playbooks after B = %+v", pbs)
	}
	if _, err := store.GetPlaybookSource(scoped, org, repoID, "deploy.yml"); !errors.Is(err, repos.ErrNotFound) {
		t.Fatalf("A's deploy.yml after B = %v, want ErrNotFound", err)
	}
	pb, err := store.GetPlaybookSource(scoped, org, repoID, "site.yml")
	if err != nil || !sameStrings(fieldNames(pb), []string{"env", "region"}) {
		t.Fatalf("GetPlaybookSource(site.yml) = %+v, %v", pb, err)
	}

	// Another organization reads nothing of it, neither under its own id
	// nor by naming the owner's.
	otherScoped := db.WithOrgScope(ctx, other)
	for _, asOrg := range []uuid.UUID{other, org} {
		if _, err := store.GetVarSources(otherScoped, asOrg, repoID); !errors.Is(err, repos.ErrNotFound) {
			t.Fatalf("GetVarSources from another org (as %s) = %v, want ErrNotFound", asOrg, err)
		}
		if pbs, err := store.ListPlaybookSources(otherScoped, asOrg, repoID); err != nil || len(pbs) != 0 {
			t.Fatalf("ListPlaybookSources from another org = %+v, %v", pbs, err)
		}
		if _, err := store.GetPlaybookSource(otherScoped, asOrg, repoID, "site.yml"); !errors.Is(err, repos.ErrNotFound) {
			t.Fatalf("GetPlaybookSource from another org = %v, want ErrNotFound", err)
		}
	}

	// A repository that does not exist (deleted while its sync ran) is
	// ErrNotFound and writes nothing.
	gone := uuid.Must(uuid.NewV7())
	if err := store.ReplaceVarSources(scoped, org, gone, recB, []varsource.Playbook{playbookSource("site.yml")}); !errors.Is(err, repos.ErrNotFound) {
		t.Fatalf("ReplaceVarSources(gone repo) = %v, want ErrNotFound", err)
	}
	var n int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM repo_playbook_sources WHERE repo_id = $1`, gone).Scan(&n); err != nil || n != 0 {
		t.Fatalf("playbook rows of the gone repo = %d, %v", n, err)
	}
}

func TestMarkVarSourcesUnreadKeepsTheRecord(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := repos.NewSQLStore(d)
	org := seedOrg(t, q, "var-sources-unread-org")
	scoped := db.WithOrgScope(ctx, org)
	repoID := seedSourcesRepo(t, d, org, "infra")

	t0 := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	if err := store.ReplaceVarSources(scoped, org, repoID, repos.SourcesRecord{Commit: commitA, SyncedAt: t0, WorkerReads: true}, []varsource.Playbook{
		playbookSource("deploy.yml", "app_version"), playbookSource("site.yml", "env"),
	}); err != nil {
		t.Fatalf("ReplaceVarSources: %v", err)
	}
	if err := store.MarkVarSourcesUnread(scoped, org, repoID, t0.Add(time.Hour)); err != nil {
		t.Fatalf("MarkVarSourcesUnread: %v", err)
	}
	got, err := store.GetVarSources(scoped, org, repoID)
	if err != nil {
		t.Fatalf("GetVarSources: %v", err)
	}
	if got.Commit != commitA || got.WorkerReads || !got.SyncedAt.Equal(t0) {
		t.Fatalf("record after an older worker's sync = %+v, want commit %s kept, synced_at kept, worker_reads false", got, commitA)
	}
	if pbs, err := store.ListPlaybookSources(scoped, org, repoID); err != nil || !sameStrings(playbookPaths(pbs), []string{"deploy.yml", "site.yml"}) {
		t.Fatalf("playbooks after an older worker's sync = %+v, %v", pbs, err)
	}

	// No record yet: an empty one, flagged.
	fresh := seedSourcesRepo(t, d, org, "fresh")
	at := t0.Add(2 * time.Hour)
	if err := store.MarkVarSourcesUnread(scoped, org, fresh, at); err != nil {
		t.Fatalf("MarkVarSourcesUnread(fresh): %v", err)
	}
	got, err = store.GetVarSources(scoped, org, fresh)
	if err != nil {
		t.Fatalf("GetVarSources(fresh): %v", err)
	}
	if got.Commit != "" || got.WorkerReads || !got.SyncedAt.Equal(at) || got.Incomplete || len(got.LeftOut) != 0 || len(got.Notes) != 0 {
		t.Fatalf("fresh record = %+v", got)
	}
	if pbs, err := store.ListPlaybookSources(scoped, org, fresh); err != nil || len(pbs) != 0 {
		t.Fatalf("fresh playbooks = %+v, %v", pbs, err)
	}

	// A newer worker's read clears the flag again.
	if err := store.ReplaceVarSources(scoped, org, fresh, repos.SourcesRecord{Commit: commitB, SyncedAt: at, WorkerReads: true}, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetVarSources(scoped, org, fresh); err != nil || !got.WorkerReads || got.Commit != commitB {
		t.Fatalf("record after a newer worker's read = %+v, %v", got, err)
	}

	// A repository gone meanwhile: nothing written, no error.
	gone := uuid.Must(uuid.NewV7())
	if err := store.MarkVarSourcesUnread(scoped, org, gone, at); err != nil {
		t.Fatalf("MarkVarSourcesUnread(gone) = %v, want nil", err)
	}
	if _, err := store.GetVarSources(scoped, org, gone); !errors.Is(err, repos.ErrNotFound) {
		t.Fatalf("GetVarSources(gone) = %v, want ErrNotFound", err)
	}
}

// TestRecordVarSourcesThroughTheSQLStore runs the service over the real
// store: a read, a Failed read that keeps it, an older worker's sync that
// flags it, and the reads the API serves.
func TestRecordVarSourcesThroughTheSQLStore(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	svc := repos.NewService(repos.NewSQLStore(d), nil, nil)
	org := seedOrg(t, q, "var-sources-service-org")
	scoped := db.WithOrgScope(ctx, org)
	repoID := seedSourcesRepo(t, d, org, "infra")

	if _, _, found, err := svc.VarSources(scoped, org, repoID); err != nil || found {
		t.Fatalf("VarSources before any sync = found %v, %v", found, err)
	}
	t0 := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	read := encodeReport(t, varsource.Report{Commit: commitA, Playbooks: []varsource.Playbook{
		playbookSource("deploy.yml", "app_version"), playbookSource("site.yml", "env"),
	}, Notes: []varsource.Note{}})
	if stored, err := svc.RecordVarSources(scoped, org, repoID, t0, read); err != nil || !stored {
		t.Fatalf("RecordVarSources = %v, %v", stored, err)
	}
	t1 := t0.Add(time.Hour)
	failed := encodeReport(t, varsource.Report{Commit: commitB, Failed: true, Playbooks: []varsource.Playbook{},
		Notes: []varsource.Note{varsource.ReadFailed("the repository subpath could not be opened; no playbooks discovered")}})
	if stored, err := svc.RecordVarSources(scoped, org, repoID, t1, failed); err != nil || stored {
		t.Fatalf("RecordVarSources(failed) = %v, %v; want false, nil", stored, err)
	}
	rec, pbs, found, err := svc.VarSources(scoped, org, repoID)
	if err != nil || !found {
		t.Fatalf("VarSources = found %v, %v", found, err)
	}
	if rec.Commit != commitA || !rec.SyncedAt.Equal(t1) || !rec.WorkerReads || len(rec.Notes) != 1 ||
		!sameStrings(playbookPaths(pbs), []string{"deploy.yml", "site.yml"}) {
		t.Fatalf("after a failed read = %+v, %v", rec, playbookPaths(pbs))
	}

	if stored, err := svc.RecordVarSources(scoped, org, repoID, t1.Add(time.Hour), nil); err != nil || stored {
		t.Fatalf("RecordVarSources(older worker) = %v, %v; want false, nil", stored, err)
	}
	rec, pb, found, err := svc.PlaybookVarSource(scoped, org, repoID, "deploy.yml")
	if err != nil || !found || rec.WorkerReads || rec.Commit != commitA || !sameStrings(fieldNames(pb), []string{"app_version"}) {
		t.Fatalf("PlaybookVarSource(deploy.yml) = %+v, %+v, %v, %v", rec, pb, found, err)
	}
	if _, _, found, err := svc.PlaybookVarSource(scoped, org, repoID, "missing.yml"); err != nil || found {
		t.Fatalf("PlaybookVarSource(missing.yml) = found %v, %v", found, err)
	}

	// The repository deleted: its sources go with it.
	if _, err := d.Exec(scoped, `DELETE FROM git_repos WHERE id = $1`, repoID); err != nil {
		t.Fatal(err)
	}
	if _, _, found, err := svc.VarSources(scoped, org, repoID); err != nil || found {
		t.Fatalf("VarSources after the repo's delete = found %v, %v", found, err)
	}
	// A sync of a repository gone meanwhile is skipped, never an error.
	if stored, err := svc.RecordVarSources(scoped, org, repoID, t1, read); err != nil || stored {
		t.Fatalf("RecordVarSources(gone repo) = %v, %v; want false, nil", stored, err)
	}
}

func encodeReport(t *testing.T, r varsource.Report) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestLockVarSourcesHoldsTheRepositoryUntilTheTransactionEnds: inside a
// write transaction (the job finish's) the lock keeps a second finish of
// the same repository from reading its previous record until the first
// commits; a repository gone meanwhile is ErrNotFound.
func TestLockVarSourcesHoldsTheRepositoryUntilTheTransactionEnds(t *testing.T) {
	d := testdb.Open(t)
	store := repos.NewSQLStore(d)
	org := seedOrg(t, gen.New(d.Pool()), "var-sources-lock-org")
	repoID := seedSourcesRepo(t, d, org, "infra")

	if err := store.LockVarSources(db.WithOrgScope(ctx, org), org, uuid.Must(uuid.NewV7())); !errors.Is(err, repos.ErrNotFound) {
		t.Fatalf("LockVarSources(gone) = %v, want ErrNotFound", err)
	}
	locked, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- d.WithWriteTx(ctx, org, func(ctx context.Context) error {
			if err := store.LockVarSources(ctx, org, repoID); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	waitCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	err := d.WithWriteTx(waitCtx, org, func(ctx context.Context) error { return store.LockVarSources(ctx, org, repoID) })
	if err == nil {
		t.Fatal("a second lock while the first transaction is open = nil, want it to wait (cancelled here)")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first transaction: %v", err)
	}
	if err := d.WithWriteTx(ctx, org, func(ctx context.Context) error { return store.LockVarSources(ctx, org, repoID) }); err != nil {
		t.Fatalf("lock after the first committed: %v", err)
	}
}
