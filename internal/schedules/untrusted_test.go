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
	in.Untrusted = true
	s, err := w.svc.CreateAs(ctx, w.org, id, w.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != id || s.CreatedBy == nil || *s.CreatedBy != w.actor || !s.UntrustedOverrides {
		t.Fatalf("created = %+v", s)
	}
	if _, err := w.svc.RunNow(ctx, w.org, id, w.actor); err != nil {
		t.Fatal(err)
	}
	req := w.runs.requests[len(w.runs.requests)-1]
	if req.Overrides == nil || !req.Overrides.Untrusted {
		t.Fatalf("run overrides = %+v, want untrusted", req.Overrides)
	}
}

// Without overrides there is nothing untrusted to carry, so the flag stays
// off and a person's later overrides are theirs.
func TestCreateAsWithoutOverridesIsNotUntrusted(t *testing.T) {
	w := newWorld(t)
	in := validInput(w.templateID)
	in.Untrusted = true
	s, err := w.svc.CreateAs(ctx, w.org, uuid.Must(uuid.NewV7()), w.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	if s.UntrustedOverrides {
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
			in.Overrides, in.Untrusted = ov, true
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
	in.Overrides, in.Untrusted = templates.Overrides{ExtraVars: map[string]any{"release": "1.2"}}, true
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
	if !up.UntrustedOverrides {
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
	if cleared.UntrustedOverrides {
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
	if s.UntrustedOverrides {
		t.Fatal("a person's schedule is untrusted")
	}
	if _, err := w.svc.RunNow(ctx, w.org, s.ID, w.actor); err != nil {
		t.Fatal(err)
	}
	if req := w.runs.requests[len(w.runs.requests)-1]; req.Overrides.Untrusted {
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
		UntrustedOverrides: true,
	}
	got, err := st.Create(scoped, in)
	if err != nil {
		t.Fatal(err)
	}
	if !got.UntrustedOverrides {
		t.Fatal("create dropped the untrusted mark")
	}
	got.UntrustedOverrides = false
	up, err := st.Update(scoped, got, got.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if up.UntrustedOverrides {
		t.Fatal("update kept a cleared untrusted mark")
	}
}
