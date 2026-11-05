package playbooks_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
)

func newSQLService(d *db.DB, resolver playbooks.TemplateResolver) *playbooks.Service {
	return playbooks.NewService(playbooks.NewSQLStore(d), repos.NewService(repos.NewSQLStore(d), nil, nil), resolver)
}

// waitingResolver, once armed, holds each new playbook's resolution
// (which runs after the insert, inside the pick's transaction) until a
// second one arrives or wait passes, so two picks that both missed the row
// would both insert before either commits.
type waitingResolver struct {
	mu      sync.Mutex
	calls   int
	arrived chan struct{}
	wait    time.Duration
}

func (r *waitingResolver) arm(wait time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls, r.arrived, r.wait = 0, make(chan struct{}), wait
}

func (r *waitingResolver) ResolveTemplatePlaybooks(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string) error {
	r.mu.Lock()
	arrived, wait := r.arrived, r.wait
	if arrived == nil {
		r.mu.Unlock()
		return nil
	}
	r.calls++
	if r.calls == 2 {
		close(arrived)
	}
	r.mu.Unlock()
	select {
	case <-arrived:
	case <-time.After(wait):
	}
	return nil
}

func countRepoPlaybooks(t *testing.T, d *db.DB, repoID uuid.UUID, path string) int {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM playbooks WHERE repo_id = $1 AND path = $2`, repoID, path).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEnsureRepoReusesTheRow(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	resolver := &waitingResolver{}
	svc := newSQLService(d, resolver)
	org := seedOrg(t, q, "pb-ensure-org")
	scoped := db.WithOrgScope(ctx, org)
	repoID := seedRepo(t, d, org, "infra")

	first, created, err := svc.EnsureRepo(scoped, org, repoID, "site.yml")
	if err != nil || !created {
		t.Fatalf("first EnsureRepo = %+v, %v, %v", first, created, err)
	}
	again, created, err := svc.EnsureRepo(scoped, org, repoID, "site.yml")
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("second EnsureRepo = %+v, %v, %v; want %v reused", again, created, err, first.ID)
	}

	// Rows made before picking existed (several for one path) are reused:
	// the oldest one.
	older, err := svc.CreateRepo(scoped, org, repoID, "old.yml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateRepo(scoped, org, repoID, "old.yml"); err != nil {
		t.Fatal(err)
	}
	got, created, err := svc.EnsureRepo(scoped, org, repoID, "old.yml")
	if err != nil || created || got.ID != older.ID {
		t.Fatalf("EnsureRepo(old.yml) = %v, %v, %v; want the oldest row %v", got.ID, created, err, older.ID)
	}

	// Concurrent picks of one path make one row: the first holds its
	// transaction open in the resolver, the rest wait on the lock and then
	// find its row.
	resolver.arm(500 * time.Millisecond)
	const n = 8
	var wg sync.WaitGroup
	ids := make([]uuid.UUID, n)
	createdFlags := make([]bool, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, c, err := svc.EnsureRepo(scoped, org, repoID, "concurrent.yml")
			ids[i], createdFlags[i], errs[i] = p.ID, c, err
		}()
	}
	wg.Wait()
	createdCount := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("concurrent EnsureRepo %d: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("concurrent EnsureRepo returned %v and %v, want one row", ids[0], ids[i])
		}
		if createdFlags[i] {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("created = true %d times, want once", createdCount)
	}
	if got := countRepoPlaybooks(t, d, repoID, "concurrent.yml"); got != 1 {
		t.Fatalf("rows for concurrent.yml = %d, want 1", got)
	}

	// Another organization neither sees nor reuses the row.
	other := seedOrg(t, q, "pb-ensure-other")
	if _, _, err := svc.EnsureRepo(db.WithOrgScope(ctx, other), other, repoID, "site.yml"); err == nil {
		t.Fatal("another organization picked a playbook of a repository it does not have")
	}
}

// TestEditorPlaybookVarSourceIsStored: the parse result is written with
// the text on create and update, and a result the database cannot hold
// leaves the column NULL instead of failing the save.
func TestEditorPlaybookVarSourceIsStored(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	svc := newSQLService(d, &fakeTemplateResolver{})
	org := seedOrg(t, q, "pb-varsource-org")
	scoped := db.WithOrgScope(ctx, org)

	stored := func(id uuid.UUID) []byte {
		t.Helper()
		var raw []byte
		if err := d.Pool().QueryRow(ctx, `SELECT var_source FROM playbooks WHERE id = $1`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}

	p, err := svc.CreateEditor(scoped, org, playbooks.CreateInput{Inline: markedPlaybook})
	if err != nil {
		t.Fatalf("CreateEditor: %v", err)
	}
	if raw := stored(p.ID); len(raw) == 0 {
		t.Fatal("var_source is NULL after a create")
	}
	src, err := svc.VarSource(scoped, org, p.ID)
	if err != nil || !src.Found || len(src.Playbook.Fields) != 1 || src.Playbook.Fields[0].Name != "http_port" {
		t.Fatalf("VarSource = %+v, %v", src, err)
	}

	if _, err := svc.UpdateEditor(scoped, org, p.ID, playbooks.CreateInput{Inline: validPlaybook}); err != nil {
		t.Fatalf("UpdateEditor: %v", err)
	}
	if src, err := svc.VarSource(scoped, org, p.ID); err != nil || len(src.Playbook.Fields) != 0 {
		t.Fatalf("VarSource after the update = %+v, %v; want no fields", src, err)
	}

	nul := "- hosts: all\n  vars:\n    x: \"a\\0b\"  # pulsix: X\n  tasks: []\n"
	if _, err := svc.UpdateEditor(scoped, org, p.ID, playbooks.CreateInput{Inline: nul}); err != nil {
		t.Fatalf("UpdateEditor(NUL default): %v", err)
	}
	if raw := stored(p.ID); raw != nil {
		t.Fatalf("var_source = %s, want NULL", raw)
	}
	if src, err := svc.VarSource(scoped, org, p.ID); err != nil || len(src.Playbook.Fields) != 1 {
		t.Fatalf("VarSource(NUL default) = %+v, %v", src, err)
	}
}
