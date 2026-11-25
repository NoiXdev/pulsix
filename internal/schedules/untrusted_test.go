package schedules_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/templates"
)

// An approved schedule request (spec 2026-11-25 C) creates the schedule
// under the id the request took, as the approver, with the assistant's
// overrides marked untrusted; its runs carry them untrusted.
func TestCreateAsKeepsTheIDAndMarksTheOverridesUntrusted(t *testing.T) {
	w := newWorld(t)
	id := uuid.Must(uuid.NewV7())
	in := validInput(w.templateID)
	in.Overrides = templates.Overrides{ExtraVars: map[string]any{"release": "1.2"}}
	in.UntrustedLimit, in.UntrustedExtraVars = true, true
	s, err := w.svc.CreateAs(ctx, w.org, id, w.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != id || s.CreatedBy == nil || *s.CreatedBy != w.actor || !s.UntrustedExtraVars || s.UntrustedLimit {
		t.Fatalf("created = %+v", s)
	}
	if _, err := w.svc.RunNow(ctx, w.org, id, w.actor); err != nil {
		t.Fatal(err)
	}
	req := w.runs.requests[len(w.runs.requests)-1]
	if req.Overrides == nil || !req.Overrides.UntrustedVars || req.Overrides.UntrustedLimit || req.Overrides.Untrusted {
		t.Fatalf("run overrides = %+v, want untrusted extra vars only", req.Overrides)
	}
}

// Without overrides there is nothing untrusted to carry, so the flag stays
// off and a person's later overrides are theirs.
func TestCreateAsWithoutOverridesIsNotUntrusted(t *testing.T) {
	w := newWorld(t)
	in := validInput(w.templateID)
	in.UntrustedLimit, in.UntrustedExtraVars = true, true
	s, err := w.svc.CreateAs(ctx, w.org, uuid.Must(uuid.NewV7()), w.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	if s.UntrustedLimit || s.UntrustedExtraVars {
		t.Fatal("a schedule without overrides is marked untrusted")
	}
}

// Untrusted overrides are held to the claim's rules at the write already:
// a name Ansible acts on, an "__ansible" key or a file limit is refused.
func TestUntrustedOverridesFollowTheClaimRules(t *testing.T) {
	limitFile := "@/etc/hosts"
	cases := map[string]templates.Overrides{
		"ansible name":   {ExtraVars: map[string]any{"ansible_become": true}},
		"ansible key":    {ExtraVars: map[string]any{"x": map[string]any{"__ansible_unsafe": "y"}}},
		"keyword name":   {ExtraVars: map[string]any{"hosts": "all"}},
		"file limit":     {Limit: &limitFile},
		"pulsix name":    {ExtraVars: map[string]any{"pulsix_agent_enroll_token": "x"}},
		"magic var name": {ExtraVars: map[string]any{"hostvars": "x"}},
	}
	for name, ov := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			in := validInput(w.templateID)
			in.Overrides, in.UntrustedLimit, in.UntrustedExtraVars = ov, true, true
			if _, err := w.svc.CreateAs(ctx, w.org, uuid.Must(uuid.NewV7()), w.actor, in); !errors.Is(err, schedules.ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}

// A person's update keeps the assistant's mark while overrides remain (it
// cannot tell its own values from the assistant's), holding them to the
// claim's rules; clearing the overrides drops the mark.
func TestAPersonsUpdateKeepsTheUntrustedMarkWhileOverridesRemain(t *testing.T) {
	w := newWorld(t)
	in := validInput(w.templateID)
	in.Overrides, in.UntrustedExtraVars = templates.Overrides{ExtraVars: map[string]any{"release": "1.2"}}, true
	s, err := w.svc.CreateAs(ctx, w.org, uuid.Must(uuid.NewV7()), w.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	person := validInput(w.templateID)
	person.Overrides = templates.Overrides{ExtraVars: map[string]any{"release": "1.3"}}
	up, err := w.svc.Update(ctx, w.org, s.ID, s.Revision, person)
	if err != nil {
		t.Fatal(err)
	}
	if !up.UntrustedExtraVars {
		t.Fatal("a person's update with overrides dropped the untrusted mark")
	}
	person.Overrides = templates.Overrides{ExtraVars: map[string]any{"ansible_user": "root"}}
	if _, err := w.svc.Update(ctx, w.org, s.ID, up.Revision, person); !errors.Is(err, schedules.ErrInvalidInput) {
		t.Fatalf("an ansible name on an untrusted schedule: %v", err)
	}
	person.Overrides = templates.Overrides{}
	cleared, err := w.svc.Update(ctx, w.org, s.ID, up.Revision, person)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.UntrustedExtraVars {
		t.Fatal("clearing the overrides kept the untrusted mark")
	}
}

// A person's schedule stays trusted: its runs carry operator overrides.
func TestAPersonsScheduleIsTrusted(t *testing.T) {
	w := newWorld(t)
	in := validInput(w.templateID)
	in.Overrides = templates.Overrides{ExtraVars: map[string]any{"ansible_user": "deploy"}}
	s, err := w.svc.Create(ctx, w.org, w.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	if s.UntrustedLimit || s.UntrustedExtraVars {
		t.Fatal("a person's schedule is untrusted")
	}
	if _, err := w.svc.RunNow(ctx, w.org, s.ID, w.actor); err != nil {
		t.Fatal(err)
	}
	if req := w.runs.requests[len(w.runs.requests)-1]; req.Overrides.UntrustedVars || req.Overrides.UntrustedLimit {
		t.Fatal("a person's schedule ran untrusted")
	}
}

// Validate checks a schedule as Create or Update would store it, without
// storing anything, and refuses a name another schedule holds.
func TestValidateChecksWithoutStoring(t *testing.T) {
	w := newWorld(t)
	taken, err := w.svc.Create(ctx, w.org, w.actor, validInput(w.templateID))
	if err != nil {
		t.Fatal(err)
	}
	in := validInput(w.templateID)
	in.Name = "Other"
	if err := w.svc.Validate(ctx, w.org, uuid.Must(uuid.NewV7()), in); err != nil {
		t.Fatalf("valid: %v", err)
	}
	if list, _, _ := w.svc.List(ctx, w.org, nil); len(list) != 1 {
		t.Fatalf("Validate stored a schedule: %d", len(list))
	}
	in.Name = taken.Name
	if err := w.svc.Validate(ctx, w.org, uuid.Must(uuid.NewV7()), in); !errors.Is(err, schedules.ErrNameTaken) {
		t.Fatalf("taken name: %v", err)
	}
	if err := w.svc.Validate(ctx, w.org, taken.ID, in); err != nil {
		t.Fatalf("its own name: %v", err)
	}
	in.Cron = "61 * * * *"
	if err := w.svc.Validate(ctx, w.org, taken.ID, in); !errors.Is(err, schedules.ErrInvalidInput) {
		t.Fatalf("bad cron: %v", err)
	}
}

func TestStoreKeepsTheUntrustedMark(t *testing.T) {
	d := testdb.Open(t)
	org, templateID := seedTemplate(t, d, "sched-untrusted")
	st := schedules.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	in := schedules.Schedule{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, TemplateID: templateID, Name: "Assistant's", Mode: schedules.ModeCron,
		Cron: "0 3 * * *", Timezone: "UTC", Overrides: templates.Overrides{ExtraVars: map[string]any{"a": "b"}},
		UntrustedExtraVars: true,
	}
	got, err := st.Create(scoped, in)
	if err != nil {
		t.Fatal(err)
	}
	if !got.UntrustedExtraVars || got.UntrustedLimit {
		t.Fatal("create mixed up the untrusted marks")
	}
	got.UntrustedExtraVars, got.UntrustedLimit = false, true
	up, err := st.Update(scoped, got, got.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if up.UntrustedExtraVars || !up.UntrustedLimit {
		t.Fatal("update mixed up the untrusted marks")
	}
	byName, err := st.GetByName(scoped, org, "Assistant's")
	if err != nil || byName.ID != in.ID {
		t.Fatalf("GetByName = %+v %v", byName, err)
	}
	if _, err := st.GetByName(scoped, org, "nobody"); !errors.Is(err, schedules.ErrNotFound) {
		t.Fatalf("GetByName of an unknown name = %v", err)
	}
	if _, err := st.SetPausedAt(scoped, org, in.ID, true, nil, up.Revision+1); !errors.Is(err, schedules.ErrRevisionMismatch) {
		t.Fatalf("SetPausedAt at a stale revision = %v", err)
	}
	paused, err := st.SetPausedAt(scoped, org, in.ID, true, nil, up.Revision)
	if err != nil || !paused.Paused {
		t.Fatalf("SetPausedAt = %+v %v", paused, err)
	}
}

// Each part carries its own mark: an assistant's limit leaves a person's
// extra vars trusted, and a person's file limit stays allowed next to an
// assistant's extra vars.
func TestUntrustedPartsAreMarkedApart(t *testing.T) {
	w := newWorld(t)
	limit, file := "web*", "@hosts.txt"
	in := validInput(w.templateID)
	in.Overrides = templates.Overrides{Limit: &limit, ExtraVars: map[string]any{"ansible_user": "deploy"}}
	in.UntrustedLimit = true
	s, err := w.svc.CreateAs(ctx, w.org, uuid.Must(uuid.NewV7()), w.actor, in)
	if err != nil {
		t.Fatalf("an assistant's limit next to a person's ansible_user: %v", err)
	}
	if !s.UntrustedLimit || s.UntrustedExtraVars {
		t.Fatalf("marks = limit %v vars %v", s.UntrustedLimit, s.UntrustedExtraVars)
	}
	in = validInput(w.templateID)
	in.Name = "other"
	in.Overrides = templates.Overrides{Limit: &file, ExtraVars: map[string]any{"release": "1"}}
	in.UntrustedExtraVars = true
	s, err = w.svc.CreateAs(ctx, w.org, uuid.Must(uuid.NewV7()), w.actor, in)
	if err != nil {
		t.Fatalf("a person's file limit next to an assistant's extra vars: %v", err)
	}
	if s.UntrustedLimit || !s.UntrustedExtraVars {
		t.Fatalf("marks = limit %v vars %v", s.UntrustedLimit, s.UntrustedExtraVars)
	}
	if _, err := w.svc.RunNow(ctx, w.org, s.ID, w.actor); err != nil {
		t.Fatal(err)
	}
	ov := w.runs.requests[len(w.runs.requests)-1].Overrides
	if !ov.UntrustedVars || ov.UntrustedLimit || ov.Untrusted {
		t.Fatalf("run overrides = %+v", ov)
	}
}

// An approved resume resumes the schedule at the revision the person saw;
// a schedule changed since is refused.
func TestResumeAtTheRevisionSeen(t *testing.T) {
	w := newWorld(t)
	s, err := w.svc.Create(ctx, w.org, w.actor, validInput(w.templateID))
	if err != nil {
		t.Fatal(err)
	}
	paused, err := w.svc.Pause(ctx, w.org, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.ResumeAt(ctx, w.org, s.ID, paused.Revision-1); !errors.Is(err, schedules.ErrRevisionMismatch) {
		t.Fatalf("stale revision: %v", err)
	}
	got, err := w.svc.ResumeAt(ctx, w.org, s.ID, paused.Revision)
	if err != nil || got.Paused || got.NextRunAt == nil {
		t.Fatalf("resumed = %+v %v", got, err)
	}
}
