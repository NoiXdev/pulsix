// The automatic apply (spec 7.75 goal 5): after a repo sync, or the save
// of an editor playbook, every automatic template of the playbook takes
// additions and safe changes, marks gone fields, and holds the rest for a
// person. Stored values are never touched.
package templates_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/varsource"
)

// repoFixture is a fixture whose two playbooks belong to one repository.
func repoFixture() (fixture, uuid.UUID) {
	f := newFixture()
	repo := uuid.Must(uuid.NewV7())
	f.store.playbookRepo[f.playbook] = repo
	f.store.playbookRepo[f.playbook2] = repo
	return f, repo
}

func storedVars(t *testing.T, f fixture, id uuid.UUID) string {
	t.Helper()
	b, err := json.Marshal(f.store.byID[id].ExtraVars)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestAutomaticApplyAfterSync: additions and a safe change are applied, a
// type change is held, a gone field is marked, and the stored values stay
// byte for byte; the review counts what is left for a person.
func TestAutomaticApplyAfterSync(t *testing.T) {
	f, repo := repoFixture()
	port := candidate("port", schema.TypeInt, withDefault(float64(80)))
	mode := candidate("mode", schema.TypeString)
	old := candidate("old", schema.TypeString)
	tm := f.linked(t, "auto", templates.ModeAutomatic, "c1",
		[]schema.Field{asStored(port, true), asStored(mode, true), asStored(old, true)},
		map[string]any{"port": float64(8080), "mode": "blue", "old": "o"})
	before := storedVars(t, f, tm.ID)
	f.sources[f.playbook] = readSource(repo, "c2",
		candidate("port", schema.TypeInt, withDefault(float64(81))),
		candidate("mode", schema.TypeInt),
		candidate("opt", schema.TypeString, withDefault("a")),
		candidate("req", schema.TypeString, required),
	)

	if err := f.svc.ApplyRepoSources(ctx, f.org, repo); err != nil {
		t.Fatal(err)
	}
	stored := f.store.byID[tm.ID]
	fields := stored.ExtraVarsSchema.Fields
	if !reflect.DeepEqual(fieldNames(fields), []string{"port", "mode", "old", "opt", "req"}) {
		t.Fatalf("fields = %v", fieldNames(fields))
	}
	if fields[0].Default != float64(81) || !fields[0].IsActive() {
		t.Errorf("port = %s, want the new default applied", mustJSON(t, fields[0]))
	}
	if fields[1].Type != schema.TypeString {
		t.Errorf("mode = %s, want the type change held", mustJSON(t, fields[1]))
	}
	if fields[2].Origin != schema.OriginGone || !fields[2].IsActive() {
		t.Errorf("old = %s, want marked gone, still active", mustJSON(t, fields[2]))
	}
	if fields[3].IsActive() || !fields[4].IsActive() || fields[3].Origin != schema.OriginSource {
		t.Errorf("added = %s, want opt inactive and req active", mustJSON(t, fields[3:]))
	}
	if stored.Revision != tm.Revision+1 || stored.VarsSourceRev != "c2" || stored.VarsAppliedAt == nil || stored.VarsMode != templates.ModeAutomatic {
		t.Errorf("stored revision %d rev %q at %v mode %q", stored.Revision, stored.VarsSourceRev, stored.VarsAppliedAt, stored.VarsMode)
	}
	if after := storedVars(t, f, tm.ID); after != before {
		t.Errorf("values %s, want %s untouched", after, before)
	}

	view, err := f.svc.Variables(ctx, f.org, tm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := (templates.Review{Gone: 1, NeedsReview: 1, MissingRequired: 1, NotSent: []string{}}); !reflect.DeepEqual(view.Review, want) {
		t.Errorf("review = %+v, want %+v", view.Review, want)
	}
	if view.Source.AppliedRev != "c2" || view.Source.Newer {
		t.Errorf("source = %+v", view.Source)
	}
}

// TestAutomaticApplyNeverSealsOrUnseals: a change of the secret flag is
// held either way, so a plaintext stays plaintext and a sealed value
// stays the same sealed bytes.
func TestAutomaticApplyNeverSealsOrUnseals(t *testing.T) {
	f, repo := repoFixture()
	pw := candidate("pw", schema.TypeString)
	token := candidate("token", schema.TypeSecret)
	tm := f.linked(t, "secrets", templates.ModeAutomatic, "c1", []schema.Field{asStored(pw, true), asStored(token, true)},
		map[string]any{"pw": "plain", "token": "hunter2"})
	sealed := storedSealed(t, f, tm.ID, "token")
	f.sources[f.playbook] = readSource(repo, "c2", candidate("pw", schema.TypeSecret), candidate("token", schema.TypeString))

	if err := f.svc.ApplyRepoSources(ctx, f.org, repo); err != nil {
		t.Fatal(err)
	}
	stored := f.store.byID[tm.ID]
	if fields := stored.ExtraVarsSchema.Fields; fields[0].Type != schema.TypeString || fields[1].Type != schema.TypeSecret {
		t.Fatalf("fields = %s, want both secret changes held", mustJSON(t, fields))
	}
	if stored.ExtraVars["pw"] != "plain" || !bytes.Equal(storedSealed(t, f, tm.ID, "token"), sealed) {
		t.Fatalf("vars = %v, want them untouched", stored.ExtraVars)
	}
	view, err := f.svc.Variables(ctx, f.org, tm.ID)
	if err != nil || view.Review.NeedsReview != 2 {
		t.Fatalf("review = %+v, %v", view.Review, err)
	}
}

// TestAutomaticApplySkipsManualTemplatesAndOldWorkers (Review Focus 3): a
// manual template waits for a person; an automatic one is left alone
// while its source is not usable (an older worker, a playbook left out
// or not read, no record).
func TestAutomaticApplySkipsManualTemplatesAndOldWorkers(t *testing.T) {
	f, repo := repoFixture()
	port := candidate("port", schema.TypeInt, withDefault(float64(80)))
	manual := f.linked(t, "manual", templates.ModeManual, "c1", []schema.Field{asStored(port, true)}, nil)
	auto := f.linkedTo(t, f.playbook2, "auto", templates.ModeAutomatic, "c1", []schema.Field{asStored(port, true)}, nil)
	changed := readSource(repo, "c2", candidate("port", schema.TypeInt, withDefault(float64(81))))
	f.sources[f.playbook] = changed

	unusable := map[string]func(*playbooks.VarSource){
		"older worker":    func(s *playbooks.VarSource) { s.WorkerReads = false },
		"left out":        func(s *playbooks.VarSource) { s.LeftOut, s.Rev = true, "" },
		"not read":        func(s *playbooks.VarSource) { s.Playbook.Failed = true },
		"no record found": func(s *playbooks.VarSource) { *s = playbooks.VarSource{Kind: playbooks.VarSourceRepo} },
	}
	for name, mod := range unusable {
		t.Run(name, func(t *testing.T) {
			src := changed
			mod(&src)
			f.sources[f.playbook2] = src
			if err := f.svc.ApplyRepoSources(ctx, f.org, repo); err != nil {
				t.Fatal(err)
			}
			if got := f.store.byID[auto.ID]; got.Revision != auto.Revision || got.VarsSourceRev != "c1" {
				t.Errorf("automatic template written: revision %d rev %q", got.Revision, got.VarsSourceRev)
			}
		})
	}
	if got := f.store.byID[manual.ID]; got.Revision != manual.Revision || got.ExtraVarsSchema.Fields[0].Default != float64(80) {
		t.Errorf("manual template written: %s", mustJSON(t, got.ExtraVarsSchema))
	}

	// Once the playbook is read again, the automatic template follows.
	f.sources[f.playbook2] = changed
	if err := f.svc.ApplyRepoSources(ctx, f.org, repo); err != nil {
		t.Fatal(err)
	}
	if got := f.store.byID[auto.ID]; got.ExtraVarsSchema.Fields[0].Default != float64(81) {
		t.Errorf("automatic template = %s, want the change applied", mustJSON(t, got.ExtraVarsSchema))
	}
}

// TestAutomaticApplyIsIdempotent: applying the same read again writes
// nothing.
func TestAutomaticApplyIsIdempotent(t *testing.T) {
	f, repo := repoFixture()
	tm := f.linked(t, "auto", templates.ModeAutomatic, "c1", []schema.Field{asStored(candidate("port", schema.TypeInt), true)}, nil)
	f.sources[f.playbook] = readSource(repo, "c2", candidate("port", schema.TypeInt), candidate("extra", schema.TypeString))
	for range 2 {
		if err := f.svc.ApplyRepoSources(ctx, f.org, repo); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.store.byID[tm.ID]; got.Revision != tm.Revision+1 || f.store.sourceWrites != 1 {
		t.Fatalf("revision %d after two applies (from %d), %d writes; want one write", got.Revision, tm.Revision, f.store.sourceWrites)
	}
	// A read that changes nothing but the revision records the revision.
	f.sources[f.playbook] = readSource(repo, "c3", candidate("port", schema.TypeInt), candidate("extra", schema.TypeString))
	if err := f.svc.ApplyRepoSources(ctx, f.org, repo); err != nil {
		t.Fatal(err)
	}
	if got := f.store.byID[tm.ID]; got.VarsSourceRev != "c3" {
		t.Fatalf("rev %q, want c3 recorded", got.VarsSourceRev)
	}
}

// TestAutomaticApplyLosesNoConcurrentSave (Review Focus 4): a person's
// save landing between the apply's read and write wins the race; the
// apply reads again and applies on top of it, and gives up after a second
// lost race, leaving the change to the next sync.
func TestAutomaticApplyLosesNoConcurrentSave(t *testing.T) {
	setup := func(t *testing.T) (fixture, uuid.UUID, templates.Template, func()) {
		f, repo := repoFixture()
		port := asStored(candidate("port", schema.TypeInt, withDefault(float64(80))), true)
		tm := f.linked(t, "auto", templates.ModeAutomatic, "c1", []schema.Field{port}, nil)
		f.sources[f.playbook] = readSource(repo, "c2", candidate("port", schema.TypeInt, withDefault(float64(81))))
		save := func() {
			cur := f.store.byID[tm.ID]
			fields := append(append([]schema.Field{}, cur.ExtraVarsSchema.Fields...), schema.Field{Name: "note", Type: schema.TypeString})
			if _, err := f.svc.UpdateVariables(ctx, f.org, tm.ID, templates.VariablesInput{
				Schema: &schema.Schema{Fields: fields}, ExtraVars: map[string]any{"note": "hi"}, IfRevision: &cur.Revision,
			}); err != nil {
				t.Errorf("the person's save: %v", err)
			}
		}
		return f, repo, tm, save
	}

	t.Run("one lost race", func(t *testing.T) {
		f, repo, tm, save := setup(t)
		f.store.beforeWrite = save
		if err := f.svc.ApplyRepoSources(ctx, f.org, repo); err != nil {
			t.Fatal(err)
		}
		got := f.store.byID[tm.ID]
		if !reflect.DeepEqual(fieldNames(got.ExtraVarsSchema.Fields), []string{"port", "note"}) ||
			got.ExtraVarsSchema.Fields[0].Default != float64(81) || got.ExtraVars["note"] != "hi" || got.Revision != tm.Revision+2 {
			t.Fatalf("stored = %s %v revision %d, want both writes", mustJSON(t, got.ExtraVarsSchema), got.ExtraVars, got.Revision)
		}
	})
	t.Run("two lost races", func(t *testing.T) {
		f, repo, tm, _ := setup(t)
		var logs bytes.Buffer
		f.svc.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
		races := 0
		var race func()
		race = func() {
			races++
			cur := f.store.byID[tm.ID]
			cur.Revision++ // another write, of anything
			f.store.byID[tm.ID] = cur
			if races < 2 {
				f.store.beforeWrite = race
			}
		}
		f.store.beforeWrite = race
		if err := f.svc.ApplyRepoSources(ctx, f.org, repo); err != nil {
			t.Fatal(err)
		}
		if got := f.store.byID[tm.ID]; got.ExtraVarsSchema.Fields[0].Default != float64(80) || f.store.sourceWrites != 0 {
			t.Fatalf("stored = %s, want the apply left to the next sync", mustJSON(t, got.ExtraVarsSchema))
		}
		if !strings.Contains(logs.String(), tm.ID.String()) || !strings.Contains(logs.String(), "revision mismatch") {
			t.Fatalf("logs = %q, want the template's lost race logged", logs.String())
		}
	})
}

// TestInlineSaveAppliesAutomaticTemplates: an editor playbook's save
// applies its new sources to its own automatic templates only.
func TestInlineSaveAppliesAutomaticTemplates(t *testing.T) {
	f := newFixture()
	port := asStored(candidate("port", schema.TypeInt, withDefault(float64(80))), true)
	mine := f.linked(t, "mine", templates.ModeAutomatic, "text:aaa", []schema.Field{port}, nil)
	other := f.linkedTo(t, f.playbook2, "other", templates.ModeAutomatic, "text:aaa", []schema.Field{port}, nil)
	inline := playbooks.VarSource{Kind: playbooks.VarSourceInline, Rev: "text:bbb", WorkerReads: true, Found: true,
		Playbook: varsource.Playbook{Path: sitePath, Fields: []schema.Field{candidate("port", schema.TypeInt, withDefault(float64(81)))},
			Files: []varsource.FileUse{{Path: sitePath, State: varsource.FileRead}}, Notes: []varsource.Note{}}}
	f.sources[f.playbook] = inline
	f.sources[f.playbook2] = inline

	if err := f.svc.ApplyPlaybookSources(ctx, f.org, f.playbook); err != nil {
		t.Fatal(err)
	}
	if got := f.store.byID[mine.ID]; got.ExtraVarsSchema.Fields[0].Default != float64(81) || got.VarsSourceRev != "text:bbb" {
		t.Errorf("the playbook's template = %s rev %q", mustJSON(t, got.ExtraVarsSchema), got.VarsSourceRev)
	}
	if got := f.store.byID[other.ID]; got.Revision != other.Revision {
		t.Errorf("another playbook's template was written")
	}
}

// TestAutomaticApplyRetryReadsTheSourceAgain: two applies race (two syncs
// finishing close together, or a save of an editor playbook during one).
// The one that loses re-reads the source as well as the template, so it
// never writes the older read over the newer one.
func TestAutomaticApplyRetryReadsTheSourceAgain(t *testing.T) {
	port := asStored(candidate("port", schema.TypeInt, withDefault(float64(80))), true)
	newer := []schema.Field{candidate("port", schema.TypeInt, withDefault(float64(82))), candidate("x", schema.TypeString)}
	older := []schema.Field{candidate("port", schema.TypeInt, withDefault(float64(81)))}
	inline := func(rev string, fields []schema.Field) playbooks.VarSource {
		return playbooks.VarSource{Kind: playbooks.VarSourceInline, Rev: rev, WorkerReads: true, Found: true,
			Playbook: varsource.Playbook{Path: sitePath, Fields: fields,
				Files: []varsource.FileUse{{Path: sitePath, State: varsource.FileRead}}, Notes: []varsource.Note{}}}
	}
	cases := []struct {
		name         string
		older, newer func(repo uuid.UUID) playbooks.VarSource
		apply        func(f fixture, repo uuid.UUID) error
		newerRev     string
	}{
		{"repo", func(repo uuid.UUID) playbooks.VarSource { return readSource(repo, "c2", older...) },
			func(repo uuid.UUID) playbooks.VarSource { return readSource(repo, "c3", newer...) },
			func(f fixture, repo uuid.UUID) error { return f.svc.ApplyRepoSources(ctx, f.org, repo) }, "c3"},
		{"inline", func(uuid.UUID) playbooks.VarSource { return inline("text:b", older) },
			func(uuid.UUID) playbooks.VarSource { return inline("text:c", newer) },
			func(f fixture, _ uuid.UUID) error { return f.svc.ApplyPlaybookSources(ctx, f.org, f.playbook) }, "text:c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, repo := repoFixture()
			tm := f.linked(t, "auto", templates.ModeAutomatic, "c1", []schema.Field{port}, nil)
			f.sources[f.playbook] = tc.older(repo)
			f.store.beforeWrite = func() {
				f.sources[f.playbook] = tc.newer(repo)
				if err := tc.apply(f, repo); err != nil {
					t.Error(err)
				}
			}
			if err := tc.apply(f, repo); err != nil {
				t.Fatal(err)
			}
			got := f.store.byID[tm.ID]
			if got.VarsSourceRev != tc.newerRev || got.ExtraVarsSchema.Fields[0].Default != float64(82) ||
				len(got.ExtraVarsSchema.Fields) != 2 || got.ExtraVarsSchema.Fields[1].Origin != schema.OriginSource {
				t.Fatalf("stored rev %q fields %s, want the newer read kept", got.VarsSourceRev, mustJSON(t, got.ExtraVarsSchema.Fields))
			}
		})
	}
}

// TestAutomaticApplyRecordsANewRevisionWithoutABump: a read that changes
// no field records its revision but leaves the template's revision, so a
// person editing the template meanwhile saves without a reload; a read
// that changes a field bumps it.
func TestAutomaticApplyRecordsANewRevisionWithoutABump(t *testing.T) {
	f, repo := repoFixture()
	port := candidate("port", schema.TypeInt)
	tm := f.linked(t, "auto", templates.ModeAutomatic, "c1", []schema.Field{asStored(port, true)}, map[string]any{"port": float64(1)})
	f.sources[f.playbook] = readSource(repo, "c2", port)
	if err := f.svc.ApplyRepoSources(ctx, f.org, repo); err != nil {
		t.Fatal(err)
	}
	got := f.store.byID[tm.ID]
	if got.Revision != tm.Revision || got.VarsSourceRev != "c2" || got.VarsAppliedAt == nil || f.store.revRecords != 1 || f.store.sourceWrites != 0 {
		t.Fatalf("revision %d (was %d) rev %q, %d records %d writes", got.Revision, tm.Revision, got.VarsSourceRev, f.store.revRecords, f.store.sourceWrites)
	}
	// The template dialog's save, based on the revision read before.
	if _, err := f.svc.Update(ctx, f.org, tm.ID, templates.UpdateInput{
		Name: tm.Name, PlaybookID: tm.PlaybookID, ExtraVarsSchema: tm.ExtraVarsSchema,
		ExtraVars: map[string]any{"port": float64(2)}, IfRevision: &tm.Revision,
	}); err != nil {
		t.Fatalf("save with the earlier revision: %v", err)
	}
	saved := f.store.byID[tm.ID]
	f.sources[f.playbook] = readSource(repo, "c3", candidate("port", schema.TypeInt, withDefault(float64(5))))
	if err := f.svc.ApplyRepoSources(ctx, f.org, repo); err != nil {
		t.Fatal(err)
	}
	if got := f.store.byID[tm.ID]; got.Revision != saved.Revision+1 || got.VarsSourceRev != "c3" {
		t.Fatalf("content change: revision %d (was %d) rev %q, want a bump", got.Revision, saved.Revision, got.VarsSourceRev)
	}
}

// TestAutomaticApplyHoldsAReaddOverAKeptSecret: a person removes secret
// field token, whose sealed value stays stored (spec 7.75 owner decision
// 4); the source later declares token again as a required plain string.
// The automatic apply does not add it over the kept sealed value — a run
// would otherwise receive the secret as a plain, unredacted value — but
// holds it for a person, whose apply asks for the value again.
func TestAutomaticApplyHoldsAReaddOverAKeptSecret(t *testing.T) {
	f, repo := repoFixture()
	token := candidate("token", schema.TypeSecret, required)
	other := candidate("other", schema.TypeString)
	tm := f.linked(t, "auto", templates.ModeAutomatic, "c1",
		[]schema.Field{asStored(token, true), asStored(other, true)},
		map[string]any{"token": "s3cret", "other": "x"})
	if _, err := f.svc.UpdateVariables(ctx, f.org, tm.ID, templates.VariablesInput{
		Schema: &schema.Schema{Fields: []schema.Field{asStored(other, true)}}, ExtraVars: map[string]any{"other": "x"},
	}); err != nil {
		t.Fatal(err)
	}
	sealed := storedSealed(t, f, tm.ID, "token")
	f.sources[f.playbook] = readSource(repo, "c2", other, candidate("token", schema.TypeString, required))

	if err := f.svc.ApplyRepoSources(ctx, f.org, repo); err != nil {
		t.Fatal(err)
	}
	stored := f.store.byID[tm.ID]
	if got := fieldNames(stored.ExtraVarsSchema.Fields); !reflect.DeepEqual(got, []string{"other"}) {
		t.Fatalf("fields = %v, want token held", got)
	}
	if sendable := schema.Sendable(stored.ExtraVarsSchema.Fields, stored.ExtraVars); sendable["token"] != nil {
		t.Fatalf("sendable = %v, want no token", sendable)
	}
	if !bytes.Equal(storedSealed(t, f, tm.ID, "token"), sealed) {
		t.Fatal("the kept sealed value changed")
	}
	view, err := f.svc.Variables(ctx, f.org, tm.ID)
	if err != nil || view.Review.NeedsReview != 1 {
		t.Fatalf("review = %+v, %v, want the held addition counted", view.Review, err)
	}

	in := templates.ApplyInput{Mode: templates.ModeAutomatic, SourceRev: "c2", SourceSyncedAt: &syncedAt}
	_, err = f.svc.ApplyVariables(ctx, f.org, tm.ID, in)
	if got := valueProblems(t, err); !reflect.DeepEqual(got, []string{"token"}) || !strings.Contains(err.Error(), "enter the value again") {
		t.Fatalf("problems %v (%v), want token to be entered again", got, err)
	}
	in.Values = map[string]any{"token": "visible"}
	if _, err := f.svc.ApplyVariables(ctx, f.org, tm.ID, in); err != nil {
		t.Fatal(err)
	}
	if got := f.store.byID[tm.ID].ExtraVars["token"]; got != "visible" {
		t.Fatalf("token = %#v", got)
	}
}
