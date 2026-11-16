package mcp_test

import (
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/tenancy"
)

// TestReadTools: each read tool returns its route's data.
func TestReadTools(t *testing.T) {
	f := newOrgFixture(t)
	cs := mustConnect(t, f.s, f.token)
	cases := []struct {
		tool string
		args map[string]any
		want []string
	}{
		{"list_hosts", nil, []string{"web-1", f.host.String()}},
		{"get_host", map[string]any{"host_id": f.host.String()}, []string{`"host"`, `"ssh"`, `"groups"`, f.credential.String(), "webservers"}},
		{"list_groups", nil, []string{"webservers"}},
		{"get_group", map[string]any{"group_id": f.group.String()}, []string{"webservers", f.host.String()}},
		{"preview_inventory", nil, []string{"web-1"}},
		{"list_repos", nil, []string{"infra", "https://git.example.test/infra.git"}},
		{"get_repo", map[string]any{"repo_id": f.repo.String()}, []string{"ansible"}},
		{"list_repo_files", map[string]any{"repo_id": f.repo.String()}, nil},
		{"list_playbooks", nil, []string{f.playbook.String()}},
		{"get_playbook", map[string]any{"playbook_id": f.playbook.String()}, []string{"editor"}},
		{"list_job_templates", nil, []string{"deploy"}},
		{"get_job_template", map[string]any{"template_id": f.template.String()}, []string{"deploy", `"revision"`}},
		{"list_credentials", nil, []string{"deploy-key", "ssh_key"}},
	}
	for _, tc := range cases {
		text := callOK(t, cs, tc.tool, tc.args, nil)
		for _, w := range tc.want {
			if !strings.Contains(text, w) {
				t.Errorf("%s: result lacks %q:\n%s", tc.tool, w, text)
			}
		}
	}
}

func TestListCredentialsShowsOnlyIDNameKind(t *testing.T) {
	f := newOrgFixture(t)
	var out struct {
		Credentials []map[string]any `json:"credentials"`
	}
	callOK(t, mustConnect(t, f.s, f.token), "list_credentials", nil, &out)
	if len(out.Credentials) != 2 {
		t.Fatalf("credentials = %+v", out.Credentials)
	}
	for k := range out.Credentials[0] {
		if k != "id" && k != "name" && k != "kind" {
			t.Errorf("list_credentials shows %q", k)
		}
	}
}

func TestGetHostShowsNoKeyMaterial(t *testing.T) {
	f := newOrgFixture(t)
	text := callOK(t, mustConnect(t, f.s, f.token), "get_host", map[string]any{"host_id": f.host.String()}, nil)
	for _, bad := range []string{"public_key", "private", "BEGIN OPENSSH"} {
		if strings.Contains(text, bad) {
			t.Errorf("get_host shows %q:\n%s", bad, text)
		}
	}
}

// TestReadToolsNeedTheirArea: each read tool is hidden from, and refused
// for, a token without its area.
func TestReadToolsNeedTheirArea(t *testing.T) {
	f := newOrgFixture(t, tenancy.AreaJobsRead)
	cs := mustConnect(t, f.s, f.token)
	names := toolNames(t, cs)
	for _, tool := range []string{"list_hosts", "get_host", "list_groups", "get_group", "preview_inventory", "list_repos", "get_repo",
		"list_repo_files", "list_playbooks", "get_playbook", "list_job_templates", "get_job_template", "list_credentials"} {
		if names[tool] {
			t.Errorf("%s is listed for a jobs.read token", tool)
		}
		if _, isErr := call(t, cs, tool, map[string]any{}); !isErr {
			t.Errorf("%s ran for a jobs.read token", tool)
		}
	}
}

// TestIDArgumentsCannotSteerTheRoute: an id argument that is not a UUID is
// refused before any request is made.
func TestIDArgumentsCannotSteerTheRoute(t *testing.T) {
	f := newOrgFixture(t)
	text, isErr := call(t, mustConnect(t, f.s, f.token), "get_host", map[string]any{"host_id": "../tokens"})
	if !isErr || !strings.Contains(text, "not a valid id") {
		t.Fatalf("get_host with a path argument = %v %s", isErr, text)
	}
}

// TestToolOutputCarriesNoSchemaLink: the REST layer's "$schema" link names
// the in-process request's host (example.com), so it would point the
// assistant at a wrong address; tool output leaves it out.
func TestToolOutputCarriesNoSchemaLink(t *testing.T) {
	f := newOrgFixture(t)
	cs := mustConnect(t, f.s, f.token)
	for name, args := range map[string]map[string]any{
		"list_zones":        nil,
		"get_host":          {"host_id": f.host.String()},
		"update_group":      {"group_id": f.group.String(), "fields": map[string]any{"description": "d"}},
		"list_run_requests": nil,
	} {
		if text := callOK(t, cs, name, args, nil); strings.Contains(text, "$schema") {
			t.Errorf("%s output carries a $schema link:\n%s", name, text)
		}
	}
}
