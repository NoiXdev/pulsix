package templates_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/templates"
)

// recordingWatcher records the changes it is handed; err fails the next.
type recordingWatcher struct {
	got []assistantchange.Change
	err error
}

func (w *recordingWatcher) Changed(_ context.Context, _ uuid.UUID, c assistantchange.Change) error {
	if w.err != nil {
		return w.err
	}
	w.got = append(w.got, c)
	return nil
}

// saveInput is the update that saves t as it is.
func saveInput(t templates.Template) templates.UpdateInput {
	return templates.UpdateInput{
		Name: t.Name, PlaybookID: t.PlaybookID, Scope: t.Scope, Limit: t.Limit, ExtraVars: t.ExtraVars,
		ExtraVarsSchema: t.ExtraVarsSchema, CredentialID: t.CredentialID, VaultCredentials: t.VaultCredentials,
	}
}

// TestAnAssistantsRunRelevantChangeIsHandedOn (spec 2026-11-25 C2): an
// update made with an assistant token that moves a field the run uses hands
// the watcher one change naming exactly the fields that moved, old and new,
// with secrets masked. A change of nothing the run uses (the name) and a
// person's change hand on nothing.
func TestAnAssistantsRunRelevantChangeIsHandedOn(t *testing.T) {
	token := uuid.Must(uuid.NewV7())
	user := uuid.Must(uuid.NewV7())
	assistant := assistantchange.Actor{UserID: user, TokenID: &token, Assistant: true}
	secretSchema := &schema.Schema{Fields: []schema.Field{
		{Name: "tag", Type: schema.TypeString},
		{Name: "password", Type: schema.TypeSecret},
	}}
	for _, tc := range []struct {
		name   string
		mutate func(f fixture, in *templates.UpdateInput)
		want   []assistantchange.Field
	}{
		{"playbook", func(f fixture, in *templates.UpdateInput) { in.PlaybookID = f.playbook2 },
			nil}, // checked by field name below: labels name editor playbooks by id
		{"scope", func(f fixture, in *templates.UpdateInput) {
			in.Scope = templates.Scope{HostIDs: []uuid.UUID{f.managed}}
		},
			[]assistantchange.Field{{Name: "scope", Old: "all managed hosts", New: "hosts web-1"}}},
		{"limit", func(_ fixture, in *templates.UpdateInput) { in.Limit = "db*" },
			[]assistantchange.Field{{Name: "limit", Old: "web*", New: "db*"}}},
		{"extra var value", func(_ fixture, in *templates.UpdateInput) {
			in.ExtraVars = map[string]any{"tag": "v2", "password": schema.Mask()}
		}, []assistantchange.Field{{Name: "extra_vars.tag", Old: "v1", New: "v2"}}},
		{"secret value", func(_ fixture, in *templates.UpdateInput) {
			in.ExtraVars = map[string]any{"tag": "v1", "password": "another-s3cret"}
		}, []assistantchange.Field{{Name: "extra_vars.password", Old: schema.Mask(), New: schema.Mask()}}},
		{"credential", func(f fixture, in *templates.UpdateInput) { in.CredentialID = nil },
			[]assistantchange.Field{{Name: "credential", Old: "ssh", New: nil}}},
		{"vault identities", func(f fixture, in *templates.UpdateInput) {
			in.VaultCredentials = []templates.VaultCredential{{Label: "prod", CredentialID: f.vaultCred}}
		}, []assistantchange.Field{{Name: "vault_identities", Old: []string{}, New: []string{"prod: vault"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture()
			w := &recordingWatcher{}
			f.svc.SetChangeWatcher(w)
			cred := f.sshCred
			created, err := f.svc.Create(ctx, f.org, templates.CreateInput{
				Name: "deploy", PlaybookID: f.playbook, Limit: "web*", CredentialID: &cred,
				ExtraVars: map[string]any{"tag": "v1", "password": "s3cret"}, ExtraVarsSchema: secretSchema,
			})
			if err != nil {
				t.Fatal(err)
			}
			in := saveInput(created)
			tc.mutate(f, &in)
			in.Actor = assistant
			if _, err := f.svc.Update(ctx, f.org, created.ID, in); err != nil {
				t.Fatal(err)
			}
			if len(w.got) != 1 {
				t.Fatalf("changes = %+v, want one", w.got)
			}
			c := w.got[0]
			if c.Subject != assistantchange.SubjectTemplate || c.ID != created.ID || c.Name != "deploy" || c.UserID != user || c.TokenID == nil || *c.TokenID != token {
				t.Fatalf("change = %+v", c)
			}
			if tc.want == nil {
				if len(c.Fields) != 1 || c.Fields[0].Name != tc.name {
					t.Fatalf("fields = %+v, want one %q", c.Fields, tc.name)
				}
				return
			}
			if !reflect.DeepEqual(c.Fields, tc.want) {
				t.Fatalf("fields = %#v, want %#v", c.Fields, tc.want)
			}
			// The same change made by a person hands on nothing.
			f2 := newFixture()
			w2 := &recordingWatcher{}
			f2.svc.SetChangeWatcher(w2)
			created2, err := f2.svc.Create(ctx, f2.org, templates.CreateInput{
				Name: "deploy", PlaybookID: f2.playbook, Limit: "web*", CredentialID: &f2.sshCred,
				ExtraVars: map[string]any{"tag": "v1", "password": "s3cret"}, ExtraVarsSchema: secretSchema,
			})
			if err != nil {
				t.Fatal(err)
			}
			in2 := saveInput(created2)
			tc.mutate(f2, &in2)
			in2.Actor = assistantchange.Actor{UserID: user}
			if _, err := f2.svc.Update(ctx, f2.org, created2.ID, in2); err != nil {
				t.Fatal(err)
			}
			if len(w2.got) != 0 {
				t.Fatalf("a person's change was handed on: %+v", w2.got)
			}
		})
	}
}

// The other field classes of the template: the schema, agent enrollment
// and the follow-ups; a rename alone is nothing the run uses.
func TestAnAssistantsChangeOfSchemaEnrollmentAndFollowUps(t *testing.T) {
	f := newFixture()
	w := &recordingWatcher{}
	f.svc.SetChangeWatcher(w)
	created, err := f.svc.Create(ctx, f.org, f.in())
	if err != nil {
		t.Fatal(err)
	}
	next, err := f.svc.Create(ctx, f.org, templates.CreateInput{Name: "cleanup", PlaybookID: f.playbook})
	if err != nil {
		t.Fatal(err)
	}
	assistant := assistantchange.Actor{UserID: uuid.Must(uuid.NewV7()), Assistant: true}
	in := saveInput(created)
	in.Name, in.Actor = "renamed", assistant
	saved, err := f.svc.Update(ctx, f.org, created.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.got) != 0 {
		t.Fatalf("a rename was handed on: %+v", w.got)
	}
	in = saveInput(saved)
	enrol := true
	follow := []uuid.UUID{next.ID}
	in.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{{Name: "tag", Type: schema.TypeString}}}
	in.AgentEnrollment, in.MayEnrolAgents, in.FollowUps, in.Actor = &enrol, true, &follow, assistant
	if _, err := f.svc.Update(ctx, f.org, created.ID, in); err != nil {
		t.Fatal(err)
	}
	if len(w.got) != 1 {
		t.Fatalf("changes = %+v", w.got)
	}
	got := map[string]assistantchange.Field{}
	for _, fd := range w.got[0].Fields {
		got[fd.Name] = fd
	}
	if len(got) != 3 || got["agent_enrollment"].Old != false || got["agent_enrollment"].New != true ||
		!reflect.DeepEqual(got["follow_ups"].New, []string{"cleanup"}) || !reflect.DeepEqual(got["follow_ups"].Old, []string{}) ||
		got["extra_vars_schema"].Old != nil || got["extra_vars_schema"].New == nil {
		t.Fatalf("fields = %#v", got)
	}
}

// The watcher runs inside the update's transaction: its error fails the
// update.
func TestAWatcherErrorFailsTheUpdate(t *testing.T) {
	f := newFixture()
	boom := errors.New("boom")
	f.svc.SetChangeWatcher(&recordingWatcher{err: boom})
	created, err := f.svc.Create(ctx, f.org, f.in())
	if err != nil {
		t.Fatal(err)
	}
	in := saveInput(created)
	in.Limit, in.Actor = "db*", assistantchange.Actor{Assistant: true}
	if _, err := f.svc.Update(ctx, f.org, created.ID, in); !errors.Is(err, boom) {
		t.Fatalf("update = %v, want the watcher's error", err)
	}
}

// The variables routes are template writes too: an assistant's new
// values are handed on.
func TestAnAssistantsVariablesWriteIsHandedOn(t *testing.T) {
	f := newFixture()
	w := &recordingWatcher{}
	f.svc.SetChangeWatcher(w)
	created, err := f.svc.Create(ctx, f.org, templates.CreateInput{Name: "deploy", PlaybookID: f.playbook, ExtraVars: map[string]any{"tag": "v1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.UpdateVariables(ctx, f.org, created.ID, templates.VariablesInput{
		ExtraVars: map[string]any{"tag": "v2"}, Actor: assistantchange.Actor{Assistant: true},
	}); err != nil {
		t.Fatal(err)
	}
	if len(w.got) != 1 || !reflect.DeepEqual(w.got[0].Fields, []assistantchange.Field{{Name: "extra_vars.tag", Old: "v1", New: "v2"}}) {
		t.Fatalf("changes = %+v", w.got)
	}
}

// Fix round 1: the variables mode (manual or automatic sources) is a field
// the run uses: an assistant's switch is handed on, a person's is not.
func TestAnAssistantsVariablesModeSwitchIsHandedOn(t *testing.T) {
	f := newFixture()
	w := &recordingWatcher{}
	f.svc.SetChangeWatcher(w)
	tm := f.linked(t, "linked", templates.ModeManual, "c1", []schema.Field{{Name: "tag", Type: schema.TypeString}}, map[string]any{"tag": "v1"})
	if _, err := f.svc.SetVariablesModeBy(ctx, f.org, tm.ID, templates.ModeAutomatic, nil, assistantchange.Actor{}); err != nil {
		t.Fatal(err)
	}
	if len(w.got) != 0 {
		t.Fatalf("a person's switch was handed on: %+v", w.got)
	}
	if _, err := f.svc.SetVariablesModeBy(ctx, f.org, tm.ID, templates.ModeManual, nil, assistantchange.Actor{Assistant: true}); err != nil {
		t.Fatal(err)
	}
	if len(w.got) != 1 || !reflect.DeepEqual(w.got[0].Fields, []assistantchange.Field{{Name: "vars_mode", Old: "automatic", New: "manual"}}) {
		t.Fatalf("changes = %+v", w.got)
	}
}

// The templates that run a playbook: what an editor playbook's change
// reaches.
func TestIDsOfPlaybook(t *testing.T) {
	f := newFixture()
	a, err := f.svc.Create(ctx, f.org, templates.CreateInput{Name: "a", PlaybookID: f.playbook})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Create(ctx, f.org, templates.CreateInput{Name: "b", PlaybookID: f.playbook2}); err != nil {
		t.Fatal(err)
	}
	ids, err := f.svc.IDsOfPlaybook(ctx, f.org, f.playbook)
	if err != nil || len(ids) != 1 || ids[0] != a.ID {
		t.Fatalf("ids = %v, %v", ids, err)
	}
}
