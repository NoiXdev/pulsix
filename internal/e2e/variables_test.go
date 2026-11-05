// The variable sources gate (backlog 7.75): a repository playbook declares
// its variables (an inline marker on a play variable, a role's argument
// specs), the worker reads them during the repository sync, and a job
// template takes them over through the preview and the apply. In automatic
// mode the next sync after a pushed change updates the template on its
// own: new fields arrive inactive, changed ones take the new content, a
// field the source dropped stays as "gone" for a person to confirm. Real
// manager, real worker loop, real git server; runs succeed before and
// after the change.
package e2e

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestVariableSourcesEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("no docker for the testcontainer database: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := wireScenario(t, ctx, cancel, wiring{})
	s.driveVariableSources()
}

// The DTOs this scenario reads: narrow mirrors of the API's shapes.
type (
	varFieldOut struct {
		Name    string `json:"name"`
		Label   string `json:"label"`
		Default any    `json:"default"`
		Active  *bool  `json:"active"`
		Origin  string `json:"origin"`
	}
	repoPlaybooksOut struct {
		Sources *struct {
			Commit      string `json:"commit"`
			WorkerReads bool   `json:"worker_reads"`
		} `json:"sources"`
		Playbooks []struct {
			Path   string `json:"path"`
			Fields int    `json:"fields"`
			Failed bool   `json:"failed"`
		} `json:"playbooks"`
	}
	varSourceOut struct {
		Commit   string  `json:"commit"`
		SyncedAt *string `json:"synced_at"`
	}
	varPreviewOut struct {
		Preview struct {
			SourceRev string `json:"source_rev"`
			Added     []struct {
				Field varFieldOut `json:"field"`
			} `json:"added"`
		} `json:"preview"`
		Source varSourceOut `json:"source"`
	}
	templateVarsOut struct {
		ExtraVarsSchema *struct {
			Fields []map[string]any `json:"fields"`
		} `json:"extra_vars_schema"`
		ExtraVars map[string]any `json:"extra_vars"`
		Revision  int64          `json:"revision"`
		Mode      string         `json:"mode"`
		Source    *varSourceOut  `json:"source"`
		Review    *struct {
			Gone int `json:"gone"`
		} `json:"review"`
	}
)

// greeterSpecsChanged is the pushed change to the greeter role's argument
// specs: greeter_style is new (optional), greeter_count's default is now
// 2, greeter_color is gone.
const greeterSpecsChanged = `argument_specs:
  main:
    short_description: Greets someone
    options:
      greeter_count:
        type: int
        default: 2
        description: How often to greet
      greeter_style:
        type: str
        description: The greeting's style
`

