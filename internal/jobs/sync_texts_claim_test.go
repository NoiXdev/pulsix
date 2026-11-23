// Backlog 3.127: every sync_repo claim asks the worker for the playbook
// texts (want_playbook_texts); no other claim does. A worker ships them
// only when asked, so an older manager, which never asks, never gets them.
package jobs

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

func TestBuildPayloadAsksOnlyASyncForThePlaybookTexts(t *testing.T) {
	org, zone, repoID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	q := newTestQueue(fakeRenderer{}, fakeCreds{}, fakeTemplates{}, fakeBastions{})
	q.SetRepoSource(fakeRepos{byID: map[uuid.UUID]RepoRef{repoID: {ID: repoID, URL: "https://git.example.org/x.git", Branch: "main"}}})
	sync := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindSyncRepo}
	sync.Payload = sealedPayload(t, q, sync, `{"repo_id":"`+repoID.String()+`"}`)
	out := &workerpb.Job{}
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), sync, out); err != nil {
		t.Fatalf("sync claim: %v", err)
	}
	if !out.GetWantPlaybookTexts() {
		t.Fatal("a sync_repo claim does not ask for the playbook texts")
	}

	// A run (a dry run too) has no report to send texts beside.
	for _, checkMode := range []bool{false, true} {
		rq, run := checkPayloadQueue(t, TemplateRow{PlaybookInline: storedPlaybook})
		run.CheckMode = checkMode
		out := &workerpb.Job{}
		if err := rq.buildPayload(context.Background(), run, out); err != nil {
			t.Fatalf("run claim (check mode %v): %v", checkMode, err)
		}
		if out.GetWantPlaybookTexts() {
			t.Fatalf("a run claim (check mode %v) asks for the playbook texts", checkMode)
		}
	}
}
