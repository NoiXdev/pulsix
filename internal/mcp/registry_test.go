package mcp

import (
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/noixdev/pulsix/internal/tenancy"
)

// TestEveryToolRegisters: building every tool's schema must not panic (a
// panic would turn every initialize into a 500).
func TestEveryToolRegisters(t *testing.T) {
	for _, tl := range registry() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: %v", tl.name, r)
				}
			}()
			tl.register(sdk.NewServer(&sdk.Implementation{Name: "t"}, nil), &caller{})
		}()
	}
}

// TestAllowedFor: a tool is listed only when the token's areas unlock it
// and the effective role in the organization holds every permission of its
// area (spec 2026-11-17 personal tokens, rule 4: no tool that would always
// answer 403 there).
func TestAllowedFor(t *testing.T) {
	byName := map[string]toolDef{}
	for _, tl := range registry() {
		byName[tl.name] = tl
	}
	jobs := []tenancy.Area{tenancy.AreaJobsRead, tenancy.AreaJobsRequest}
	cases := []struct {
		tool  string
		areas []tenancy.Area
		role  tenancy.Role
		want  bool
	}{
		{"request_run", jobs, tenancy.RoleOperator, true},
		{"request_run", jobs, tenancy.RoleViewer, false},
		{"get_job_log", jobs, tenancy.RoleViewer, true},
		{"get_job_log", jobs, tenancy.RoleOperator, true},
		{"get_job_log", []tenancy.Area{tenancy.AreaInventoryRead}, tenancy.RoleOwner, false},
		{"get_job_log", jobs, "", false},
		{"get_job_log", jobs, "superuser", false},
	}
	for _, tc := range cases {
		tl, ok := byName[tc.tool]
		if !ok {
			t.Fatalf("no tool %s", tc.tool)
		}
		if got := tl.allowedFor(tc.areas, tc.role); got != tc.want {
			t.Errorf("%s with %v as %q = %v, want %v", tc.tool, tc.areas, tc.role, got, tc.want)
		}
	}
}
