package playbooks_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/varsource"
)

func TestRepoText(t *testing.T) {
	org, repoID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	rs := newFakeRepos(map[uuid.UUID]repos.Repo{repoID: {ID: repoID, OrgID: org, Name: "infra"}})
	svc := newService(newFakeStore(), rs)
	at := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	const commit = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	// The sync record itself: rows can carry another commit (a record
	// without one below), so the answer for a path without a row must not
	// depend on which row a map iteration meets first.
	rs.records = map[uuid.UUID]repos.StoredPlaybookText{repoID: {Commit: commit, SyncedAt: at}}
	rs.texts[repoID] = map[string]repos.StoredPlaybookText{
		"site.yml":   {Commit: commit, SyncedAt: at, PlaybookText: repos.PlaybookText{Content: strPtr(validPlaybook)}},
		"big.yml":    {Commit: commit, SyncedAt: at, PlaybookText: repos.PlaybookText{LeftOut: varsource.TextTooLarge}},
		"old.yml":    {Commit: commit, SyncedAt: at},
		"capped.yml": {Commit: commit, SyncedAt: at, Kept: true, PlaybookText: repos.PlaybookText{Content: strPtr(validPlaybook)}},
	}
	register := func(path string) uuid.UUID {
		t.Helper()
		p, err := svc.CreateRepo(ctx, org, repoID, path)
		if err != nil {
			t.Fatalf("CreateRepo(%s): %v", path, err)
		}
		return p.ID
	}

	for _, tc := range []struct {
		path    string
		content *string
		leftOut string
		commit  string
		earlier bool
	}{
		{"site.yml", strPtr(validPlaybook), "", commit, false},
		{"big.yml", nil, varsource.TextTooLarge, commit, false},
		// No text and no reason: the last sync stored none for it.
		{"old.yml", nil, playbooks.TextNotSent, commit, false},
		// Kept from an earlier read: its commit is not the record's.
		{"capped.yml", strPtr(validPlaybook), "", "", true},
	} {
		got, err := svc.RepoText(ctx, org, register(tc.path))
		if err != nil {
			t.Fatalf("RepoText(%s): %v", tc.path, err)
		}
		if got.Path != tc.path || got.Commit != tc.commit || !got.SyncedAt.Equal(at) || got.LeftOut != tc.leftOut || got.FromEarlierSync != tc.earlier ||
			(got.Content == nil) != (tc.content == nil) || (got.Content != nil && *got.Content != *tc.content) {
			t.Fatalf("RepoText(%s) = %+v, want content %v, left out %q, commit %q, from earlier sync %v", tc.path, got, tc.content, tc.leftOut, tc.commit, tc.earlier)
		}
	}

	// A record without a commit is not a text kept from an earlier sync.
	rs.texts[repoID]["nocommit.yml"] = repos.StoredPlaybookText{SyncedAt: at, PlaybookText: repos.PlaybookText{Content: strPtr(validPlaybook)}}
	nc, err := svc.RepoText(ctx, org, register("nocommit.yml"))
	if err != nil || nc.Commit != "" || nc.FromEarlierSync {
		t.Fatalf("RepoText(record without a commit) = %+v, %v; want commit \"\" and not from an earlier sync", nc, err)
	}

	// A synced repository without a row for the path: no text as of
	// that sync.
	gone, err := svc.RepoText(ctx, org, register("gone.yml"))
	if err != nil || gone.Path != "gone.yml" || gone.Content != nil || gone.LeftOut != playbooks.TextNotSent ||
		gone.Commit != commit || !gone.SyncedAt.Equal(at) {
		t.Fatalf("RepoText(path without a row) = %+v, %v; want not_sent at the record's commit", gone, err)
	}
	// A repository no sync recorded.
	unsynced := uuid.Must(uuid.NewV7())
	rs.byID[unsynced] = repos.Repo{ID: unsynced, OrgID: org, Name: "fresh"}
	fresh, err := svc.CreateRepo(ctx, org, unsynced, "site.yml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RepoText(ctx, org, fresh.ID); !errors.Is(err, playbooks.ErrNoRepoText) {
		t.Fatalf("RepoText(no sync record) = %v, want ErrNoRepoText", err)
	}
	editor, err := svc.CreateEditor(ctx, org, playbooks.CreateInput{Inline: validPlaybook})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RepoText(ctx, org, editor.ID); !errors.Is(err, playbooks.ErrNotRepo) {
		t.Fatalf("RepoText(editor playbook) = %v, want ErrNotRepo", err)
	}
	if _, err := svc.RepoText(ctx, uuid.Must(uuid.NewV7()), register("site.yml")); !errors.Is(err, playbooks.ErrNotFound) {
		t.Fatalf("RepoText(another org) = %v, want ErrNotFound", err)
	}
	rs.textErr = errors.New("connection reset")
	if _, err := svc.RepoText(ctx, org, register("site.yml")); err == nil || errors.Is(err, playbooks.ErrNoRepoText) {
		t.Fatalf("RepoText(store failure) = %v, want the failure", err)
	}
}
