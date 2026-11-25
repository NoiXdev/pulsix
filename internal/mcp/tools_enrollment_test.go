package mcp_test

import (
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/templates"
)

// TestTemplateToolsTakeAgentEnrollment (spec 2026-11-25 B, "Template
// flag"): create_job_template and update_job_template accept
// agent_enrollment, and the REST layer refuses switching it without
// agents.write, which no assistant area grants. Saving a template that
// enrols agents leaves the flag as it is.
func TestTemplateToolsTakeAgentEnrollment(t *testing.T) {
	f := newOrgFixture(t)
	cs := mustConnect(t, f.s, f.token)
	text, isErr := call(t, cs, "create_job_template", map[string]any{"name": "enrol", "playbook_id": f.playbook.String(), "agent_enrollment": true})
	if !isErr || !strings.Contains(text, "agents.write") {
		t.Fatalf("create with agent_enrollment = %v %s, want a refusal naming agents.write", isErr, text)
	}
	text, isErr = call(t, cs, "update_job_template", map[string]any{"template_id": f.template.String(), "fields": map[string]any{"agent_enrollment": true}})
	if !isErr || !strings.Contains(text, "agents.write") || strings.Contains(text, "unknown field") {
		t.Fatalf("update with agent_enrollment = %v %s, want the REST refusal naming agents.write", isErr, text)
	}

	// A person switches it on; the assistant's later edits keep it.
	cur, err := f.s.Deps.Templates.Get(f.scoped, f.org, f.template)
	if err != nil {
		t.Fatal(err)
	}
	on := true
	if _, err := f.s.Deps.Templates.Update(f.scoped, f.org, f.template, templates.UpdateInput{
		Name: cur.Name, PlaybookID: cur.PlaybookID, Scope: cur.Scope, Limit: cur.Limit, ExtraVars: cur.ExtraVars,
		ExtraVarsSchema: cur.ExtraVarsSchema, CredentialID: cur.CredentialID, VaultCredentials: cur.VaultCredentials,
		AgentEnrollment: &on, MayEnrolAgents: true,
	}); err != nil {
		t.Fatal(err)
	}
	callOK(t, cs, "update_job_template", map[string]any{"template_id": f.template.String(), "fields": map[string]any{"limit": "web-*"}}, nil)
	if got, err := f.s.Deps.Templates.Get(f.scoped, f.org, f.template); err != nil || !got.AgentEnrollment || got.Limit != "web-*" {
		t.Fatalf("after the assistant's edit: %+v, %v; want the flag kept on", got, err)
	}
	text, isErr = call(t, cs, "update_job_template", map[string]any{"template_id": f.template.String(), "fields": map[string]any{"agent_enrollment": false}})
	if !isErr || !strings.Contains(text, "agents.write") {
		t.Fatalf("switching it off = %v %s, want a refusal", isErr, text)
	}
}
