package repos_test

import (
	"errors"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/varsource"
)

func TestReplaceVarSourcesStoresThePlaybookTexts(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := repos.NewSQLStore(d)
	org := seedOrg(t, q, "texts-org")
	other := seedOrg(t, q, "texts-other")
	scoped := db.WithOrgScope(ctx, org)
	repoID := seedSourcesRepo(t, d, org, "infra")

	if _, err := store.GetPlaybookText(scoped, org, repoID, "site.yml"); !errors.Is(err, repos.ErrNotFound) {
		t.Fatalf("GetPlaybookText before a sync = %v, want ErrNotFound", err)
	}

	t0 := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	rec := repos.SourcesRecord{Commit: commitA, SyncedAt: t0, WorkerReads: true, Incomplete: true, LeftOut: []string{"kept.yml"}}
	texts := map[string]repos.PlaybookText{
		"site.yml":  text("- hosts: all\n\ttasks: [] # ü \\ \n"),
		"empty.yml": text(""),
		"big.yml":   leftOut(varsource.TextTooLarge),
		"kept.yml":  text("- hosts: kept\n"),
	}
	if err := store.ReplaceVarSources(scoped, org, repoID, rec, []varsource.Playbook{
		playbookSource("big.yml"), playbookSource("empty.yml"), playbookSource("kept.yml"),
		playbookSource("old.yml"), playbookSource("site.yml"),
	}, texts); err != nil {
		t.Fatalf("ReplaceVarSources: %v", err)
	}

	listed, err := store.ListPlaybookTexts(scoped, org, repoID)
	if err != nil {
		t.Fatalf("ListPlaybookTexts: %v", err)
	}
	// old.yml has a row without a text: it is listed as none.
	assertTexts(t, listed, map[string]repos.PlaybookText{
		"site.yml": texts["site.yml"], "empty.yml": texts["empty.yml"], "big.yml": texts["big.yml"],
		"kept.yml": texts["kept.yml"], "old.yml": {},
	})

	for path, want := range map[string]repos.PlaybookText{
		"site.yml": texts["site.yml"], "empty.yml": texts["empty.yml"], "big.yml": texts["big.yml"], "old.yml": {},
	} {
		got, err := store.GetPlaybookText(scoped, org, repoID, path)
		if err != nil {
			t.Fatalf("GetPlaybookText(%s): %v", path, err)
		}
		if got.Commit != commitA || !got.SyncedAt.Equal(t0) || got.Kept {
			t.Fatalf("%s: %+v, want commit %s at %v, not kept", path, got, commitA, t0)
		}
		assertTexts(t, map[string]repos.PlaybookText{path: got.PlaybookText}, map[string]repos.PlaybookText{path: want})
	}
	kept, err := store.GetPlaybookText(scoped, org, repoID, "kept.yml")
	if err != nil || !kept.Kept || kept.Content == nil || *kept.Content != "- hosts: kept\n" {
		t.Fatalf("kept.yml = %+v, %v; want its text, marked kept (the record left it out)", kept, err)
	}
	// A path the record has no row for (removed from the repository, or
	// never reported) answers the record without a text.
	unknown, err := store.GetPlaybookText(scoped, org, repoID, "unknown.yml")
	if err != nil || unknown.Content != nil || unknown.LeftOut != "" || unknown.Kept ||
		unknown.Commit != commitA || !unknown.SyncedAt.Equal(t0) {
		t.Fatalf("GetPlaybookText(unknown path) = %+v, %v; want the record's commit and time, no text", unknown, err)
	}
	// Another organization reads nothing of it.
	if _, err := store.GetPlaybookText(db.WithOrgScope(ctx, other), other, repoID, "site.yml"); !errors.Is(err, repos.ErrNotFound) {
		t.Fatalf("GetPlaybookText from another org = %v, want ErrNotFound", err)
	}

	// The next replace without texts (an older worker) leaves none.
	if err := store.ReplaceVarSources(scoped, org, repoID, repos.SourcesRecord{Commit: commitB, SyncedAt: t0.Add(time.Hour), WorkerReads: true},
		[]varsource.Playbook{playbookSource("site.yml")}, nil); err != nil {
		t.Fatalf("ReplaceVarSources(no texts): %v", err)
	}
	got, err := store.GetPlaybookText(scoped, org, repoID, "site.yml")
	if err != nil || got.Content != nil || got.LeftOut != "" || got.Commit != commitB {
		t.Fatalf("site.yml after a sync without texts = %+v, %v; want no text at %s", got, err, commitB)
	}
}

// TestRecordVarSourcesTextsAcrossSyncs runs the texts through the real
// store: a Failed read keeps the previous text, a successful sync from a
// worker older than the texts replaces it with none.
func TestRecordVarSourcesTextsAcrossSyncs(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "texts-syncs-org")
	scoped := db.WithOrgScope(ctx, org)
	repoID := seedSourcesRepo(t, d, org, "infra")
	store := repos.NewSQLStore(d)
	svc := repos.NewService(store, nil, nil)
	t0 := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)

	textOf := func(t *testing.T) repos.StoredPlaybookText {
		t.Helper()
		got, err := svc.PlaybookText(scoped, org, repoID, "site.yml")
		if err != nil {
			t.Fatalf("PlaybookText: %v", err)
		}
		return got
	}

	if _, err := svc.RecordVarSources(scoped, org, repoID, t0, encodeReport(t, reportOf("site.yml")),
		encodeTexts(t, map[string]any{"site.yml": playText}, map[string]any{})); err != nil {
		t.Fatal(err)
	}
	if got := textOf(t); got.Content == nil || *got.Content != playText || got.Commit != commitB {
		t.Fatalf("after the first sync: %+v", got)
	}

	failed := varsource.Report{Commit: commitA, Failed: true, Playbooks: []varsource.Playbook{},
		Notes: []varsource.Note{varsource.ReadFailed("no playbooks read")}}
	if _, err := svc.RecordVarSources(scoped, org, repoID, t0.Add(time.Hour), encodeReport(t, failed),
		encodeTexts(t, map[string]any{}, map[string]any{})); err != nil {
		t.Fatal(err)
	}
	if got := textOf(t); got.Content == nil || *got.Content != playText || got.Commit != commitB {
		t.Fatalf("after a failed read: %+v, want the previous text kept", got)
	}

	if _, err := svc.RecordVarSources(scoped, org, repoID, t0.Add(2*time.Hour), encodeReport(t, reportOf("site.yml")), nil); err != nil {
		t.Fatal(err)
	}
	if got := textOf(t); got.Content != nil || got.LeftOut != "" {
		t.Fatalf("after a sync without texts: %+v, want no text and no reason", got)
	}
}
