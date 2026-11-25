package api_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/cronexpr"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/tenancy"
	"github.com/noixdev/pulsix/internal/triggers"
)

// changeReviewFixture is a run fixture with what runs its template
// unattended: an active schedule, one a person paused, a webhook trigger
// and an assistant token that writes templates and repositories.
type changeReviewFixture struct {
	runRequestFixture
	tok                     string
	active, byPerson, hook  uuid.UUID
	operator                uuid.UUID
	sshCred, vaultCred, git uuid.UUID
}

func newChangeReviewFixture(t *testing.T, slug string) changeReviewFixture {
	t.Helper()
	f := changeReviewFixture{runRequestFixture: newRunRequestFixture(t, slug)}
	f.operator = f.memberID(t, slug+"-operator@example.test")
	f.tok = f.s.AssistantTokenFor(t, f.orgID, f.operator, tenancy.RoleOperator,
		tenancy.AreaTemplatesWrite, tenancy.AreaReposWrite, tenancy.AreaAutomationRequest)
	in := schedules.Input{TemplateID: f.templateID, Name: "nightly", Mode: schedules.ModeCron, Cron: "0 3 * * *", Timezone: "UTC"}
	s, err := f.s.Deps.Schedules.Create(f.scoped, f.orgID, f.operator, in)
	if err != nil {
		t.Fatal(err)
	}
	in.Name = "weekly"
	in.Simple, in.Mode, in.Cron = &cronexpr.Simple{Kind: "weekly", Days: []string{"mon"}, Time: "04:00"}, schedules.ModeSimple, ""
	p, err := f.s.Deps.Schedules.Create(f.scoped, f.orgID, f.operator, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Deps.Schedules.Pause(f.scoped, f.orgID, p.ID); err != nil {
		t.Fatal(err)
	}
	c, err := f.s.Deps.Triggers.Create(f.scoped, f.orgID, f.operator, triggers.Input{
		Kind: triggers.KindTemplateRun, TemplateID: &f.templateID, Name: "deploy-hook", Enabled: true, Preset: triggers.PresetPulsix,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.active, f.byPerson, f.hook = s.ID, p.ID, c.Trigger.ID
	for _, cr := range []struct {
		id *uuid.UUID
		in credentials.CreateInput
	}{
		{&f.sshCred, credentials.CreateInput{Name: "deploy-login", Kind: credentials.KindSSHPassword, Username: "ansible", Material: credentials.Material{Password: "pw-s3cret"}}},
		{&f.vaultCred, credentials.CreateInput{Name: "vault-prod", Kind: credentials.KindVaultPassword, Material: credentials.Material{Password: "vault-s3cret"}}},
		{&f.git, credentials.CreateInput{Name: "git-token", Kind: credentials.KindGitToken, Material: credentials.Material{Token: "git-s3cret"}}},
	} {
		got, err := f.s.Deps.Credentials.Create(f.scoped, f.orgID, cr.in)
		if err != nil {
			t.Fatal(err)
		}
		*cr.id = got.ID
	}
	return f
}

// state reads a schedule's paused state and reason, and a trigger's.
func (f changeReviewFixture) scheduleState(t *testing.T, id uuid.UUID) (paused bool, reason string) {
	t.Helper()
	var r *string
	if err := f.s.DB.Pool().QueryRow(context.Background(), `SELECT paused, paused_reason FROM schedules WHERE id = $1`, id).Scan(&paused, &r); err != nil {
		t.Fatal(err)
	}
	if r != nil {
		reason = *r
	}
	return paused, reason
}

func (f changeReviewFixture) triggerState(t *testing.T, id uuid.UUID) (enabled bool, reason string) {
	t.Helper()
	var r *string
	if err := f.s.DB.Pool().QueryRow(context.Background(), `SELECT enabled, disabled_reason FROM job_triggers WHERE id = $1`, id).Scan(&enabled, &r); err != nil {
		t.Fatal(err)
	}
	if r != nil {
		reason = *r
	}
	return enabled, reason
}

// reviews lists the org's review requests: id by target.
func (f changeReviewFixture) reviews(t *testing.T) map[uuid.UUID][]string {
	t.Helper()
	rows, err := f.s.DB.Pool().Query(context.Background(), `SELECT target_id, id, status FROM run_requests
		WHERE org_id = $1 AND kind IN ('schedule_review', 'trigger_review') ORDER BY created_at`, f.orgID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[uuid.UUID][]string{}
	for rows.Next() {
		var target, id uuid.UUID
		var status string
		if err := rows.Scan(&target, &id, &status); err != nil {
			t.Fatal(err)
		}
		out[target] = append(out[target], id.String()+":"+status)
	}
	return out
}

func (f changeReviewFixture) events(t *testing.T, action string) []string {
	t.Helper()
	rows, err := f.s.DB.Pool().Query(context.Background(), `SELECT object_id || ' ' || summary FROM audit_events WHERE org_id = $1 AND action = $2 ORDER BY at`, f.orgID, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// putTemplate saves the fixture's template with fields changed, as the
// assistant (tok) or a person (c).
func (f changeReviewFixture) putTemplateAsAssistant(t *testing.T, fields map[string]any) {
	t.Helper()
	resp := f.s.AssistantDo(t, http.MethodPut, f.tok, f.templatePath(), "", f.templateBody(fields))
	if resp.Code != http.StatusOK {
		t.Fatalf("assistant PUT template = %d %s", resp.Code, resp.Body)
	}
}

// TestAnAssistantsTemplateChangePausesWhatRunsUnattended (spec 2026-11-25
// C2) through the real stores, for each class of run-relevant template
// field an assistant can change: the active schedule is paused and the
// trigger disabled, each with the reason assistant_change, one review each
// and an activity entry; the schedule a person paused gets the reason and
// a review too (fix round 1).
func TestAnAssistantsTemplateChangePausesWhatRunsUnattended(t *testing.T) {
	cases := map[string]func(t *testing.T, f changeReviewFixture) map[string]any{
		"limit": func(*testing.T, changeReviewFixture) map[string]any { return map[string]any{"limit": "web*"} },
		"extra vars": func(*testing.T, changeReviewFixture) map[string]any {
			return map[string]any{"extra_vars": map[string]any{"deploy_tag": "v2"}}
		},
		"schema": func(*testing.T, changeReviewFixture) map[string]any {
			return map[string]any{"extra_vars_schema": map[string]any{"fields": []map[string]any{{"name": "deploy_tag", "type": "string"}}}}
		},
		"scope": func(_ *testing.T, f changeReviewFixture) map[string]any {
			return map[string]any{"scope": map[string]any{"host_ids": []string{f.hostA.String(), f.hostB.String()}}}
		},
		"playbook": func(t *testing.T, f changeReviewFixture) map[string]any {
			pb, err := f.s.Deps.Playbooks.CreateEditor(f.scoped, f.orgID, playbooks.CreateInput{Inline: "- name: other\n  hosts: all\n  tasks: []\n"})
			if err != nil {
				t.Fatal(err)
			}
			return map[string]any{"playbook_id": pb.ID.String()}
		},
		"credential": func(_ *testing.T, f changeReviewFixture) map[string]any {
			return map[string]any{"credential_id": f.sshCred.String()}
		},
		"vault identities": func(_ *testing.T, f changeReviewFixture) map[string]any {
			return map[string]any{"vault_identities": []map[string]any{{"label": "prod", "credential_id": f.vaultCred.String()}}}
		},
	}
	for name, fields := range cases {
		t.Run(name, func(t *testing.T) {
			f := newChangeReviewFixture(t, "acr-"+strings.ReplaceAll(name, " ", "-"))
			f.putTemplateAsAssistant(t, fields(t, f))
			if paused, reason := f.scheduleState(t, f.active); !paused || reason != schedules.PausedForReview {
				t.Fatalf("active schedule = %v %q", paused, reason)
			}
			if paused, reason := f.scheduleState(t, f.byPerson); !paused || reason != schedules.PausedForReview {
				t.Fatalf("a person's pause = %v %q", paused, reason)
			}
			if enabled, reason := f.triggerState(t, f.hook); enabled || reason != triggers.DisabledForReview {
				t.Fatalf("trigger = %v %q", enabled, reason)
			}
			reviews := f.reviews(t)
			if len(reviews) != 3 || len(reviews[f.active]) != 1 || len(reviews[f.hook]) != 1 || len(reviews[f.byPerson]) != 1 {
				t.Fatalf("reviews = %v", reviews)
			}
			if ev := f.events(t, "pauseScheduleForReview"); len(ev) != 2 {
				t.Fatalf("pause events = %v", ev)
			}
			if ev := f.events(t, "disableTriggerForReview"); len(ev) != 1 || !strings.HasPrefix(ev[0], f.hook.String()) {
				t.Fatalf("disable events = %v", ev)
			}
		})
	}
}

// A person's change keeps everything running; a change of nothing the run
// uses (a rename) by the assistant too.
func TestAPersonsTemplateChangePausesNothing(t *testing.T) {
	f := newChangeReviewFixture(t, "acr-person")
	resp := requestJSON(t, f.adminC, http.MethodPut, f.s.HTTP.URL+f.templatePath(), f.templateBody(map[string]any{"limit": "web*"}))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("person PUT = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	f.putTemplateAsAssistant(t, map[string]any{"limit": "web*", "name": "deploy-renamed"})
	if paused, _ := f.scheduleState(t, f.active); paused {
		t.Fatal("a person's change or an assistant's rename paused the schedule")
	}
	if enabled, _ := f.triggerState(t, f.hook); !enabled {
		t.Fatal("a person's change or an assistant's rename disabled the trigger")
	}
	if r := f.reviews(t); len(r) != 0 {
		t.Fatalf("reviews = %v", r)
	}
	// The scheduler fires it when due (what TestReviewLifecycle's paused
	// schedule must not do).
	if _, err := f.s.DB.Pool().Exec(context.Background(), `UPDATE schedules SET next_run_at = now() - interval '1 minute' WHERE id = $1`, f.active); err != nil {
		t.Fatal(err)
	}
	if n, err := f.ticker().Tick(context.Background()); err != nil || n != 1 {
		t.Fatalf("tick = %d %v, want the schedule fired", n, err)
	}
}

func (f changeReviewFixture) ticker() *schedules.Ticker {
	return schedules.NewTicker(f.s.Deps.Schedules, schedules.NewDueSQLStore(f.s.DB, rand.Reader, slog.New(slog.NewTextHandler(io.Discard, nil))),
		time.Minute, time.Now, nil, nil)
}

type reviewDetail struct {
	Request struct {
		ID       string `json:"id"`
		Kind     string `json:"kind"`
		Status   string `json:"status"`
		TargetID string `json:"target_id"`
	} `json:"request"`
	Picture struct {
		TemplateName string `json:"template_name"`
		Review       *struct {
			Item            string          `json:"item"`
			PausedForReview bool            `json:"paused_for_review"`
			Schedule        json.RawMessage `json:"schedule"`
			Trigger         json.RawMessage `json:"trigger"`
			Changes         []struct {
				Subject string `json:"subject"`
				Name    string `json:"name"`
				TokenID string `json:"token_id"`
				Fields  []struct {
					Field string `json:"field"`
					Old   any    `json:"old"`
					New   any    `json:"new"`
				} `json:"fields"`
			} `json:"changes"`
		} `json:"review"`
	} `json:"picture"`
	Fingerprint string `json:"fingerprint"`
}

func (f changeReviewFixture) reviewDetail(t *testing.T, id string) reviewDetail {
	t.Helper()
	resp := requestJSON(t, f.adminC, http.MethodGet, f.s.HTTP.URL+f.orgPath()+"/run-requests/"+id, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out reviewDetail
	decodeInto(t, resp, &out)
	return out
}

func reviewID(entries []string) string { return strings.SplitN(entries[len(entries)-1], ":", 2)[0] }

// TestReviewLifecycle: the review shows the item, the change (old and new,
// a secret masked) and the token; a second change refreshes the open
// review; a token cannot approve and cannot ask to resume what its change
// paused; the scheduler never fires the paused schedule; approving resumes
// it at the revision seen; rejecting leaves the trigger disabled; a
// person's direct enable closes its review. The schedule and trigger
// pages link the review.
func TestReviewLifecycle(t *testing.T) {
	f := newChangeReviewFixture(t, "acr-life")
	// Due before the change: the ticker must not fire it once paused.
	if _, err := f.s.DB.Pool().Exec(context.Background(), `UPDATE schedules SET next_run_at = now() - interval '1 minute' WHERE id = $1`, f.active); err != nil {
		t.Fatal(err)
	}
	f.putTemplateAsAssistant(t, map[string]any{"limit": "web*", "extra_vars": map[string]any{"deploy_tag": "v2"}})
	if n, err := f.ticker().Tick(context.Background()); err != nil || n != 0 {
		t.Fatalf("tick = %d %v, want nothing fired", n, err)
	}
	reviews := f.reviews(t)
	schedReview, hookReview := reviewID(reviews[f.active]), reviewID(reviews[f.hook])
	d := f.reviewDetail(t, schedReview)
	r := d.Picture.Review
	if d.Request.Kind != "schedule_review" || d.Request.Status != "pending" || d.Request.TargetID != f.active.String() || r == nil ||
		r.Item != "schedule" || !r.PausedForReview || !strings.Contains(string(r.Schedule), `"name":"nightly"`) || len(r.Changes) != 1 ||
		r.Changes[0].Subject != "job_template" || r.Changes[0].Name != "deploy" || r.Changes[0].TokenID == "" {
		t.Fatalf("review = %+v", d)
	}
	got := map[string][2]any{}
	for _, fd := range r.Changes[0].Fields {
		got[fd.Field] = [2]any{fd.Old, fd.New}
	}
	if got["limit"] != [2]any{nil, "web*"} || got["extra_vars.deploy_tag"] != [2]any{"extra-var-s3cret-31d7", "v2"} || len(got) != 2 {
		t.Fatalf("fields = %v", got)
	}

	// A second change refreshes the open reviews: still one per item.
	f.putTemplateAsAssistant(t, map[string]any{"limit": "db*", "extra_vars": map[string]any{"deploy_tag": "v2"}})
	if again := f.reviews(t); len(again[f.active]) != 1 || len(again[f.hook]) != 1 {
		t.Fatalf("after a second change = %v", again)
	}
	d = f.reviewDetail(t, schedReview)
	if len(d.Picture.Review.Changes) != 2 {
		t.Fatalf("changes = %+v", d.Picture.Review.Changes)
	}
	// One entry each for the active and the person-paused schedule, from
	// the first change only.
	if ev := f.events(t, "pauseScheduleForReview"); len(ev) != 2 {
		t.Fatalf("a schedule paused already was paused again: %v", ev)
	}

	// The assistant asks to resume what its change paused: refused.
	if resp := f.s.AssistantDo(t, http.MethodPost, f.tok, f.orgPath()+"/schedules/"+f.active.String()+"/resume-requests", "",
		map[string]any{"reason": "resume it"}); resp.Code != http.StatusUnprocessableEntity || !strings.Contains(resp.Body.String(), "review") {
		t.Fatalf("resume request = %d %s", resp.Code, resp.Body)
	}
	// A token never approves.
	if resp := f.s.AssistantDo(t, http.MethodPost, f.tok, f.orgPath()+"/run-requests/"+schedReview+"/approve", "",
		map[string]any{"fingerprint": d.Fingerprint}); resp.Code != http.StatusForbidden {
		t.Fatalf("token approve = %d", resp.Code)
	}
	// The schedules page links the review.
	list := requestJSON(t, f.adminC, http.MethodGet, f.s.HTTP.URL+f.orgPath()+"/schedules", nil)
	body := mustReadBody(t, list)
	if !strings.Contains(body, `"paused_reason":"assistant_change"`) || !strings.Contains(body, `"review_request_id":"`+schedReview+`"`) {
		t.Fatalf("schedules = %s", body)
	}
	// Approving resumes the schedule.
	if resp := f.approve(t, f.adminC, schedReview, d.Fingerprint); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if paused, reason := f.scheduleState(t, f.active); paused || reason != "" {
		t.Fatalf("after approval = %v %q", paused, reason)
	}
	if ev := f.events(t, "POST resumeSchedule"); len(ev) != 1 || !strings.Contains(ev[0], "approved review "+schedReview) {
		t.Fatalf("resume event = %v", ev)
	}

	// The trigger page links its review; rejecting leaves it disabled.
	tl := requestJSON(t, f.adminC, http.MethodGet, f.s.HTTP.URL+f.orgPath()+"/triggers?template_id="+f.templateID.String(), nil)
	if body := mustReadBody(t, tl); !strings.Contains(body, `"disabled_reason":"assistant_change"`) || !strings.Contains(body, `"review_request_id":"`+hookReview+`"`) {
		t.Fatalf("triggers = %s", body)
	}
	if resp := postJSON(t, f.adminC, f.s.HTTP.URL+f.orgPath()+"/run-requests/"+hookReview+"/reject", map[string]any{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("reject = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if enabled, reason := f.triggerState(t, f.hook); enabled || reason != triggers.DisabledForReview {
		t.Fatalf("after rejection = %v %q", enabled, reason)
	}
	// Another change opens a new review; a person enabling the trigger
	// directly closes it as theirs.
	f.putTemplateAsAssistant(t, map[string]any{"limit": "app*", "extra_vars": map[string]any{"deploy_tag": "v2"}})
	if r := f.reviews(t)[f.hook]; len(r) != 2 || !strings.HasSuffix(r[1], ":pending") {
		t.Fatalf("trigger reviews = %v", r)
	}
	tg, err := f.s.Deps.Triggers.Get(f.scoped, f.orgID, f.hook)
	if err != nil {
		t.Fatal(err)
	}
	put := requestWithHeaders(t, f.adminC, http.MethodPut, f.s.HTTP.URL+f.orgPath()+"/triggers/"+f.hook.String(), map[string]any{
		"kind": "template_run", "template_id": f.templateID.String(), "name": "deploy-hook", "enabled": true, "preset": "pulsix",
	}, map[string]string{"If-Match": `"` + strconv.FormatInt(tg.Revision, 10) + `"`})
	if put.StatusCode != http.StatusOK {
		t.Fatalf("enable = %d %s", put.StatusCode, mustReadBody(t, put))
	}
	if r := f.reviews(t)[f.hook]; !strings.HasSuffix(r[1], ":approved") {
		t.Fatalf("after a person's enable = %v", r)
	}
	if enabled, reason := f.triggerState(t, f.hook); !enabled || reason != "" {
		t.Fatalf("enabled = %v %q", enabled, reason)
	}
}

// TestAnAssistantsRepoChangePausesItsTemplates: a change of the
// repository (each class: URL, branch, subpath, credential, vault
// identities) pauses the schedules of the templates whose playbook comes
// from it, disables their triggers and the trigger that syncs it; a
// person's resume of the schedule closes its review.
func TestAnAssistantsRepoChangePausesItsTemplates(t *testing.T) {
	cases := map[string]func(f changeReviewFixture, body map[string]any){
		"url":     func(_ changeReviewFixture, b map[string]any) { b["url"] = "https://git.example.com/acme/other.git" },
		"branch":  func(_ changeReviewFixture, b map[string]any) { b["branch"] = "release" },
		"subpath": func(_ changeReviewFixture, b map[string]any) { b["subpath"] = "ansible" },
		"credential": func(f changeReviewFixture, b map[string]any) {
			b["credential_id"] = f.git.String()
		},
		"vault identities": func(f changeReviewFixture, b map[string]any) {
			b["vault_identities"] = []map[string]any{{"label": "prod", "credential_id": f.vaultCred.String()}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newChangeReviewFixture(t, "acr-repo-"+strings.ReplaceAll(name, " ", "-"))
			repo := f.repoTemplate(t)
			sync, err := f.s.Deps.Triggers.Create(f.scoped, f.orgID, f.operator, triggers.Input{
				Kind: triggers.KindRepoSync, RepoID: &repo.ID, Name: "push", Enabled: true, Preset: triggers.PresetGitHub,
			})
			if err != nil {
				t.Fatal(err)
			}
			body := map[string]any{"name": repo.Name, "url": repo.URL, "branch": repo.Branch}
			mutate(f, body)
			if resp := f.s.AssistantDo(t, http.MethodPut, f.tok, f.orgPath()+"/repos/"+repo.ID.String(), "", body); resp.Code != http.StatusOK {
				t.Fatalf("assistant PUT repo = %d %s", resp.Code, resp.Body)
			}
			if paused, reason := f.scheduleState(t, f.active); !paused || reason != schedules.PausedForReview {
				t.Fatalf("schedule = %v %q", paused, reason)
			}
			for _, id := range []uuid.UUID{f.hook, sync.Trigger.ID} {
				if enabled, reason := f.triggerState(t, id); enabled || reason != triggers.DisabledForReview {
					t.Fatalf("trigger %s = %v %q", id, enabled, reason)
				}
			}
			reviews := f.reviews(t)
			if len(reviews) != 4 {
				t.Fatalf("reviews = %v", reviews)
			}
			d := f.reviewDetail(t, reviewID(reviews[sync.Trigger.ID]))
			if d.Picture.Review == nil || d.Picture.Review.Item != "trigger" || d.Picture.TemplateName != "" ||
				d.Picture.Review.Changes[0].Subject != "repo" || len(d.Picture.Review.Changes[0].Fields) != 1 {
				t.Fatalf("sync review = %+v", d)
			}
			// A person resumes the schedule directly: its review closes.
			resp := postJSON(t, f.adminC, f.s.HTTP.URL+f.orgPath()+"/schedules/"+f.active.String()+"/resume", map[string]any{})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("resume = %d %s", resp.StatusCode, mustReadBody(t, resp))
			}
			if r := f.reviews(t)[f.active]; !strings.HasSuffix(r[0], ":approved") {
				t.Fatalf("after a person's resume = %v", r)
			}
		})
	}
}

// Fix round 1: a schedule a person paused gets the reason and a review that
// names that person; approving it accepts the change and leaves the
// schedule paused as the person left it, without the reason.
func TestAReviewOfWhatAPersonPausedKeepsItPaused(t *testing.T) {
	f := newChangeReviewFixture(t, "acr-person-paused")
	in := schedules.Input{TemplateID: f.templateID, Name: "monthly", Mode: schedules.ModeCron, Cron: "0 5 1 * *", Timezone: "UTC"}
	sch, err := f.s.Deps.Schedules.Create(f.scoped, f.orgID, f.operator, in)
	if err != nil {
		t.Fatal(err)
	}
	if resp := postJSON(t, f.adminC, f.s.HTTP.URL+f.orgPath()+"/schedules/"+sch.ID.String()+"/pause", map[string]any{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("pause = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	f.putTemplateAsAssistant(t, map[string]any{"limit": "web*"})
	if paused, reason := f.scheduleState(t, sch.ID); !paused || reason != schedules.PausedForReview {
		t.Fatalf("schedule = %v %q", paused, reason)
	}
	id := reviewID(f.reviews(t)[sch.ID])
	resp := requestJSON(t, f.adminC, http.MethodGet, f.s.HTTP.URL+f.orgPath()+"/run-requests/"+id, nil)
	var d struct {
		Picture struct {
			Review struct {
				PausedBefore *struct {
					ByUserID string `json:"by_user_id"`
				} `json:"paused_before"`
			} `json:"review"`
		} `json:"picture"`
		Fingerprint string `json:"fingerprint"`
	}
	decodeInto(t, resp, &d)
	admin := f.memberID(t, "acr-person-paused-admin@example.test")
	if d.Picture.Review.PausedBefore == nil || d.Picture.Review.PausedBefore.ByUserID != admin.String() {
		t.Fatalf("paused before = %+v", d.Picture.Review.PausedBefore)
	}
	// The schedule paused through the service directly has no activity
	// entry naming anyone: its review says it was paused, by nobody named.
	other := f.reviewDetail(t, reviewID(f.reviews(t)[f.byPerson]))
	if other.Request.Kind != "schedule_review" {
		t.Fatalf("other = %+v", other)
	}
	if r := f.approve(t, f.adminC, id, d.Fingerprint); r.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", r.StatusCode, mustReadBody(t, r))
	}
	if paused, reason := f.scheduleState(t, sch.ID); !paused || reason != "" {
		t.Fatalf("after approval = %v %q, want paused without the reason", paused, reason)
	}
}

// Fix round 1: an assistant's edit of an inline playbook's text pauses
// what runs the templates of that playbook.
func TestAnAssistantsPlaybookEditPausesItsTemplates(t *testing.T) {
	f := newChangeReviewFixture(t, "acr-playbook")
	resp := f.s.AssistantDo(t, http.MethodPut, f.tok, f.orgPath()+"/playbooks/"+f.playbook.String(), "",
		map[string]any{"inline": "- name: deploy\n  hosts: web\n  tasks: []\n"})
	if resp.Code != http.StatusOK {
		t.Fatalf("assistant PUT playbook = %d %s", resp.Code, resp.Body)
	}
	if paused, reason := f.scheduleState(t, f.active); !paused || reason != schedules.PausedForReview {
		t.Fatalf("schedule = %v %q", paused, reason)
	}
	if enabled, reason := f.triggerState(t, f.hook); enabled || reason != triggers.DisabledForReview {
		t.Fatalf("trigger = %v %q", enabled, reason)
	}
	d := f.reviewDetail(t, reviewID(f.reviews(t)[f.active]))
	if c := d.Picture.Review.Changes; len(c) != 1 || c[0].Subject != "playbook" || len(c[0].Fields) != 1 || c[0].Fields[0].Field != "playbook_text" {
		t.Fatalf("changes = %+v", c)
	}
	// A person's edit pauses nothing.
	g := newChangeReviewFixture(t, "acr-playbook-person")
	put := requestJSON(t, g.adminC, http.MethodPut, g.s.HTTP.URL+g.orgPath()+"/playbooks/"+g.playbook.String(),
		map[string]any{"inline": "- name: deploy\n  hosts: web\n  tasks: []\n"})
	if put.StatusCode != http.StatusOK {
		t.Fatalf("person PUT playbook = %d %s", put.StatusCode, mustReadBody(t, put))
	}
	if paused, _ := g.scheduleState(t, g.active); paused {
		t.Fatal("a person's playbook edit paused the schedule")
	}
}

// Fix round 1: switching a template's variables mode is a run-relevant
// change.
func TestAnAssistantsVariablesModeSwitchPausesTheTemplate(t *testing.T) {
	f := newChangeReviewFixture(t, "acr-vars-mode")
	url := f.s.HTTP.URL + f.templatePath() + "/variables"
	var preview struct {
		Preview struct {
			SourceRev string `json:"source_rev"`
		} `json:"preview"`
		Source struct {
			SyncedAt *time.Time `json:"synced_at"`
		} `json:"source"`
	}
	decodeInto(t, postJSON(t, f.adminC, url+"/preview", map[string]any{}), &preview)
	apply := map[string]any{"mode": "manual", "source_rev": preview.Preview.SourceRev}
	if preview.Source.SyncedAt != nil {
		apply["source_synced_at"] = preview.Source.SyncedAt
	}
	if resp := postJSON(t, f.adminC, url+"/apply", apply); resp.StatusCode != http.StatusOK {
		t.Fatalf("link = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if paused, _ := f.scheduleState(t, f.active); paused {
		t.Fatal("a person's link paused the schedule")
	}
	resp := f.s.AssistantDo(t, http.MethodPut, f.tok, f.templatePath()+"/variables/mode", "", map[string]any{"mode": "automatic"})
	if resp.Code != http.StatusOK {
		t.Fatalf("assistant mode = %d %s", resp.Code, resp.Body)
	}
	if paused, reason := f.scheduleState(t, f.active); !paused || reason != schedules.PausedForReview {
		t.Fatalf("schedule = %v %q", paused, reason)
	}
	d := f.reviewDetail(t, reviewID(f.reviews(t)[f.active]))
	if c := d.Picture.Review.Changes; len(c) != 1 || c[0].Fields[0].Field != "vars_mode" || c[0].Fields[0].New != "automatic" {
		t.Fatalf("changes = %+v", c)
	}
}
