package mcp_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// allAreas is every area an operator-capped assistant token can hold.
var allAreas = []tenancy.Area{
	tenancy.AreaInventoryWrite, tenancy.AreaReposWrite, tenancy.AreaTemplatesWrite,
	tenancy.AreaCredentialsNames, tenancy.AreaJobsRequest,
	tenancy.AreaMonitoringRead, tenancy.AreaAlertsHandle, tenancy.AreaActivityRead,
	tenancy.AreaTemplatesRead,
}

// orgFixture is one organization with a host, a group, a repository, an
// editor playbook, a template and an SSH key credential, plus an operator
// and an assistant token holding all areas.
type orgFixture struct {
	s          *apitest.Server
	org        uuid.UUID
	scoped     context.Context
	owner      uuid.UUID
	zone       uuid.UUID
	host       uuid.UUID
	group      uuid.UUID
	repo       uuid.UUID
	playbook   uuid.UUID
	template   uuid.UUID
	credential uuid.UUID
	vault      uuid.UUID
	token      string
}

func newOrgFixture(t *testing.T, areas ...tenancy.Area) orgFixture {
	t.Helper()
	s := apitest.New(t)
	if len(areas) == 0 {
		areas = allAreas
	}
	org, tok := s.OrgWithAssistantToken(t, tenancy.RoleOperator, areas...)
	scoped := db.WithOrgScope(context.Background(), org)
	f := orgFixture{s: s, org: org, scoped: scoped, token: tok}
	var err error
	if err = s.DB.Pool().QueryRow(context.Background(), `SELECT user_id FROM org_memberships WHERE org_id = $1 LIMIT 1`, org).Scan(&f.owner); err != nil {
		t.Fatal(err)
	}
	zones, err := s.Deps.Hosts.ListZones(scoped, org)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}
	f.zone = zones[0].ID
	h, err := s.Deps.Hosts.CreateHost(scoped, org, hosts.HostInput{Name: "web-1", Address: "10.0.0.1", ZoneID: f.zone, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	f.host = h.ID
	cred, err := s.Deps.Credentials.Create(scoped, org, credentials.CreateInput{
		Name: "deploy-key", Kind: credentials.KindSSHKey, Username: "ansible", Material: credentials.Material{Generate: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.credential = cred.ID
	vault, err := s.Deps.Credentials.Create(scoped, org, credentials.CreateInput{
		Name: "noidee-vault", Kind: credentials.KindVaultPassword, Material: credentials.Material{Password: "vault-passphrase-7f3a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.vault = vault.ID
	if _, err := s.Deps.SSH.SetHostSSH(scoped, org, f.host, sshconf.HostSSHInput{CredentialID: &cred.ID, Port: 22, BastionMode: sshconf.ModeInherit}); err != nil {
		t.Fatal(err)
	}
	g, err := s.Deps.Inventory.CreateGroup(scoped, org, inventory.GroupInput{Name: "webservers"})
	if err != nil {
		t.Fatal(err)
	}
	f.group = g.ID
	if _, err := s.Deps.Inventory.Assign(scoped, org, g.ID, f.host, nil, nil); err != nil {
		t.Fatal(err)
	}
	repo, err := s.Deps.Repos.Create(scoped, org, repos.CreateInput{Name: "infra", URL: "https://git.example.test/infra.git", Branch: "main", Subpath: "ansible"})
	if err != nil {
		t.Fatal(err)
	}
	f.repo = repo.ID
	pb, err := s.Deps.Playbooks.CreateEditor(scoped, org, playbooks.CreateInput{Inline: "- name: deploy\n  hosts: all\n  tasks: []\n"})
	if err != nil {
		t.Fatal(err)
	}
	f.playbook = pb.ID
	tmpl, err := s.Deps.Templates.Create(scoped, org, templates.CreateInput{
		Name: "deploy", PlaybookID: pb.ID, Scope: templates.Scope{HostIDs: []uuid.UUID{f.host}},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.template = tmpl.ID
	return f
}
