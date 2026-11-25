package jobs

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// pinWorld is one repo-playbook template and an editor-playbook template,
// each targeting one host, with a queue that can build their claims.
type pinWorld struct {
	org, zone, host, repoTmpl, inlineTmpl, repo uuid.UUID
	q                                           *SQLQueue
}

func newPinWorld() pinWorld {
	w := pinWorld{org: uuid.Must(uuid.NewV7()), zone: uuid.Must(uuid.NewV7()), host: uuid.Must(uuid.NewV7()),
		repoTmpl: uuid.Must(uuid.NewV7()), inlineTmpl: uuid.Must(uuid.NewV7()), repo: uuid.Must(uuid.NewV7())}
	scope := json.RawMessage(`{"host_ids":["` + w.host.String() + `"]}`)
	tmpls := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		w.repoTmpl: {ID: w.repoTmpl, OrgID: w.org, PlaybookID: uuid.Must(uuid.NewV7()),
			PlaybookRepo: &w.repo, PlaybookPath: "site.yml", Scope: scope},
		w.inlineTmpl: {ID: w.inlineTmpl, OrgID: w.org, PlaybookID: uuid.Must(uuid.NewV7()),
			PlaybookInline: "- hosts: all", Scope: scope},
	}}
	w.q = newTestQueue(fakeRenderer{inv: render.Inventory{
		Doc:         map[string]any{"all": map[string]any{}},
		Connections: map[string]render.Connection{"web1": {HostID: w.host}},
	}}, fakeCreds{}, tmpls, fakeBastions{})
	w.q.SetRepoSource(fakeRepos{byID: map[uuid.UUID]RepoRef{
		w.repo: {ID: w.repo, URL: "https://git.example.org/ansible.git", Branch: "main"},
	}})
	w.q.SetKeyPins(fakePins{})
	return w
}

func (w pinWorld) build(t *testing.T, tmpl uuid.UUID, commit string) (*workerpb.Job, error) {
	t.Helper()
	out := &workerpb.Job{}
	err := w.q.buildPayload(db.WithOrgScope(context.Background(), w.org), Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ZoneID: w.zone, Kind: KindRun, TemplateID: &tmpl,
		HostIDs: []uuid.UUID{w.host}, PinnedCommit: commit,
	}, out)
	return out, err
}

func repoPayload(t *testing.T, out *workerpb.Job) map[string]any {
	t.Helper()
	var payload map[string]map[string]any
	if err := json.Unmarshal([]byte(out.KindPayload), &payload); err != nil {
		t.Fatal(err)
	}
	return payload["repo"]
}

// TestBuildPayloadPinnedRunShipsTheCommit: the claim of a pinned run tells
// the worker which commit to check out; an unpinned run's claim carries
// no commit, so the worker runs the branch tip as before.
func TestBuildPayloadPinnedRunShipsTheCommit(t *testing.T) {
	w := newPinWorld()
	out, err := w.build(t, w.repoTmpl, testCommit)
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if got := repoPayload(t, out)["commit"]; got != testCommit {
		t.Fatalf("repo.commit = %v, want %q", got, testCommit)
	}

	out, err = w.build(t, w.repoTmpl, "")
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if got, present := repoPayload(t, out)["commit"]; present {
		t.Fatalf("an unpinned run's claim carries repo.commit %v", got)
	}
}

// TestBuildPayloadRefusesAPinItCannotHonour: a pin on a run whose playbook
// no longer comes from a repository, or a pin that is no commit id, fails
// the claim instead of running something else.
func TestBuildPayloadRefusesAPinItCannotHonour(t *testing.T) {
	w := newPinWorld()
	if _, err := w.build(t, w.inlineTmpl, testCommit); err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("an editor playbook's pinned run built: %v", err)
	}
	if _, err := w.build(t, w.repoTmpl, "main"); err == nil || !strings.Contains(err.Error(), "commit") {
		t.Fatalf("a pin that is no commit id built: %v", err)
	}
}

func TestSyncedRefDigestsURLBranchAndSubpath(t *testing.T) {
	base := SyncedRef("https://git.example.org/a.git", "main", "ansible")
	if base != SyncedRef("https://git.example.org/a.git", "main", "ansible") {
		t.Fatal("SyncedRef is not deterministic")
	}
	if len(base) != 64 || strings.Contains(base, "git.example.org") {
		t.Fatalf("SyncedRef = %q, want a hex digest that names nothing", base)
	}
	for name, other := range map[string]string{
		"url":     SyncedRef("https://git.example.org/b.git", "main", "ansible"),
		"branch":  SyncedRef("https://git.example.org/a.git", "dev", "ansible"),
		"subpath": SyncedRef("https://git.example.org/a.git", "main", ""),
		// The fields cannot run into each other.
		"split": SyncedRef("https://git.example.org/a.git", "mainansible", ""),
	} {
		if other == base {
			t.Errorf("a changed %s keeps the digest", name)
		}
	}
}