func (s *scenario) driveVariableSources() {
	s.t.Helper()
	prefix := "/api/v1/" + s.apiPath()
	s.pinHostKey()

	// --- The sync wireScenario ran discovered the playbook ----------------
	firstCommit := s.assertDiscovered(3)

	var picked playbookOut
	s.post(prefix+"/repos/"+s.repoID+"/playbooks", map[string]any{"path": "vars_demo.yml"}, &picked)
	tid := s.createTemplate("vars-demo", picked.Playbook.ID, map[string]any{})
	varsPath := prefix + "/job-templates/" + tid + "/variables"

	// --- Preview: three added, all inactive: each has a default (the
	// play variable's value, the specs' defaults), and the default
	// activation turns on only required fields without one ----------------
	var preview varPreviewOut
	s.post(varsPath+"/preview", struct{}{}, &preview)
	added := map[string]varFieldOut{}
	for _, a := range preview.Preview.Added {
		added[a.Field.Name] = a.Field
	}
	if len(added) != 3 {
		s.t.Fatalf("preview added %+v, want greeting_name, greeter_color, greeter_count", preview.Preview.Added)
	}
	if f := added["greeting_name"]; f.Label != "Greeting name" || f.Default != "World" {
		s.t.Fatalf("added greeting_name = %+v, want label %q and default World from the marker", f, "Greeting name")
	}
	for _, name := range []string{"greeting_name", "greeter_color", "greeter_count"} {
		if f, ok := added[name]; !ok || isActive(f) || f.Origin != "source" {
			s.t.Fatalf("added %s = %+v (present %v), want inactive with origin source", name, f, ok)
		}
	}
	if preview.Preview.SourceRev != firstCommit {
		s.t.Fatalf("preview source_rev = %q, want the synced commit %q", preview.Preview.SourceRev, firstCommit)
	}

	// --- Apply in manual mode; switch greeting_name on with a value; run --
	var applied templateVarsOut
	s.post(varsPath+"/apply", map[string]any{
		"mode": "manual", "source_rev": preview.Preview.SourceRev, "source_synced_at": preview.Source.SyncedAt,
	}, &applied)
	if applied.Mode != "manual" || len(fieldsOf(applied)) != 3 {
		s.t.Fatalf("after the apply: mode %q, fields %+v; want manual with 3 fields", applied.Mode, fieldsOf(applied))
	}
	for _, f := range applied.ExtraVarsSchema.Fields {
		if f["name"] == "greeting_name" {
			f["active"] = true
		}
	}
	var withValue templateVarsOut
	s.put(varsPath, map[string]any{
		"extra_vars_schema": applied.ExtraVarsSchema,
		"extra_vars":        map[string]any{"greeting_name": "Pulsix"},
	}, &withValue)
	if got := withValue.ExtraVars["greeting_name"]; got != "Pulsix" {
		s.t.Fatalf("greeting_name value = %v, want Pulsix", got)
	}
	if f := s.fieldByNameOrFail(withValue, "greeting_name"); !isActive(f) || f.Origin != "source" {
		s.t.Fatalf("greeting_name after the save = %+v, want active and still from the source", f)
	}
	s.runTemplateSucceeds(tid, "before the change")

	// --- Automatic mode; a pushed change; the next sync applies it --------
	var auto templateVarsOut
	s.put(varsPath+"/mode", map[string]any{"mode": "automatic"}, &auto)
	if auto.Mode != "automatic" {
		s.t.Fatalf("mode after the switch = %q, want automatic", auto.Mode)
	}
	pushFixtureChange(s.t, s.git.bare, map[string]string{"roles/greeter/meta/argument_specs.yml": greeterSpecsChanged})
	var syncOut repoSyncOut
	s.post(prefix+"/repos/"+s.repoID+"/sync", struct{}{}, &syncOut)
	if job := s.awaitJob(syncOut.Job.ID, "sync_repo", 60*time.Second); job.Job.Status != "succeeded" {
		s.t.Fatalf("repo sync after the push = %q (error %q)", job.Job.Status, job.Job.Error)
	}
	secondCommit := s.assertDiscovered(3) // greeting_name, greeter_count, greeter_style
	if secondCommit == firstCommit {
		s.t.Fatalf("the sync after the push still reports commit %s", firstCommit)
	}

	// The automatic apply runs right after the sync's result is stored, so
	// the job can read succeeded a moment before the template changed.
	after := s.awaitTemplateVars(varsPath, 30*time.Second, func(v templateVarsOut) bool {
		return v.Revision > auto.Revision
	})
	if f := s.fieldByNameOrFail(after, "greeter_style"); isActive(f) || f.Origin != "source" {
		s.t.Fatalf("greeter_style after the sync = %+v, want added inactive with origin source", f)
	}
	if f := s.fieldByNameOrFail(after, "greeter_count"); f.Default != float64(2) || f.Origin != "source" {
		s.t.Fatalf("greeter_count after the sync = %+v, want default 2 with origin source", f)
	}
	if f := s.fieldByNameOrFail(after, "greeter_color"); f.Origin != "gone" {
		s.t.Fatalf("greeter_color after the sync = %+v, want kept with origin gone", f)
	}
	// A sync never changes the person's active switch, nor a stored value.
	if f := s.fieldByNameOrFail(after, "greeting_name"); !isActive(f) || after.ExtraVars["greeting_name"] != "Pulsix" {
		s.t.Fatalf("greeting_name after the sync = %+v value %v, want active with its value kept", f, after.ExtraVars["greeting_name"])
	}
	if after.Mode != "automatic" {
		s.t.Fatalf("mode after the automatic apply = %q, want automatic", after.Mode)
	}
	var read templateVarsOut
	s.get(varsPath, &read)
	if read.Review == nil || read.Review.Gone != 1 {
		s.t.Fatalf("variables review = %+v, want gone 1", read.Review)
	}
	if read.Source == nil || read.Source.Commit != secondCommit {
		s.t.Fatalf("variables source = %+v, want commit %s", read.Source, secondCommit)
	}

	s.runTemplateSucceeds(tid, "after the change")
}

