package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// The Agents page and the host's Agent card (v0.3a, spec §8). Reading is
// a viewer's right — which hosts report is part of seeing the fleet —
// and enrolling or ending an agent is an operator's, the same bar
// hosts.write clears. The enrollment token itself is returned exactly
// once, by the route that creates it.

// defaultTokenMinutes and maxTokenMinutes bound how long an agent
// enrollment token may live. The default matches the worker side's fixed
// hour; the ceiling (a week) is what a staged rollout needs — a token
// left valid for longer is a credential nobody is watching.
const (
	defaultTokenMinutes = 60
	maxTokenMinutes     = 7 * 24 * 60
)

type agentDTO struct {
	HostID        string     `json:"host_id"`
	HostName      string     `json:"host_name"`
	AgentName     string     `json:"agent_name"`
	Version       string     `json:"version,omitempty"`
	Status        string     `json:"status" enum:"online,offline" doc:"online = a heartbeat within three heartbeat intervals"`
	LastSeen      *time.Time `json:"last_seen,omitempty"`
	CertExpiresAt *time.Time `json:"cert_expires_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

type agentTokenDTO struct {
	ID        string    `json:"id"`
	ZoneID    string    `json:"zone_id"`
	HostID    string    `json:"host_id,omitempty" doc:"Set when the token is bound to an existing host"`
	NameHint  string    `json:"name_hint,omitempty"`
	MaxUses   int       `json:"max_uses"`
	Uses      int       `json:"uses"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

type agentOrgInput struct {
	Org string `path:"org"`
}

type agentHostInput struct {
	Org    string `path:"org"`
	HostID string `path:"host_id"`
}

type agentHostPathInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

type agentTokenIDInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

type agentListOutput struct {
	Body struct {
		Agents []agentDTO `json:"agents"`
	}
}

type agentOutput struct {
	Body struct {
		Agent agentDTO `json:"agent"`
	}
}

type agentTokenListOutput struct {
	Body struct {
		Tokens []agentTokenDTO `json:"tokens"`
	}
}

type agentTokenCreateInput struct {
	Org  string `path:"org"`
	Body struct {
		ZoneID           string `json:"zone_id" format:"uuid" doc:"The zone a host created by this token lands in"`
		HostID           string `json:"host_id,omitempty" format:"uuid" doc:"Bind the token to an existing host; omit to let the first enrollment create one"`
		NameHint         string `json:"name_hint,omitempty" maxLength:"64" doc:"The name a created host gets; the agent's own name wins when empty"`
		MaxUses          int    `json:"max_uses" minimum:"1" maximum:"100" doc:"How many agents may redeem this token"`
		ExpiresInMinutes int    `json:"expires_in_minutes,omitempty" minimum:"1" maximum:"10080" doc:"Lifetime from now; 60 when omitted"`
	}
}

type agentTokenCreateOutput struct {
	Body struct {
		TokenRecord    agentTokenDTO `json:"token_record"`
		Token          string        `json:"token" doc:"Shown once — the manager stores only its hash"`
		InstallCommand string        `json:"install_command" doc:"Run this on the host to install and enrol the agent"`
	}
}

func toAgentDTO(v agents.View) agentDTO {
	dto := agentDTO{
		HostID: v.HostID.String(), HostName: v.HostName, AgentName: v.AgentName, Version: v.Version,
		LastSeen: v.LastSeen, CertExpiresAt: v.CertNotAfter, CreatedAt: v.CreatedAt, Status: "offline",
	}
	if v.Online {
		dto.Status = "online"
	}
	return dto
}

func toAgentTokenDTO(t agents.Token) agentTokenDTO {
	dto := agentTokenDTO{
		ID: t.ID.String(), ZoneID: t.ZoneID.String(), NameHint: t.NameHint,
		MaxUses: t.MaxUses, Uses: t.Uses, ExpiresAt: t.ExpiresAt, CreatedAt: t.CreatedAt,
	}
	if t.HostID != nil {
		dto.HostID = t.HostID.String()
	}
	return dto
}

// registerAgentRoutes wires the agent administration surface.
func registerAgentRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/agents", func(ctx context.Context, in *agentOrgInput) (*agentListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermAgentsRead)
		if err != nil {
			return nil, err
		}
		list, err := deps.Agents.ListView(ctx, scope.OrgID)
		if err != nil {
			return nil, mapAgentsErr(ctx, err)
		}
		out := &agentListOutput{}
		out.Body.Agents = make([]agentDTO, len(list))
		for i, v := range list {
			out.Body.Agents[i] = toAgentDTO(v)
		}
		return out, nil
	})

	huma.Get(hapi, "/api/v1/orgs/{org}/hosts/{id}/agent", func(ctx context.Context, in *agentHostPathInput) (*agentOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermAgentsRead)
		if err != nil {
			return nil, err
		}
		hostID, err := parseUUID(in.ID, "host id")
		if err != nil {
			return nil, err
		}
		v, err := deps.Agents.ViewByHost(ctx, scope.OrgID, hostID)
		if err != nil {
			return nil, mapAgentsErr(ctx, err)
		}
		out := &agentOutput{}
		out.Body.Agent = toAgentDTO(v)
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/agents/{host_id}/revoke", func(ctx context.Context, in *agentHostInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermAgentsWrite)
		if err != nil {
			return nil, err
		}
		hostID, err := parseUUID(in.HostID, "host id")
		if err != nil {
			return nil, err
		}
		// Read first: the revocation's audit line names the agent, and a
		// host without one must answer 404 rather than report success.
		v, err := deps.Agents.ViewByHost(ctx, scope.OrgID, hostID)
		if err != nil {
			return nil, mapAgentsErr(ctx, err)
		}
		if err := deps.Agents.Revoke(ctx, scope.OrgID, hostID); err != nil {
			return nil, mapAgentsErr(ctx, err)
		}
		// Nothing reports for this host any more. The flip is separate
		// from the revocation on purpose: monitored is a host capability
		// with its own partial-update rules (hosts.Service), not a column
		// the agent registry owns.
		monitored := false
		if _, err := deps.Hosts.SetCapabilities(ctx, scope.OrgID, hostID, nil, &monitored); err != nil && !errors.Is(err, hosts.ErrNotFound) {
			return nil, mapAgentsErr(ctx, err)
		}
		setAuditObject(ctx, hostID.String(), fmt.Sprintf("revoked agent %s on host %s", v.AgentName, v.HostName))
		return &okOutput{}, nil
	}, withAudit("revokeAgent", "agent"))

	huma.Get(hapi, "/api/v1/orgs/{org}/agents/tokens", func(ctx context.Context, in *agentOrgInput) (*agentTokenListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermAgentsRead)
		if err != nil {
			return nil, err
		}
		list, err := deps.Agents.ListTokens(ctx, scope.OrgID)
		if err != nil {
			return nil, mapAgentsErr(ctx, err)
		}
		out := &agentTokenListOutput{}
		out.Body.Tokens = make([]agentTokenDTO, len(list))
		for i, t := range list {
			out.Body.Tokens[i] = toAgentTokenDTO(t)
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/agents/tokens", func(ctx context.Context, in *agentTokenCreateInput) (*agentTokenCreateOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermAgentsWrite)
		if err != nil {
			return nil, err
		}
		zoneID, err := parseUUID(in.Body.ZoneID, "zone id")
		if err != nil {
			return nil, err
		}
		// The zone and the bound host must exist in this organization:
		// the registry does not hold either list (its own doc says so),
		// and a token minted against a stranger's zone would only fail at
		// enrollment time, on the host being installed.
		if err := requireZone(ctx, deps, scope.OrgID, zoneID); err != nil {
			return nil, err
		}
		input := agents.TokenInput{
			ZoneID: zoneID, NameHint: strings.TrimSpace(in.Body.NameHint),
			MaxUses: in.Body.MaxUses, CreatedBy: &scope.UserID,
		}
		if in.Body.HostID != "" {
			hostID, err := parseUUID(in.Body.HostID, "host id")
			if err != nil {
				return nil, err
			}
			if _, err := deps.Hosts.GetHost(ctx, scope.OrgID, hostID); err != nil {
				return nil, mapAgentsErr(ctx, err)
			}
			input.HostID = &hostID
		}
		minutes := in.Body.ExpiresInMinutes
		if minutes == 0 {
			minutes = defaultTokenMinutes
		}
		if minutes < 1 || minutes > maxTokenMinutes {
			return nil, huma.Error400BadRequest(fmt.Sprintf("expires_in_minutes must be between 1 and %d", maxTokenMinutes))
		}
		// The lifetime is a duration from now rather than a timestamp:
		// the manager's clock is the one the token is checked against, so
		// a browser's clock skew must not be able to mint a token that is
		// already expired (or valid far longer than the caller asked).
		input.ExpiresAt = time.Now().Add(time.Duration(minutes) * time.Minute)

		tok, err := deps.Agents.CreateToken(ctx, scope.OrgID, input)
		if err != nil {
			return nil, mapAgentsErr(ctx, err)
		}
		// Re-read the pending list to answer with the stored record: the
		// service hands back the token material only.
		record, err := findAgentToken(ctx, deps, scope.OrgID, tok.ID)
		if err != nil {
			return nil, mapAgentsErr(ctx, err)
		}
		setAuditObject(ctx, tok.ID.String(), "created an agent enrollment token")
		out := &agentTokenCreateOutput{}
		// AgentString, not String: the pxa1 prefix is what makes this an
		// agent token, and the agent's parser refuses a worker one.
		printed := enrolltoken.AgentString(tok)
		out.Body.TokenRecord, out.Body.Token = record, printed
		out.Body.InstallCommand = agentInstallCommand(deps, printed, record.NameHint)
		return out, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createAgentToken", "agent_enrollment"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/agents/tokens/{id}", func(ctx context.Context, in *agentTokenIDInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermAgentsWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "token id")
		if err != nil {
			return nil, err
		}
		if err := deps.Agents.RevokeToken(ctx, scope.OrgID, id); err != nil {
			return nil, mapAgentsErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "withdrew an agent enrollment token")
		return &okOutput{}, nil
	}, withAudit("withdrawAgentToken", "agent_enrollment"))
}

