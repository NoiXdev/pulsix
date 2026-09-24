package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// agentFixture is one organization with a zone, a host, and the three
// roles the agent routes distinguish.
type agentFixture struct {
	s                      *apitest.Server
	org                    uuid.UUID
	zone                   uuid.UUID
	host                   hosts.Host
	base                   string
	owner, operator, viewx *http.Client
}

func newAgentFixture(t *testing.T, slug string) agentFixture {
	t.Helper()
	s := apitest.New(t)
	owner := s.SeedUser(slug+"-owner@example.test", "correct horse battery")
	operator := s.SeedUser(slug+"-operator@example.test", "correct horse battery")
	viewer := s.SeedUser(slug+"-viewer@example.test", "correct horse battery")
	org := s.SeedOrg(slug, owner)
	s.AddMember(org, operator, tenancy.RoleOperator)
	s.AddMember(org, viewer, tenancy.RoleViewer)

	ctx := db.WithOrgScope(context.Background(), org)
	zones, err := s.Deps.Hosts.ListZones(ctx, org)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}
	host, err := s.Deps.Hosts.CreateHost(ctx, org, hosts.HostInput{Name: slug + "-host", ZoneID: zones[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	return agentFixture{
		s: s, org: org, zone: zones[0].ID, host: host,
		base:  s.HTTP.URL + "/api/v1/orgs/" + slug,
		owner: s.LoginAs(owner), operator: s.LoginAs(operator), viewx: s.LoginAs(viewer),
	}
}

// enroll registers an agent for the fixture's host the way the gRPC
// enrollment path does, so the read routes have something to report.
func (f agentFixture) enroll(t *testing.T, name, version, serial string, lastSeen bool) {
	t.Helper()
	ctx := db.WithOrgScope(context.Background(), f.org)
	now := time.Now()
	if err := f.s.Deps.Agents.Register(ctx, agents.Registration{
		OrgID: f.org, HostID: f.host.ID, AgentName: name, Version: version, Serial: serial,
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(90 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	monitored := true
	if _, err := f.s.Deps.Hosts.SetCapabilities(ctx, f.org, f.host.ID, nil, &monitored); err != nil {
		t.Fatal(err)
	}
	if lastSeen {
		if err := f.s.Deps.Agents.Touch(context.Background(), serial, version); err != nil {
			t.Fatal(err)
		}
	}
}

type agentListBody struct {
	Agents []struct {
		HostID        string     `json:"host_id"`
		HostName      string     `json:"host_name"`
		AgentName     string     `json:"agent_name"`
		Version       string     `json:"version"`
		Status        string     `json:"status"`
		LastSeen      *time.Time `json:"last_seen"`
		CertExpiresAt *time.Time `json:"cert_expires_at"`
	} `json:"agents"`
}

// The enrollment token is the credential a host is installed with: it
// comes back exactly once, in the create response, together with the
// command that uses it — and never appears in any later read.
func TestAgentTokenIsShownOnceAndDrivesTheInstallCommand(t *testing.T) {
	f := newAgentFixture(t, "agent-tokens")

	resp := postJSON(t, f.operator, f.base+"/agents/tokens", map[string]any{
		"zone_id": f.zone.String(), "name_hint": "edge-1", "max_uses": 3, "expires_in_minutes": 120,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	var created struct {
		Token string `json:"token"`
		Item  struct {
			ID       string `json:"id"`
			NameHint string `json:"name_hint"`
			MaxUses  int    `json:"max_uses"`
		} `json:"token_record"`
		InstallCommand string `json:"install_command"`
	}
	decodeInto(t, resp, &created)
	if !strings.HasPrefix(created.Token, "pxa1.") {
		t.Fatalf("token = %q, want a pxa1 token", created.Token)
	}
	if created.Item.MaxUses != 3 || created.Item.NameHint != "edge-1" {
		t.Fatalf("token record = %+v", created.Item)
	}
	if !strings.Contains(created.InstallCommand, created.Token) ||
		!strings.Contains(created.InstallCommand, "/install.sh") ||
		!strings.Contains(created.InstallCommand, "--manager localhost:8444") {
		t.Fatalf("install command = %q", created.InstallCommand)
	}

	// The pending list names the token, never its secret.
	body := mustReadBody(t, requestJSON(t, f.viewx, http.MethodGet, f.base+"/agents/tokens", nil))
	if !strings.Contains(body, created.Item.ID) || !strings.Contains(body, "edge-1") {
		t.Fatalf("pending list = %s", body)
	}
	if strings.Contains(body, created.Token) || strings.Contains(body, strings.Split(created.Token, ".")[2]) {
		t.Fatalf("the pending list leaked the token secret: %s", body)
	}

	// Withdrawing it removes it; a second withdrawal is a 404.
	if resp := requestJSON(t, f.operator, http.MethodDelete, f.base+"/agents/tokens/"+created.Item.ID, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("withdraw: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, f.operator, http.MethodDelete, f.base+"/agents/tokens/"+created.Item.ID, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second withdraw: %d", resp.StatusCode)
	}
	if body := mustReadBody(t, requestJSON(t, f.viewx, http.MethodGet, f.base+"/agents/tokens", nil)); strings.Contains(body, created.Item.ID) {
		t.Fatalf("withdrawn token still listed: %s", body)
	}
}

// A token whose shape the registry refuses never reaches the database,
// and the refusal says which field is wrong.
func TestAgentTokenCreateValidates(t *testing.T) {
	f := newAgentFixture(t, "agent-token-validation")
	for name, body := range map[string]map[string]any{
		"no zone":        {"max_uses": 1},
		"zero uses":      {"zone_id": f.zone.String(), "max_uses": 0},
		"too many uses":  {"zone_id": f.zone.String(), "max_uses": 101},
		"expired on day": {"zone_id": f.zone.String(), "max_uses": 1, "expires_in_minutes": 0},
		"unknown host":   {"zone_id": f.zone.String(), "max_uses": 1, "host_id": uuid.Must(uuid.NewV7()).String()},
	} {
		t.Run(name, func(t *testing.T) {
			resp := postJSON(t, f.operator, f.base+"/agents/tokens", body)
			if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound &&
				resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want a refusal (body: %s)", resp.StatusCode, mustReadBody(t, resp))
			}
		})
	}
}

// The Agents page and the host's Agent card read the same registry: the
// agent, its host, its version, its certificate expiry and whether it is
// still reporting.
func TestAgentListAndHostCard(t *testing.T) {
	f := newAgentFixture(t, "agent-list")
	f.enroll(t, "agent-a", "v0.3a", "serial-list-1", true)

	var list agentListBody
	decodeInto(t, requestJSON(t, f.viewx, http.MethodGet, f.base+"/agents", nil), &list)
	if len(list.Agents) != 1 {
		t.Fatalf("agents = %+v, want 1", list.Agents)
	}
	got := list.Agents[0]
	if got.HostID != f.host.ID.String() || got.HostName != f.host.Name || got.AgentName != "agent-a" ||
		got.Version != "v0.3a" || got.Status != "online" || got.LastSeen == nil || got.CertExpiresAt == nil {
		t.Fatalf("agent row = %+v", got)
	}

	var card struct {
		Agent struct {
			AgentName string `json:"agent_name"`
			Status    string `json:"status"`
		} `json:"agent"`
	}
	decodeInto(t, requestJSON(t, f.viewx, http.MethodGet, f.base+"/hosts/"+f.host.ID.String()+"/agent", nil), &card)
	if card.Agent.AgentName != "agent-a" || card.Agent.Status != "online" {
		t.Fatalf("host card = %+v", card.Agent)
	}

	// A host without an agent has no card, and an unknown host id is the
	// same 404 — a caller cannot probe for hosts this way.
	ctx := db.WithOrgScope(context.Background(), f.org)
	other, err := f.s.Deps.Hosts.CreateHost(ctx, f.org, hosts.HostInput{Name: "agentless", ZoneID: f.zone})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{other.ID.String(), uuid.Must(uuid.NewV7()).String()} {
		if resp := requestJSON(t, f.viewx, http.MethodGet, f.base+"/hosts/"+id+"/agent", nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("host %s card = %d, want 404", id, resp.StatusCode)
		}
	}
}

// An agent that stopped reporting reads as offline: the verdict comes
// from last_seen at read time, so nothing has to run to produce it.
func TestAgentWithoutHeartbeatIsOffline(t *testing.T) {
	f := newAgentFixture(t, "agent-offline")
	f.enroll(t, "agent-quiet", "v0.3a", "serial-quiet", false)

	var list agentListBody
	decodeInto(t, requestJSON(t, f.viewx, http.MethodGet, f.base+"/agents", nil), &list)
	if len(list.Agents) != 1 || list.Agents[0].Status != "offline" || list.Agents[0].LastSeen != nil {
		t.Fatalf("agents = %+v, want one offline agent", list.Agents)
	}
}

// Revoking ends the agent: its certificate is refused from then on, the
// binding is gone, and the host stops being monitored — it has nothing
// reporting for it any more.
func TestAgentRevokeClearsTheBindingAndMonitoredFlag(t *testing.T) {
	f := newAgentFixture(t, "agent-revoke")
	f.enroll(t, "agent-r", "v0.3a", "serial-revoke", true)

	if resp := postJSON(t, f.viewx, f.base+"/agents/"+f.host.ID.String()+"/revoke", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer revoke = %d, want 403", resp.StatusCode)
	}
	if resp := postJSON(t, f.operator, f.base+"/agents/"+f.host.ID.String()+"/revoke", nil); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}

	ctx := db.WithOrgScope(context.Background(), f.org)
	host, err := f.s.Deps.Hosts.GetHost(ctx, f.org, f.host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if host.Monitored {
		t.Error("the host stayed monitored after its only agent was revoked")
	}
	if id, err := f.s.Deps.Agents.GetBySerial(context.Background(), "serial-revoke"); err == nil && !id.Revoked {
		t.Error("the revoked serial is still usable")
	}
	var list agentListBody
	decodeInto(t, requestJSON(t, f.viewx, http.MethodGet, f.base+"/agents", nil), &list)
	if len(list.Agents) != 0 {
		t.Fatalf("agents after revocation = %+v, want none", list.Agents)
	}
	// Revoking again is a 404: there is no agent left to end.
	if resp := postJSON(t, f.operator, f.base+"/agents/"+f.host.ID.String()+"/revoke", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second revoke = %d, want 404", resp.StatusCode)
	}
}

// The role matrix: a viewer reads, an operator writes, and a member of
// another organization sees nothing at all.
func TestAgentRoutesRoleMatrix(t *testing.T) {
	f := newAgentFixture(t, "agent-roles")
	f.enroll(t, "agent-m", "v0.3a", "serial-roles", true)

	// Viewer: reads yes, writes no.
	for _, path := range []string{"/agents", "/agents/tokens", "/hosts/" + f.host.ID.String() + "/agent"} {
		if resp := requestJSON(t, f.viewx, http.MethodGet, f.base+path, nil); resp.StatusCode != http.StatusOK {
			t.Errorf("viewer GET %s = %d, want 200", path, resp.StatusCode)
		}
	}
	if resp := postJSON(t, f.viewx, f.base+"/agents/tokens", map[string]any{"zone_id": f.zone.String(), "max_uses": 1}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer create token = %d, want 403", resp.StatusCode)
	}

	// A member of another organization: the org itself is invisible.
	outsider := f.s.SeedUser("agent-outsider@example.test", "correct horse battery")
	f.s.SeedOrg("agent-roles-other", outsider)
	oc := f.s.LoginAs(outsider)
	for _, path := range []string{"/agents", "/agents/tokens", "/hosts/" + f.host.ID.String() + "/agent"} {
		resp := requestJSON(t, oc, http.MethodGet, f.base+path, nil)
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
			t.Errorf("outsider GET %s = %d, want 403/404", path, resp.StatusCode)
		}
	}
}
