// Backlog 3.126: what a check-mode claim ships. The job row's check_mode is
// the authority for --check; the sealed overrides add --diff, --tags and
// the unsaved draft, and only to a check-mode row. Nothing a payload says
// can make a row run for real with a draft, and nothing it lacks can make
// a check-mode row run for real.
package jobs

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

const (
	storedPlaybook = "- hosts: all\n  tasks: []\n"
	draftPlaybook  = "- hosts: all\n  tasks:\n    - ansible.builtin.debug: {msg: draft}\n"
)

// checkPayloadQueue is a queue over one editor-playbook template.
func checkPayloadQueue(t *testing.T, tmpl TemplateRow) (*SQLQueue, Job) {
	t.Helper()
	org, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	tmpl.ID, tmpl.OrgID = templateID, org
	q := newTestQueue(fakeRenderer{inv: render.Inventory{Doc: map[string]any{}}}, fakeCreds{}, fakeTemplates{byID: map[uuid.UUID]TemplateRow{templateID: tmpl}}, fakeBastions{})
	q.SetKeyPins(fakePins{})
	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: uuid.Must(uuid.NewV7()), Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts}
	return q, job
}

func TestBuildPayloadShipsACheckModeRunsFlagsAndDraft(t *testing.T) {
	draft := draftPlaybook
	for _, tc := range []struct {
		name      string
		checkMode bool
		ov        *RunOverrides
		want      *workerpb.Job // only Check, Diff, Tags, Playbook are compared
	}{
		{
			name: "a dry run of the draft", checkMode: true,
			ov:   &RunOverrides{Check: true, Diff: true, Tags: []string{"web", "db:init"}, Playbook: &draft},
			want: &workerpb.Job{Check: true, Diff: true, Tags: []string{"web", "db:init"}, Playbook: draftPlaybook},
		},
		{
			name: "a dry run of the stored playbook", checkMode: true,
			ov:   &RunOverrides{Check: true},
			want: &workerpb.Job{Check: true, Playbook: storedPlaybook},
		},
		{
			// The column is authoritative for check: overrides that lost
			// their Check (impossible through the service) still never run
			// a check-mode row for real — and bring no draft.
			name: "a check-mode row whose overrides lack check", checkMode: true,
			ov:   &RunOverrides{Diff: true, Tags: []string{"web"}, Playbook: &draft},
			want: &workerpb.Job{Check: true, Diff: true, Tags: []string{"web"}, Playbook: storedPlaybook},
		},
		{
			name: "a check-mode row without overrides", checkMode: true,
			want: &workerpb.Job{Check: true, Playbook: storedPlaybook},
		},
		{
			// A forged payload on an ordinary run: the draft, the diff and
			// the tags belong to a dry run only, so none of them ships.
			name: "an ordinary run whose payload names a draft",
			ov:   &RunOverrides{Diff: true, Tags: []string{"web"}, Playbook: &draft},
			want: &workerpb.Job{Playbook: storedPlaybook},
		},
		{
			name: "an ordinary run",
			want: &workerpb.Job{Playbook: storedPlaybook},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, job := checkPayloadQueue(t, TemplateRow{PlaybookInline: storedPlaybook})
			job.CheckMode = tc.checkMode
			if tc.ov != nil {
				job.Payload = sealedOverride(t, q, job, *tc.ov)
			}
			out := &workerpb.Job{}
			if err := q.buildPayload(context.Background(), job, out); err != nil {
				t.Fatalf("buildPayload: %v", err)
			}
			if out.GetCheck() != tc.want.GetCheck() || out.GetDiff() != tc.want.GetDiff() ||
				!slices.Equal(out.GetTags(), tc.want.GetTags()) || out.GetPlaybook() != tc.want.GetPlaybook() {
				t.Fatalf("claim = check %v diff %v tags %q playbook %q; want check %v diff %v tags %q playbook %q",
					out.GetCheck(), out.GetDiff(), out.GetTags(), out.GetPlaybook(),
					tc.want.GetCheck(), tc.want.GetDiff(), tc.want.GetTags(), tc.want.GetPlaybook())
			}
		})
	}
}

// TestBuildPayloadRefusesInconsistentCheckModeClaims: the claims that
// cannot be shipped safely fail the job instead.
func TestBuildPayloadRefusesInconsistentCheckModeClaims(t *testing.T) {
	repoID := uuid.Must(uuid.NewV7())
	repoDraft := "repo:" + repoID.String() + ":site.yml"
	draft := draftPlaybook
	for _, tc := range []struct {
		name      string
		tmpl      TemplateRow
		kind      Kind
		checkMode bool
		ov        *RunOverrides
	}{
		{
			// Overrides asking for a dry run on a row that is not one: the
			// requester wanted nothing changed, so it never runs for real.
			name: "check overrides on an ordinary row", tmpl: TemplateRow{PlaybookInline: storedPlaybook}, kind: KindRun,
			ov: &RunOverrides{Check: true, Playbook: &draft},
		},
		{
			name: "a draft for a repository playbook", tmpl: TemplateRow{PlaybookRepo: &repoID, PlaybookPath: "site.yml"}, kind: KindRun,
			checkMode: true, ov: &RunOverrides{Check: true, Playbook: &draft},
		},
		{
			// A draft spelled like a repository reference would make the
			// claim fetch (and open the credential of) a repository.
			name: "a draft spelled as a repository reference", tmpl: TemplateRow{PlaybookInline: storedPlaybook}, kind: KindRun,
			checkMode: true, ov: &RunOverrides{Check: true, Playbook: &repoDraft},
		},
		{
			// Only a template run can be a dry run: any other kind would
			// ship without the flag and run for real.
			name: "a check-mode row of another kind", tmpl: TemplateRow{PlaybookInline: storedPlaybook}, kind: KindAdhoc,
			checkMode: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, job := checkPayloadQueue(t, tc.tmpl)
			q.SetRepoSource(fakeRepos{byID: map[uuid.UUID]RepoRef{repoID: {ID: repoID}}})
			job.Kind, job.CheckMode = tc.kind, tc.checkMode
			if tc.kind != KindRun {
				job.TemplateID = nil
			}
			if tc.ov != nil {
				job.Payload = sealedOverride(t, q, job, *tc.ov)
			}
			out := &workerpb.Job{}
			err := q.buildPayload(context.Background(), job, out)
			if !errors.Is(err, ErrInvalidDryRun) {
				t.Fatalf("buildPayload = %v (claim %v), want ErrInvalidDryRun", err, out)
			}
		})
	}
}