// requireZone refuses a zone that is not this organization's: 404, the
// same answer a zone of another organization gets, so the route cannot be
// used to probe for zone ids.
func requireZone(ctx context.Context, deps Deps, orgID, zoneID uuid.UUID) error {
	zones, err := deps.Hosts.ListZones(ctx, orgID)
	if err != nil {
		return mapAgentsErr(ctx, err)
	}
	for _, z := range zones {
		if z.ID == zoneID {
			return nil
		}
	}
	return huma.Error404NotFound("zone not found")
}

// findAgentToken reads back the token record just created. The create
// call returns the credential; the record the GUI lists comes from the
// same pending list every other read uses, so the two can never disagree.
func findAgentToken(ctx context.Context, deps Deps, orgID, id uuid.UUID) (agentTokenDTO, error) {
	list, err := deps.Agents.ListTokens(ctx, orgID)
	if err != nil {
		return agentTokenDTO{}, err
	}
	for _, t := range list {
		if t.ID == id {
			return toAgentTokenDTO(t), nil
		}
	}
	return agentTokenDTO{}, agents.ErrNotFound
}

// agentInstallCommand is the one line the Add-agent dialog shows next to
// the token: the manager's own install script, the token, and the gRPC
// endpoint the agent dials (the shared listener workers use). The
// certificate's name is passed only when it differs from the address's
// host — that is the case the agent cannot infer, and spelling it out
// always would make the common command noisier than it needs to be.
func agentInstallCommand(deps Deps, token, nameHint string) string {
	base := strings.TrimRight(deps.PublicURL, "/")
	if base == "" {
		base = "https://<manager>"
	}
	addr := deps.WorkerEndpoint.Addr
	if addr == "" {
		addr = "<manager>:8444"
	}
	cmd := fmt.Sprintf("curl -fsSL %s/install.sh | sudo sh -s -- --token %s --manager %s", base, token, addr)
	if name := deps.WorkerEndpoint.ServerName; name != "" && name != hostOf(addr) {
		cmd += " --server-name " + name
	}
	if nameHint != "" {
		cmd += " --name " + nameHint
	}
	return cmd
}

// hostOf is the host part of a host:port, or the input when it carries no
// port (then it is already a bare host).
func hostOf(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

func mapAgentsErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, agents.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, agents.ErrNotFound), errors.Is(err, hosts.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, agents.ErrTokenConflict):
		return huma.Error409Conflict("a host with this name already exists")
	case errors.Is(err, agents.ErrTokenInvalid):
		return huma.Error404NotFound("not found")
	}
	slog.ErrorContext(ctx, "unmapped agents error", "err", err)
	return huma.Error500InternalServerError("internal error")
}
