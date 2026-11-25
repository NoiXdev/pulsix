package runrequests_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/cronexpr"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/templates"
)

// fakeSchedules is the schedule service as the resolver sees it: stored
// schedules, the inputs it validated and what it applied.
type fakeSchedules struct {
	byID        map[uuid.UUID]schedules.Schedule
	validateErr error
	applyErr    error
	validated   []schedules.Input
	created     []schedules.Input
	createdAs   []uuid.UUID // id, actor
	updated     []schedules.Input
	updatedRev  []int64
	resumed     []uuid.UUID
}

func (f *fakeSchedules) Get(_ context.Context, _, id uuid.UUID) (schedules.Schedule, error) {
	s, ok := f.byID[id]
	if !ok {
		return schedules.Schedule{}, schedules.ErrNotFound
	}
	return s, nil
}

func (f *fakeSchedules) Validate(_ context.Context, _, _ uuid.UUID, in schedules.Input) error {
	f.validated = append(f.validated, in)
	return f.validateErr
}

func (f *fakeSchedules) CreateAs(_ context.Context, _, id, actor uuid.UUID, in schedules.Input) (schedules.Schedule, error) {
	if f.applyErr != nil {
		return schedules.Schedule{}, f.applyErr
	}
	f.created, f.createdAs = append(f.created, in), append(f.createdAs, id, actor)
	return schedules.Schedule{ID: id}, nil
}

func (f *fakeSchedules) Update(_ context.Context, _, id uuid.UUID, revision int64, in schedules.Input) (schedules.Schedule, error) {
	if f.applyErr != nil {
		return schedules.Schedule{}, f.applyErr
	}
	f.updated, f.updatedRev = append(f.updated, in), append(f.updatedRev, revision)
	return schedules.Schedule{ID: id}, nil
}

func (f *fakeSchedules) Resume(_ context.Context, _, id uuid.UUID) (schedules.Schedule, error) {
	if f.applyErr != nil {
		return schedules.Schedule{}, f.applyErr
	}
	f.resumed = append(f.resumed, id)
	return schedules.Schedule{ID: id}, nil
}

// scheduleWorld is the resolver's world with template S, whose schema
// declares release (string), db_password (secret) and cfg (object), and a
// stored schedule of it, paused, with a secret override.
type scheduleWorld struct {
	*world
	s, sched uuid.UUID
	scheds   *fakeSchedules
}

func newScheduleWorld() *scheduleWorld {
	w := &scheduleWorld{world: newWorld(), s: id(), sched: id()}
	w.tmpls.fakeTemplates[w.s] = templates.Template{ID: w.s, Name: "S", Revision: 6, PlaybookID: w.pb,
		ExtraVarsSchema: &schema.Schema{Fields: []schema.Field{
			{Name: "release", Type: schema.TypeString}, {Name: "db_password", Type: schema.TypeSecret},
			{Name: "cfg", Type: schema.TypeObject, Fields: []schema.Field{{Name: "level", Type: schema.TypeString}}},
		}}}
	limit := "web*"
	w.scheds = &fakeSchedules{byID: map[uuid.UUID]schedules.Schedule{w.sched: {
		ID: w.sched, TemplateID: w.s, Name: "nightly", Mode: schedules.ModeSimple, Cron: "0 3 * * *",
		Simple: &cronexpr.Simple{Kind: "daily", Time: "03:00"}, Timezone: "Europe/Berlin",
		Guards:    schedules.Guards{Overlap: schedules.OverlapSkip},
		Overrides: templates.Overrides{Limit: &limit, ExtraVars: map[string]any{"release": "1.0", "db_password": schema.Sealed([]byte("ct"))}},
		Paused:    true, Revision: 5,
	}}}
	return w
}

func (w *scheduleWorld) resolver() *runrequests.Resolver {
	return runrequests.NewResolver(runrequests.Sources{
		Templates: w.tmpls, Playbooks: w.pbs, Repos: w.repos, Jobs: w.jobs, Hosts: w.hosts, SSH: w.ssh,
		Credentials: w.creds, Vars: w.inv, VarsKey: w.key, Settings: w.settings, FollowUpRuns: w.rows, Overrides: w.ovr,
		Commits: w.commits, SyncJobs: w.syncs, RepoSyncs: w.repoSyncs,
		InventorySecrets: w.invSecrets, PlaybookVars: w.pbVars, Agents: w.agents, Schedules: w.scheds,
	})
}

