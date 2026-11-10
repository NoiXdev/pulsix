package repos_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
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
	if _, pb, err := store.GetPlaybookVarSource(scoped, org, repoID, "deploy.yml"); err != nil || pb != nil {
		t.Fatalf("A's deploy.yml after B = %+v, %v; want no row", pb, err)
	}
	_, pb, err := store.GetPlaybookVarSource(scoped, org, repoID, "site.yml")
	if err != nil || pb == nil || !sameStrings(fieldNames(*pb), []string{"env", "region"}) {
		t.Fatalf("GetPlaybookVarSource(site.yml) = %+v, %v", pb, err)
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
		if _, _, err := store.GetPlaybookVarSource(otherScoped, asOrg, repoID, "site.yml"); !errors.Is(err, repos.ErrNotFound) {
			t.Fatalf("GetPlaybookVarSource from another org = %v, want ErrNotFound", err)
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
	recp, pb, err := svc.PlaybookVarSource(scoped, org, repoID, "deploy.yml")
	if err != nil || recp == nil || pb == nil || recp.WorkerReads || recp.Commit != commitA || !sameStrings(fieldNames(*pb), []string{"app_version"}) {
		t.Fatalf("PlaybookVarSource(deploy.yml) = %+v, %+v, %v", recp, pb, err)
	}
	// A path the record has no row for: the record, no playbook.
	if recp, pb, err := svc.PlaybookVarSource(scoped, org, repoID, "missing.yml"); err != nil || recp == nil || recp.Commit != commitA || pb != nil {
		t.Fatalf("PlaybookVarSource(missing.yml) = %+v, %+v, %v; want the record and no playbook", recp, pb, err)
	}

	// The repository deleted: its sources go with it.
	if _, err := d.Exec(scoped, `DELETE FROM git_repos WHERE id = $1`, repoID); err != nil {
		t.Fatal(err)
	}
	if _, _, found, err := svc.VarSources(scoped, org, repoID); err != nil || found {
		t.Fatalf("VarSources after the repo's delete = found %v, %v", found, err)
	}
	if recp, pb, err := svc.PlaybookVarSource(scoped, org, repoID, "deploy.yml"); err != nil || recp != nil || pb != nil {
		t.Fatalf("PlaybookVarSource without a record = %+v, %+v, %v; want neither", recp, pb, err)
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

// TestUndecodablePlaybookRowCountsAsMissing: a stored playbook row that no
// longer decodes (written by another version) is treated as missing, with
// one log line each time it is met, and never fails a later finish.
func TestUndecodablePlaybookRowCountsAsMissing(t *testing.T) {
	d := testdb.Open(t)
	store := repos.NewSQLStore(d)
	var logs bytes.Buffer
	store.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	svc := repos.NewService(store, nil, nil)
	org := seedOrg(t, gen.New(d.Pool()), "var-sources-undecodable-org")
	scoped := db.WithOrgScope(ctx, org)
	repoID := seedSourcesRepo(t, d, org, "infra")
	t0 := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)

	if err := store.ReplaceVarSources(scoped, org, repoID, repos.SourcesRecord{Commit: commitA, SyncedAt: t0, WorkerReads: true},
		[]varsource.Playbook{playbookSource("site.yml", "env"), playbookSource("old.yml", "x")}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(scoped, `UPDATE repo_playbook_sources SET result = '{"fields": "not a list"}' WHERE repo_id = $1 AND path = 'old.yml'`, repoID); err != nil {
		t.Fatal(err)
	}
	lines := func() int { return strings.Count(logs.String(), "old.yml") }

	pbs, err := store.ListPlaybookSources(scoped, org, repoID)
	if err != nil || !sameStrings(playbookPaths(pbs), []string{"site.yml"}) {
		t.Fatalf("ListPlaybookSources = %v, %v; want site.yml only", playbookPaths(pbs), err)
	}
	if n := lines(); n != 1 {
		t.Fatalf("log lines naming old.yml after the list = %d, want 1:\n%s", n, logs.String())
	}
	if rec, pb, err := store.GetPlaybookVarSource(scoped, org, repoID, "old.yml"); err != nil || pb != nil || rec.Commit != commitA {
		t.Fatalf("GetPlaybookVarSource(old.yml) = %+v, %+v, %v; want the record and no row", rec, pb, err)
	}
	if n := lines(); n != 2 {
		t.Fatalf("log lines naming old.yml after the get = %d, want 2", n)
	}

	// A later finish that keeps the previous rows (a left-out name, then a
	// Failed read) still succeeds; the undecodable row is gone with it.
	leftOut := encodeReport(t, varsource.Report{Commit: commitB, Incomplete: true, LeftOut: []string{"old.yml"},
		Playbooks: []varsource.Playbook{playbookSource("site.yml", "env")}, Notes: []varsource.Note{}})
	if err := d.WithWriteTx(ctx, org, func(ctx context.Context) error {
		_, err := svc.RecordVarSources(ctx, org, repoID, t0.Add(time.Hour), leftOut)
		return err
	}); err != nil {
		t.Fatalf("RecordVarSources(left out) = %v, want the finish to succeed", err)
	}
	failed := encodeReport(t, varsource.Report{Commit: commitB, Failed: true, Playbooks: []varsource.Playbook{},
		Notes: []varsource.Note{varsource.ReadFailed("unreadable")}})
	if err := d.WithWriteTx(ctx, org, func(ctx context.Context) error {
		_, err := svc.RecordVarSources(ctx, org, repoID, t0.Add(2*time.Hour), failed)
		return err
	}); err != nil {
		t.Fatalf("RecordVarSources(failed) = %v, want the finish to succeed", err)
	}
	if pbs, err := store.ListPlaybookSources(scoped, org, repoID); err != nil || !sameStrings(playbookPaths(pbs), []string{"site.yml"}) {
		t.Fatalf("playbooks after the finishes = %v, %v", playbookPaths(pbs), err)
	}
}

// TestGetPlaybookVarSource: the record and one playbook row in one read —
// both when the row exists, the record and no playbook without one, and
// ErrNotFound without a record. Another organization reads nothing.
func TestGetPlaybookVarSource(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := repos.NewSQLStore(d)
	org := seedOrg(t, q, "playbook-var-source-org")
	other := seedOrg(t, q, "playbook-var-source-other")
	scoped := db.WithOrgScope(ctx, org)
	repoID := seedSourcesRepo(t, d, org, "infra")

	if _, _, err := store.GetPlaybookVarSource(scoped, org, repoID, "site.yml"); !errors.Is(err, repos.ErrNotFound) {
		t.Fatalf("without a record = %v, want ErrNotFound", err)
	}
	t0 := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	if err := store.ReplaceVarSources(scoped, org, repoID, repos.SourcesRecord{
		Commit: commitA, SyncedAt: t0, WorkerReads: true, Incomplete: true, LeftOut: []string{"huge.yml"},
		Notes: []varsource.Note{varsource.ReadFailed("one file could not be read")},
	}, []varsource.Playbook{playbookSource("site.yml", "env"), playbookSource("deploy.yml", "app_version")}); err != nil {
		t.Fatal(err)
	}

	rec, pb, err := store.GetPlaybookVarSource(scoped, org, repoID, "site.yml")
	if err != nil || pb == nil || pb.Path != "site.yml" || !sameStrings(fieldNames(*pb), []string{"env"}) {
		t.Fatalf("site.yml = %+v, %+v, %v; want the record and the row", rec, pb, err)
	}
	if rec.Commit != commitA || !rec.SyncedAt.Equal(t0) || !rec.WorkerReads || !rec.Incomplete ||
		!sameStrings(rec.LeftOut, []string{"huge.yml"}) || len(rec.Notes) != 1 {
		t.Fatalf("record = %+v", rec)
	}
	rec, pb, err = store.GetPlaybookVarSource(scoped, org, repoID, "missing.yml")
	if err != nil || pb != nil || rec.Commit != commitA {
		t.Fatalf("missing.yml = %+v, %+v, %v; want the record and no playbook", rec, pb, err)
	}

	otherScoped := db.WithOrgScope(ctx, other)
	for _, asOrg := range []uuid.UUID{other, org} {
		if _, _, err := store.GetPlaybookVarSource(otherScoped, asOrg, repoID, "site.yml"); !errors.Is(err, repos.ErrNotFound) {
			t.Fatalf("from another org (as %s) = %v, want ErrNotFound", asOrg, err)
		}
	}
}

// TestStoredPlaybookResultOverTheReadLimitReadsAsMissing (backlog 7.122):
// a stored result longer, as jsonb renders it, than the read limit is not
// decoded: it reads as missing with a log line, never as an error that
// would fail a sync. One exactly at the limit still decodes.
func TestStoredPlaybookResultOverTheReadLimitReadsAsMissing(t *testing.T) {
	d := testdb.Open(t)
	store := repos.NewSQLStore(d)
	var logs bytes.Buffer
	store.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	org := seedOrg(t, gen.New(d.Pool()), "var-sources-read-limit-org")
	scoped := db.WithOrgScope(ctx, org)
	repoID := seedSourcesRepo(t, d, org, "infra")
	t0 := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

	if err := store.ReplaceVarSources(scoped, org, repoID, repos.SourcesRecord{Commit: commitA, SyncedAt: t0, WorkerReads: true},
		[]varsource.Playbook{playbookSource("at.yml"), playbookSource("over.yml")}); err != nil {
		t.Fatal(err)
	}
	// {"pad": "aaa…"} renders as 11 bytes around the padding.
	for path, size := range map[string]int{"at.yml": repos.MaxStoredResultReadBytes, "over.yml": repos.MaxStoredResultReadBytes + 1} {
		var got int
		if err := d.QueryRow(scoped,
			`UPDATE repo_playbook_sources SET result = jsonb_build_object('pad', repeat('a', $2::int))
			 WHERE repo_id = $1 AND path = $3 RETURNING octet_length(result::text)`, repoID, size-11, path).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != size {
			t.Fatalf("%s: stored result renders as %d bytes, want %d", path, got, size)
		}
	}

	pbs, err := store.ListPlaybookSources(scoped, org, repoID)
	if err != nil || !sameStrings(playbookPaths(pbs), []string{"at.yml"}) {
		t.Fatalf("ListPlaybookSources = %v, %v; want at.yml only", playbookPaths(pbs), err)
	}
	if _, pb, err := store.GetPlaybookVarSource(scoped, org, repoID, "at.yml"); err != nil || pb == nil || pb.Path != "at.yml" {
		t.Fatalf("GetPlaybookVarSource(at.yml) = %+v, %v; want the row", pb, err)
	}
	if rec, pb, err := store.GetPlaybookVarSource(scoped, org, repoID, "over.yml"); err != nil || pb != nil || rec.Commit != commitA {
		t.Fatalf("GetPlaybookVarSource(over.yml) = %+v, %+v, %v; want the record and no row", rec, pb, err)
	}
	if n := strings.Count(logs.String(), "too large to read"); n != 2 || strings.Contains(logs.String(), "at.yml") {
		t.Fatalf("log = %q, want two 'too large to read' lines naming over.yml only", logs.String())
	}
}
