package mcp_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/jobs"
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

// TestGetJobNeverShowsAnEnrollmentToken (spec 2026-11-25 B, the
// acceptance): the assistant reads an enrolling run through get_job and
// get_job_log and sees the token counts, never the token its claim handed
// the host.
func TestGetJobNeverShowsAnEnrollmentToken(t *testing.T) {
	f := newOrgFixture(t)
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
	started, err := f.s.Deps.Jobs.EnqueueRunWith(f.scoped, f.org, f.template, jobs.RunRequest{
		Trigger: jobs.TriggerManual, Actor: &f.owner, EnrollHostIDs: []uuid.UUID{f.host},
	})
	if err != nil || len(started) != 1 {
		t.Fatalf("enqueue = %+v, %v", started, err)
	}
	c, err := f.s.Queue.Claim(context.Background(), f.org, []uuid.UUID{f.zone}, "v1")
	if err != nil || c == nil {
		t.Fatalf("Claim = %+v, %v", c, err)
	}
	var doc struct {
		Meta struct {
			Hostvars map[string]map[string]any `json:"hostvars"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal([]byte(c.Job.GetInventoryDoc()), &doc); err != nil {
		t.Fatal(err)
	}
	tok, _ := doc.Meta.Hostvars["web-1"]["pulsix_agent_enroll_token"].(string)
	if _, err := enrolltoken.ParseAgent(tok); err != nil {
		t.Fatalf("web-1's hostvars = %v, want a token", doc.Meta.Hostvars["web-1"])
	}
	secret := base64.RawURLEncoding.EncodeToString(parsedSecret(t, tok))

	cs := mustConnect(t, f.s, f.token)
	for _, tool := range []string{"get_job", "get_job_log"} {
		text := callOK(t, cs, tool, map[string]any{"job_id": started[0].ID.String()}, nil)
		if strings.Contains(text, tok) || strings.Contains(text, secret) {
			t.Fatalf("%s carries the token: %s", tool, text)
		}
		if tool == "get_job" && !strings.Contains(text, `"enroll_tokens"`) {
			t.Fatalf("get_job = %s, want the token counts", text)
		}
	}
}

// parsedSecret is an agent token's secret part.
func parsedSecret(t *testing.T, tok string) []byte {
	t.Helper()
	parsed, err := enrolltoken.ParseAgent(tok)
	if err != nil || len(parsed.Secret) == 0 {
		t.Fatalf("not an agent token: %v", err)
	}
	return parsed.Secret
}