// assertDiscovered checks that the repository's last sync read
// vars_demo.yml with want fields, and returns the commit it read.
func (s *scenario) assertDiscovered(want int) string {
	s.t.Helper()
	var out repoPlaybooksOut
	s.get("/api/v1/"+s.apiPath()+"/repos/"+s.repoID+"/playbooks", &out)
	if out.Sources == nil || len(out.Sources.Commit) != 40 || !out.Sources.WorkerReads {
		s.t.Fatalf("repo sources = %+v, want a 40-character commit read by the worker", out.Sources)
	}
	for _, p := range out.Playbooks {
		if p.Path != "vars_demo.yml" {
			continue
		}
		if p.Failed || p.Fields != want {
			s.t.Fatalf("discovered vars_demo.yml = %+v, want %d fields", p, want)
		}
		return out.Sources.Commit
	}
	s.t.Fatalf("discovered playbooks %+v do not list vars_demo.yml", out.Playbooks)
	return ""
}

// awaitTemplateVars polls the template's variables until done holds.
func (s *scenario) awaitTemplateVars(path string, timeout time.Duration, done func(templateVarsOut) bool) templateVarsOut {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		var v templateVarsOut
		s.get(path, &v)
		if done(v) {
			return v
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("template variables never reached the expected state in %s: %+v", timeout, v)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// runTemplateSucceeds runs the template and requires the run to succeed.
func (s *scenario) runTemplateSucceeds(tid, when string) {
	s.t.Helper()
	run := s.awaitJob(s.runTemplate(tid), "run", 90*time.Second)
	if run.Job.Status != "succeeded" {
		s.t.Fatalf("run %s = %q (error %q)%s", when, run.Job.Status, run.Job.Error, s.runnerOutput())
	}
}

// fieldsOf decodes the schema's top-level fields into their narrow mirror.
func fieldsOf(v templateVarsOut) []varFieldOut {
	if v.ExtraVarsSchema == nil {
		return nil
	}
	out := make([]varFieldOut, len(v.ExtraVarsSchema.Fields))
	for i, f := range v.ExtraVarsSchema.Fields {
		out[i].Name, _ = f["name"].(string)
		out[i].Label, _ = f["label"].(string)
		out[i].Default = f["default"]
		out[i].Origin, _ = f["origin"].(string)
		if a, ok := f["active"].(bool); ok {
			out[i].Active = &a
		}
	}
	return out
}

// fieldByNameOrFail returns the template's top-level field name, failing the
// scenario when the schema has none.
func (s *scenario) fieldByNameOrFail(v templateVarsOut, name string) varFieldOut {
	s.t.Helper()
	for _, f := range fieldsOf(v) {
		if f.Name == name {
			return f
		}
	}
	s.t.Fatalf("the template's schema has no field %s: %+v", name, fieldsOf(v))
	return varFieldOut{}
}

// isActive reads a field's activation: absent means active.
func isActive(f varFieldOut) bool { return f.Active == nil || *f.Active }