func (w *scheduleWorld) spec() *runrequests.ScheduleSpec {
	return &runrequests.ScheduleSpec{TemplateID: w.s, Name: "weekly", Mode: schedules.ModeCron, Cron: "0 4 * * 1", Timezone: "UTC"}
}

func TestValidateScheduleCreate(t *testing.T) {
	w := newScheduleWorld()
	spec := w.spec()
	spec.ExtraVars = map[string]any{"release": json.Number("2")}
	ch, tmpl, rev, err := w.resolver().ValidateSchedule(context.Background(), id(), id(), runrequests.ScheduleInput{Action: runrequests.ScheduleCreate, Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	if tmpl != w.s || rev != 6 || ch.Action != runrequests.ScheduleCreate || !ch.AssistantOverrides || ch.Spec.Name != "weekly" {
		t.Fatalf("change = %+v, template %v rev %d", ch, tmpl, rev)
	}
	in := w.scheds.validated[0]
	if !in.Untrusted || in.TemplateID != w.s || in.Overrides.ExtraVars["release"] != json.Number("2") {
		t.Fatalf("validated = %+v", in)
	}
	// Without overrides there is nothing of the assistant's to mark.
	ch, _, _, err = w.resolver().ValidateSchedule(context.Background(), id(), id(), runrequests.ScheduleInput{Action: runrequests.ScheduleCreate, Spec: w.spec()})
	if err != nil || ch.AssistantOverrides {
		t.Fatalf("plain create = %+v, %v", ch, err)
	}
}

// The assistant's extra vars and limit follow the run request's rules:
// never a secret, never a name Ansible or Pulsix acts on, never a Pulsix
// marker, only declared names, no secret shadowing, a plain host pattern.
func TestValidateScheduleRefusesWhatAnAssistantMayNotSet(t *testing.T) {
	file := "@/etc/passwd"
	cases := map[string]func(w *scheduleWorld, s *runrequests.ScheduleSpec){
		"a secret": func(_ *scheduleWorld, s *runrequests.ScheduleSpec) { s.ExtraVars = map[string]any{"db_password": "x"} },
		"a keep on create": func(_ *scheduleWorld, s *runrequests.ScheduleSpec) {
			s.ExtraVars = map[string]any{"db_password": schema.Mask()}
		},
		"an ansible name": func(_ *scheduleWorld, s *runrequests.ScheduleSpec) {
			s.ExtraVars = map[string]any{"ansible_user": "root"}
		},
		"a keyword": func(_ *scheduleWorld, s *runrequests.ScheduleSpec) { s.ExtraVars = map[string]any{"hosts": "all"} },
		"a marker": func(_ *scheduleWorld, s *runrequests.ScheduleSpec) {
			s.ExtraVars = map[string]any{"release": schema.Mask()}
		},
		"an ansible key": func(_ *scheduleWorld, s *runrequests.ScheduleSpec) {
			s.ExtraVars = map[string]any{"cfg": map[string]any{"__ansible_unsafe": "x"}}
		},
		"a file limit": func(_ *scheduleWorld, s *runrequests.ScheduleSpec) { s.Limit = &file },
		"no schema": func(w *scheduleWorld, s *runrequests.ScheduleSpec) {
			s.TemplateID, s.ExtraVars = w.y, map[string]any{"release": "1"}
		},
		"a shadowed inventory secret": func(w *scheduleWorld, s *runrequests.ScheduleSpec) {
			w.invSecrets.names["release"] = true
			s.ExtraVars = map[string]any{"release": "1"}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			w := newScheduleWorld()
			spec := w.spec()
			mutate(w, spec)
			_, _, _, err := w.resolver().ValidateSchedule(context.Background(), id(), id(), runrequests.ScheduleInput{Action: runrequests.ScheduleCreate, Spec: spec})
			if !errors.Is(err, runrequests.ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
			if len(w.scheds.validated) != 0 {
				t.Fatal("a refused request reached the schedule's own checks")
			}
		})
	}
}

// The schedule's own rules (name, timetable, guards, the template's
// schema) are the schedule service's; its refusal is the request's.
func TestValidateScheduleMapsTheSchedulesRefusal(t *testing.T) {
	w := newScheduleWorld()
	w.scheds.validateErr = schedules.ErrNameTaken
	_, _, _, err := w.resolver().ValidateSchedule(context.Background(), id(), id(), runrequests.ScheduleInput{Action: runrequests.ScheduleCreate, Spec: w.spec()})
	if !errors.Is(err, runrequests.ErrInvalidInput) || !strings.Contains(err.Error(), "name") {
		t.Fatalf("err = %v", err)
	}
	w.scheds.validateErr = &schema.ValidationError{Problems: []schema.Problem{{Path: "release", Message: "must be a string"}}}
	_, _, _, err = w.resolver().ValidateSchedule(context.Background(), id(), id(), runrequests.ScheduleInput{Action: runrequests.ScheduleCreate, Spec: w.spec()})
	if !errors.Is(err, runrequests.ErrInvalidInput) || !strings.Contains(err.Error(), "release") {
		t.Fatalf("err = %v", err)
	}
	_, _, _, err = w.resolver().ValidateSchedule(context.Background(), id(), id(), runrequests.ScheduleInput{Action: runrequests.ScheduleCreate,
		Spec: &runrequests.ScheduleSpec{TemplateID: id(), Name: "x"}})
	if !errors.Is(err, runrequests.ErrNotFound) {
		t.Fatalf("unknown template: %v", err)
	}
}

// An update merges its patch into the schedule as it is: what it leaves
// out keeps the schedule's value, secrets as the masked marker the
// approval resolves against the stored value.
func TestValidateScheduleUpdateMergesThePatch(t *testing.T) {
	w := newScheduleWorld()
	cron := "*/30 * * * *"
	ch, tmpl, _, err := w.resolver().ValidateSchedule(context.Background(), id(), w.sched, runrequests.ScheduleInput{
		Action: runrequests.ScheduleUpdate, ScheduleID: w.sched, Patch: &runrequests.SchedulePatch{Cron: &cron},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := ch.Spec
	if tmpl != w.s || s.Name != "nightly" || s.Mode != schedules.ModeCron || s.Cron != cron || s.Simple != nil || s.Timezone != "Europe/Berlin" || *s.Limit != "web*" {
		t.Fatalf("merged = %+v", s)
	}
	if ch.AssistantOverrides {
		t.Fatal("an update that sets no override marked the overrides the assistant's")
	}
	if pw, ok := s.ExtraVars["db_password"].(map[string]any); !ok || pw["$secret"] != "set" || s.ExtraVars["release"] != "1.0" {
		t.Fatalf("extra vars = %#v", s.ExtraVars)
	}
	if w.scheds.validated[0].Untrusted {
		t.Fatal("validated as untrusted")
	}
}

// An update that sets extra vars sets the complete new set; it keeps a
// stored secret with {"$secret":"keep"} (stored as the masked marker) and
// marks the overrides the assistant's.
func TestValidateScheduleUpdateWithExtraVars(t *testing.T) {
	w := newScheduleWorld()
	ch, _, _, err := w.resolver().ValidateSchedule(context.Background(), id(), w.sched, runrequests.ScheduleInput{
		Action: runrequests.ScheduleUpdate, ScheduleID: w.sched,
		Patch: &runrequests.SchedulePatch{ExtraVars: map[string]any{"release": "2.0", "db_password": map[string]any{"$secret": "keep"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ch.AssistantOverrides || ch.Spec.ExtraVars["release"] != "2.0" {
		t.Fatalf("change = %+v", ch)
	}
	if pw := ch.Spec.ExtraVars["db_password"].(map[string]any); pw["$secret"] != "set" || len(pw) != 1 {
		t.Fatalf("kept secret = %#v", pw)
	}
	if !w.scheds.validated[0].Untrusted {
		t.Fatal("the assistant's overrides were validated as trusted")
	}
	// A new value for the secret is refused, as is a keep at another path.
	for _, v := range []any{"hunter2", map[string]any{"$secret": "keep", "from": "release"}} {
		_, _, _, err := w.resolver().ValidateSchedule(context.Background(), id(), w.sched, runrequests.ScheduleInput{
			Action: runrequests.ScheduleUpdate, ScheduleID: w.sched,
			Patch: &runrequests.SchedulePatch{ExtraVars: map[string]any{"db_password": v}},
		})
		if !errors.Is(err, runrequests.ErrInvalidInput) {
			t.Fatalf("%v: err = %v", v, err)
		}
	}
}

func TestValidateScheduleResume(t *testing.T) {
	w := newScheduleWorld()
	ch, tmpl, _, err := w.resolver().ValidateSchedule(context.Background(), id(), w.sched, runrequests.ScheduleInput{Action: runrequests.ScheduleResume, ScheduleID: w.sched})
	if err != nil || ch.Action != runrequests.ScheduleResume || ch.Spec != nil || tmpl != w.s {
		t.Fatalf("resume = %+v, %v, %v", ch, tmpl, err)
	}
	s := w.scheds.byID[w.sched]
	s.Paused = false
	w.scheds.byID[w.sched] = s
	if _, _, _, err := w.resolver().ValidateSchedule(context.Background(), id(), w.sched, runrequests.ScheduleInput{Action: runrequests.ScheduleResume, ScheduleID: w.sched}); !errors.Is(err, runrequests.ErrInvalidInput) {
		t.Fatalf("resume of an active schedule: %v", err)
	}
	if _, _, _, err := w.resolver().ValidateSchedule(context.Background(), id(), id(), runrequests.ScheduleInput{Action: runrequests.ScheduleResume, ScheduleID: id()}); !errors.Is(err, runrequests.ErrNotFound) {
		t.Fatalf("resume of an unknown schedule: %v", err)
	}
}

func (w *scheduleWorld) request(ch runrequests.ScheduleChange, target uuid.UUID) runrequests.Request {
	tmpl := w.s
	if ch.Spec != nil {
		tmpl = ch.Spec.TemplateID
	}
	return runrequests.Request{ID: id(), Kind: runrequests.KindSchedule, TemplateID: tmpl, Schedule: &ch, TargetID: &target}
}

// An update's picture shows the schedule as it will be next to the one
// as it is, secrets masked; the schedule's revision is in it, so any
// change to the schedule changes the fingerprint.
func TestPictureOfAScheduleUpdate(t *testing.T) {
	w := newScheduleWorld()
	spec := w.spec()
	spec.ExtraVars = map[string]any{"release": "2", "db_password": schema.Mask()}
	req := w.request(runrequests.ScheduleChange{Action: runrequests.ScheduleUpdate, Spec: spec, AssistantOverrides: true}, w.sched)
	p, err := w.resolver().Picture(context.Background(), id(), req)
	if err != nil {
		t.Fatal(err)
	}
	ps := p.Schedule
	if p.TemplateName != "S" || p.TemplateRevision != 6 || ps == nil || ps.Action != runrequests.ScheduleUpdate || ps.ScheduleID != w.sched.String() || !ps.AssistantOverrides {
		t.Fatalf("picture = %+v", p)
	}
	if ps.Current == nil || ps.Current.Revision != 5 || !ps.Current.Paused || ps.Current.Description == "" || ps.Current.TemplateName != "S" {
		t.Fatalf("current = %+v", ps.Current)
	}
	if pw := ps.Current.ExtraVars["db_password"].(map[string]any); pw["$secret"] != "set" {
		t.Fatalf("current secret = %#v", pw)
	}
	if ps.New == nil || ps.New.Name != "weekly" || !ps.New.Paused || ps.New.Cron != "0 4 * * 1" || ps.New.Revision != 0 {
		t.Fatalf("new = %+v", ps.New)
	}
	if len(p.Targets) != 0 || p.Repo != nil || p.Commit != nil {
		t.Fatalf("a schedule picture carries run fields: %+v", p)
	}
	before := runrequests.Fingerprint(req.ID, p)
	s := w.scheds.byID[w.sched]
	s.Revision = 6
	w.scheds.byID[w.sched] = s
	p2, err := w.resolver().Picture(context.Background(), id(), req)
	if err != nil {
		t.Fatal(err)
	}
	if runrequests.Fingerprint(req.ID, p2) == before {
		t.Fatal("a changed schedule kept the fingerprint")
	}
}

func TestPictureOfAScheduleCreateAndAGoneSchedule(t *testing.T) {
	w := newScheduleWorld()
	target := id()
	p, err := w.resolver().Picture(context.Background(), id(), w.request(runrequests.ScheduleChange{Action: runrequests.ScheduleCreate, Spec: w.spec()}, target))
	if err != nil {
		t.Fatal(err)
	}
	if p.Schedule.Current != nil || p.Schedule.New == nil || p.Schedule.New.Paused || p.Schedule.ScheduleID != target.String() || p.Schedule.Gone {
		t.Fatalf("create picture = %+v", p.Schedule)
	}
	p, err = w.resolver().Picture(context.Background(), id(), w.request(runrequests.ScheduleChange{Action: runrequests.ScheduleResume}, id()))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Schedule.Gone || p.Schedule.Current != nil {
		t.Fatalf("gone picture = %+v", p.Schedule)
	}
}

// The approval applies the request as pictured: a create under the
// request's target id as the approver, an update at the revision the
// person saw, a resume; the overrides are untrusted when they are the
// assistant's.
func TestApplySchedule(t *testing.T) {
	w := newScheduleWorld()
	actor := id()
	target := id()
	spec := w.spec()
	spec.ExtraVars = map[string]any{"release": "2"}
	req := w.request(runrequests.ScheduleChange{Action: runrequests.ScheduleCreate, Spec: spec, AssistantOverrides: true}, target)
	if err := w.resolver().ApplySchedule(context.Background(), id(), req, runrequests.PictureSchedule{Action: runrequests.ScheduleCreate}, actor); err != nil {
		t.Fatal(err)
	}
	if w.scheds.createdAs[0] != target || w.scheds.createdAs[1] != actor || !w.scheds.created[0].Untrusted || w.scheds.created[0].Name != "weekly" {
		t.Fatalf("created = %+v %v", w.scheds.created, w.scheds.createdAs)
	}

	upd := w.request(runrequests.ScheduleChange{Action: runrequests.ScheduleUpdate, Spec: w.spec()}, w.sched)
	pic := runrequests.PictureSchedule{Action: runrequests.ScheduleUpdate, Current: &runrequests.ScheduleState{Revision: 5}}
	if err := w.resolver().ApplySchedule(context.Background(), id(), upd, pic, actor); err != nil {
		t.Fatal(err)
	}
	if w.scheds.updatedRev[0] != 5 || w.scheds.updated[0].Untrusted {
		t.Fatalf("updated = %+v at %v", w.scheds.updated, w.scheds.updatedRev)
	}

	res := w.request(runrequests.ScheduleChange{Action: runrequests.ScheduleResume}, w.sched)
	if err := w.resolver().ApplySchedule(context.Background(), id(), res, runrequests.PictureSchedule{Action: runrequests.ScheduleResume}, actor); err != nil {
		t.Fatal(err)
	}
	if len(w.scheds.resumed) != 1 || w.scheds.resumed[0] != w.sched {
		t.Fatalf("resumed = %v", w.scheds.resumed)
	}
}

// A schedule changed between the picture and the write is "changed,
// review again", not a silent overwrite; what the assistant asked for is
// checked again at the approval.
func TestApplyScheduleRefusals(t *testing.T) {
	w := newScheduleWorld()
	w.scheds.applyErr = schedules.ErrRevisionMismatch
	upd := w.request(runrequests.ScheduleChange{Action: runrequests.ScheduleUpdate, Spec: w.spec()}, w.sched)
	pic := runrequests.PictureSchedule{Action: runrequests.ScheduleUpdate, Current: &runrequests.ScheduleState{Revision: 5}}
	if err := w.resolver().ApplySchedule(context.Background(), id(), upd, pic, id()); !errors.Is(err, runrequests.ErrChanged) {
		t.Fatalf("stale revision: %v", err)
	}
	w.scheds.applyErr = nil
	w.invSecrets.names["release"] = true
	spec := w.spec()
	spec.ExtraVars = map[string]any{"release": "2"}
	req := w.request(runrequests.ScheduleChange{Action: runrequests.ScheduleCreate, Spec: spec, AssistantOverrides: true}, id())
	if err := w.resolver().ApplySchedule(context.Background(), id(), req, runrequests.PictureSchedule{Action: runrequests.ScheduleCreate}, id()); !errors.Is(err, runrequests.ErrInvalidInput) {
		t.Fatalf("a shadow that appeared after the request: %v", err)
	}
	if len(w.scheds.created) != 0 {
		t.Fatal("created despite the refusal")
	}
}

// An update's patch keeps what it leaves out and switches the mode with
// the timetable it gives.
func TestValidateScheduleUpdatePatches(t *testing.T) {
	cron, empty, tz, name := "*/5 * * * *", "", "UTC", "renamed"
	modeCron, modeSimple := schedules.ModeCron, schedules.ModeSimple
	weekly := &cronexpr.Simple{Kind: "weekly", Days: []string{"mon"}, Time: "04:00"}
	guards := schedules.Guards{Overlap: schedules.OverlapQueue}
	cases := []struct {
		name  string
		patch runrequests.SchedulePatch
		check func(t *testing.T, w *scheduleWorld, s *runrequests.ScheduleSpec)
	}{
		{"nothing", runrequests.SchedulePatch{}, func(t *testing.T, _ *scheduleWorld, s *runrequests.ScheduleSpec) {
			if s.Mode != modeSimple || s.Cron != "" || s.Simple == nil || s.Simple.Kind != "daily" || s.Name != "nightly" {
				t.Fatalf("spec = %+v", s)
			}
		}},
		{"cron mode keeps the compiled timetable", runrequests.SchedulePatch{Mode: &modeCron}, func(t *testing.T, _ *scheduleWorld, s *runrequests.ScheduleSpec) {
			if s.Mode != modeCron || s.Cron != "0 3 * * *" || s.Simple != nil {
				t.Fatalf("spec = %+v", s)
			}
		}},
		{"a cron switches to cron mode", runrequests.SchedulePatch{Cron: &cron}, func(t *testing.T, _ *scheduleWorld, s *runrequests.ScheduleSpec) {
			if s.Mode != modeCron || s.Cron != cron || s.Simple != nil {
				t.Fatalf("spec = %+v", s)
			}
		}},
		{"a simple shape switches to simple mode", runrequests.SchedulePatch{Simple: weekly}, func(t *testing.T, _ *scheduleWorld, s *runrequests.ScheduleSpec) {
			if s.Mode != modeSimple || s.Cron != "" || s.Simple.Kind != "weekly" {
				t.Fatalf("spec = %+v", s)
			}
		}},
		{"name, zone, guards, template", runrequests.SchedulePatch{Name: &name, Timezone: &tz, Guards: &guards}, func(t *testing.T, _ *scheduleWorld, s *runrequests.ScheduleSpec) {
			if s.Name != name || s.Timezone != tz || s.Guards.Overlap != schedules.OverlapQueue {
				t.Fatalf("spec = %+v", s)
			}
		}},
		{"an empty limit clears it", runrequests.SchedulePatch{Limit: &empty}, func(t *testing.T, _ *scheduleWorld, s *runrequests.ScheduleSpec) {
			if s.Limit != nil {
				t.Fatalf("limit = %q", *s.Limit)
			}
		}},
		{"the template stays", runrequests.SchedulePatch{}, func(t *testing.T, w *scheduleWorld, s *runrequests.ScheduleSpec) {
			if s.TemplateID != w.s {
				t.Fatalf("template = %v", s.TemplateID)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newScheduleWorld()
			patch := tc.patch
			ch, _, _, err := w.resolver().ValidateSchedule(context.Background(), id(), w.sched, runrequests.ScheduleInput{Action: runrequests.ScheduleUpdate, ScheduleID: w.sched, Patch: &patch})
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, w, ch.Spec)
		})
	}
	// A template the update moves to is the request's template.
	w := newScheduleWorld()
	_, tmpl, rev, err := w.resolver().ValidateSchedule(context.Background(), id(), w.sched, runrequests.ScheduleInput{
		Action: runrequests.ScheduleUpdate, ScheduleID: w.sched, Patch: &runrequests.SchedulePatch{TemplateID: &w.y},
	})
	if err != nil || tmpl != w.y || rev != 5 {
		t.Fatalf("moved to %v rev %d, %v", tmpl, rev, err)
	}
}

// A kept secret nested in a value shows masked in the picture, never as
// the keep marker.
func TestPictureMasksKeptSecrets(t *testing.T) {
	w := newScheduleWorld()
	spec := w.spec()
	spec.ExtraVars = map[string]any{"list": []any{map[string]any{"$secret": "keep", "from": "x"}}}
	p, err := w.resolver().Picture(context.Background(), id(), w.request(runrequests.ScheduleChange{Action: runrequests.ScheduleCreate, Spec: spec}, id()))
	if err != nil {
		t.Fatal(err)
	}
	item := p.Schedule.New.ExtraVars["list"].([]any)[0].(map[string]any)
	if item["$secret"] != "set" || len(item) != 1 {
		t.Fatalf("shown = %#v", item)
	}
}
