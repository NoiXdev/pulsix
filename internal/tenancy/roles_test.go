package tenancy_test

import (
	"testing"

	"github.com/noixdev/pulsix/internal/tenancy"
)

func TestRoleMatrix(t *testing.T) {
	cases := []struct {
		role tenancy.Role
		perm tenancy.Permission
		want bool
	}{
		{tenancy.RoleOwner, tenancy.PermOrgManage, true},
		{tenancy.RoleOwner, tenancy.PermOrgDelete, true},
		{tenancy.RoleAdmin, tenancy.PermOrgManage, true},
		{tenancy.RoleAdmin, tenancy.PermOrgDelete, false},
		{tenancy.RoleAdmin, tenancy.PermMembersManage, true},
		{tenancy.RoleAdmin, tenancy.PermTokensManage, true},
		{tenancy.RoleOperator, tenancy.PermMembersManage, false},
		{tenancy.RoleOperator, tenancy.PermTokensManage, false},
		{tenancy.RoleOperator, tenancy.PermHostsWrite, true},
		{tenancy.RoleOperator, tenancy.PermHostsRead, true},
		{tenancy.RoleOperator, tenancy.PermAuditRead, true},
		{tenancy.RoleViewer, tenancy.PermHostsWrite, false},
		{tenancy.RoleViewer, tenancy.PermHostsRead, true},
		{tenancy.RoleViewer, tenancy.PermAuditRead, false},
		{tenancy.Role("bogus"), tenancy.PermHostsRead, false},
		{tenancy.RoleOwner, tenancy.Permission("bogus.perm"), false},
		{tenancy.RoleViewer, tenancy.PermInventoryRead, true},
		{tenancy.RoleViewer, tenancy.PermInventoryWrite, false},
		{tenancy.RoleOperator, tenancy.PermInventoryWrite, true},
		{tenancy.RoleViewer, tenancy.PermCredentialsRead, false},
		{tenancy.RoleOperator, tenancy.PermCredentialsRead, true},
		{tenancy.RoleOperator, tenancy.PermCredentialsWrite, false},
		{tenancy.RoleAdmin, tenancy.PermCredentialsWrite, true},
		{tenancy.RoleViewer, tenancy.PermReposRead, true},
		{tenancy.RoleViewer, tenancy.PermReposWrite, false},
		{tenancy.RoleOperator, tenancy.PermReposWrite, true},
		{tenancy.RoleViewer, tenancy.PermPlaybooksRead, true},
		{tenancy.RoleViewer, tenancy.PermPlaybooksWrite, false},
		{tenancy.RoleOperator, tenancy.PermPlaybooksWrite, true},
		{tenancy.RoleViewer, tenancy.PermJobsRead, true},
		{tenancy.RoleViewer, tenancy.PermJobsRun, false},
		{tenancy.RoleOperator, tenancy.PermJobsRun, true},
		{tenancy.RoleOperator, tenancy.PermWorkersRead, false},
		{tenancy.RoleOperator, tenancy.PermWorkersWrite, false},
		{tenancy.RoleAdmin, tenancy.PermWorkersRead, true},
		{tenancy.RoleAdmin, tenancy.PermWorkersWrite, true},
		{tenancy.RoleOwner, tenancy.PermWorkersWrite, true},
	}
	for _, c := range cases {
		if got := tenancy.Allows(c.role, c.perm); got != c.want {
			t.Errorf("Allows(%s, %s) = %v, want %v", c.role, c.perm, got, c.want)
		}
	}
}

func TestRoleOrdering(t *testing.T) {
	if tenancy.Lower(tenancy.RoleOwner, tenancy.RoleOperator) != tenancy.RoleOperator {
		t.Fatal("Lower(owner, operator) != operator")
	}
	if tenancy.Lower(tenancy.RoleViewer, tenancy.RoleAdmin) != tenancy.RoleViewer {
		t.Fatal("Lower(viewer, admin) != viewer")
	}
	if tenancy.Lower(tenancy.RoleAdmin, tenancy.RoleAdmin) != tenancy.RoleAdmin {
		t.Fatal("Lower(admin, admin) != admin")
	}
}

func TestValid(t *testing.T) {
	for _, r := range []tenancy.Role{tenancy.RoleOwner, tenancy.RoleAdmin, tenancy.RoleOperator, tenancy.RoleViewer} {
		if !tenancy.Valid(r) {
			t.Errorf("Valid(%s) = false, want true", r)
		}
	}
	if tenancy.Valid(tenancy.Role("bogus")) {
		t.Error("Valid(bogus) = true, want false")
	}
}
