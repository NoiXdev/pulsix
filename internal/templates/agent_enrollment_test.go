package templates_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/templates"
)

func boolPtr(b bool) *bool { return &b }

// A template's agent enrollment flag (spec 2026-11-25 B) is stored on
// create and kept by an update that does not name it; setting or changing
// it needs the caller's leave (MayEnrolAgents, the agents.write the REST
// layer checks), and keeping it as it is does not.
func TestAgentEnrollmentFlag(t *testing.T) {
	f := newFixture()
	in := f.in()
	in.AgentEnrollment = true
	if _, err := f.svc.Create(ctx, f.org, in); !errors.Is(err, templates.ErrEnrollmentNotAllowed) {
		t.Fatalf("create with the flag, not allowed = %v, want ErrEnrollmentNotAllowed", err)
	}
	in.MayEnrolAgents = true
	created, err := f.svc.Create(ctx, f.org, in)
	if err != nil {
		t.Fatal(err)
	}
	if !created.AgentEnrollment {
		t.Fatal("created without the flag")
	}
	got, err := f.svc.Get(ctx, f.org, created.ID)
	if err != nil || !got.AgentEnrollment {
		t.Fatalf("Get = %+v, %v; want the flag on", got, err)
	}

	update := func(flag *bool, may bool) (templates.Template, error) {
		return f.svc.Update(ctx, f.org, created.ID, templates.UpdateInput{
			Name: "deploy-site", PlaybookID: f.playbook, AgentEnrollment: flag, MayEnrolAgents: may,
		})
	}
	kept, err := update(nil, false)
	if err != nil || !kept.AgentEnrollment {
		t.Fatalf("update without the flag = %+v, %v; want it kept on", kept, err)
	}
	if kept, err = update(boolPtr(true), false); err != nil || !kept.AgentEnrollment {
		t.Fatalf("update naming the stored value = %+v, %v; want it kept on", kept, err)
	}
	if _, err := update(boolPtr(false), false); !errors.Is(err, templates.ErrEnrollmentNotAllowed) {
		t.Fatalf("switch off, not allowed = %v, want ErrEnrollmentNotAllowed", err)
	}
	off, err := update(boolPtr(false), true)
	if err != nil || off.AgentEnrollment {
		t.Fatalf("switch off = %+v, %v; want it off", off, err)
	}
	// Part of the template's revision: a switch moves it, so pending run
	// requests' pictures change.
	if off.Revision <= kept.Revision {
		t.Fatalf("revision %d after the switch, %d before; want it moved", off.Revision, kept.Revision)
	}
	if _, err := update(boolPtr(true), false); !errors.Is(err, templates.ErrEnrollmentNotAllowed) {
		t.Fatalf("switch on, not allowed = %v, want ErrEnrollmentNotAllowed", err)
	}
}

// enrollVarNames are the host variables the claim sets for a host that
// gets an enrollment token (spec 2026-11-25 B, "What Pulsix sets").
var enrollVarNames = []string{"pulsix_agent_enroll_token", "pulsix_agent_enroll_tokens"}

// Extra vars beat host variables, so a template with agent enrollment must
// not send extra vars of the names the claim sets for each host: its own
// values, its schema's fields, a variables write, and the overrides of
// schedules, triggers and run requests are all refused (spec 2026-11-25 B,
// "Shadowing"). The pulsix_ reservation every template has refuses them;
// this pins it for a template with the flag.
func TestAgentEnrollmentRefusesShadowingVars(t *testing.T) {
	for _, name := range enrollVarNames {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			in := f.in()
			in.AgentEnrollment, in.MayEnrolAgents = true, true
			enrol, err := f.svc.Create(ctx, f.org, in)
			if err != nil {
				t.Fatal(err)
			}

			in.Name, in.ExtraVars = "values", map[string]any{name: "x"}
			if _, err := f.svc.Create(ctx, f.org, in); !slices.Equal(problemPaths(err), []string{name}) {
				t.Fatalf("create with the value = %v, want a problem at %s", err, name)
			}
			_, err = f.svc.Update(ctx, f.org, enrol.ID, templates.UpdateInput{
				Name: enrol.Name, PlaybookID: f.playbook, ExtraVars: map[string]any{name: "x"},
			})
			if !slices.Equal(problemPaths(err), []string{name}) {
				t.Fatalf("update with the value = %v, want a problem at %s", err, name)
			}

			in.Name, in.ExtraVars = "field", nil
			in.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{{Name: name, Type: schema.TypeString}}}
			if _, err := f.svc.Create(ctx, f.org, in); !errors.Is(err, templates.ErrBadSchema) {
				t.Fatalf("create with the field = %v, want ErrBadSchema", err)
			}
			_, err = f.svc.UpdateVariables(ctx, f.org, enrol.ID, templates.VariablesInput{
				Schema: &schema.Schema{Fields: []schema.Field{{Name: name, Type: schema.TypeString}}},
			})
			if !errors.Is(err, templates.ErrBadSchema) {
				t.Fatalf("variables write with the field = %v, want ErrBadSchema", err)
			}

			_, err = f.svc.ValidateOverrides(ctx, f.org, enrol.ID, templates.Overrides{ExtraVars: map[string]any{name: "y"}}, templates.OverrideOptions{})
			if !slices.Equal(problemPaths(err), []string{name}) {
				t.Fatalf("override = %v, want a problem at %s", err, name)
			}
		})
	}
}

// A save that keeps the flag (an assistant's, without If-Match) racing a
// person who switches it off must not write the flag it read back (review
// finding 3, controller ruling D): the write is guarded by the revision
// the service read, so it fails and the person's switch stands.
func TestAgentEnrollmentIsNotLostToAConcurrentSave(t *testing.T) {
	f := newFixture()
	in := f.in()
	in.AgentEnrollment, in.MayEnrolAgents = true, true
	created, err := f.svc.Create(ctx, f.org, in)
	if err != nil {
		t.Fatal(err)
	}
	f.store.beforeWrite = func() {
		cur := f.store.byID[created.ID]
		cur.AgentEnrollment = false
		cur.Revision++
		f.store.byID[created.ID] = cur
	}
	_, err = f.svc.Update(ctx, f.org, created.ID, templates.UpdateInput{Name: created.Name, PlaybookID: f.playbook, Limit: "web*"})
	if !errors.Is(err, templates.ErrRevisionMismatch) {
		t.Fatalf("racing save = %v, want ErrRevisionMismatch", err)
	}
	if f.store.byID[created.ID].AgentEnrollment {
		t.Fatal("the racing save wrote back the flag it read")
	}
}
