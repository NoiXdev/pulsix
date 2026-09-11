package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// zoneDTO is the wire shape of one zone.
type zoneDTO struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

func toZoneDTO(z hosts.Zone) zoneDTO { return zoneDTO{ID: z.ID.String(), Slug: z.Slug, Name: z.Name} }

type zoneCreateInput struct {
	Org  string `path:"org"`
	Body struct {
		// The pattern mirrors org_handlers.go's own slug rule
		// (orgCreateInput.Body.Slug): lowercase letters, digits and single
		// hyphens between segments.
		Slug string `json:"slug" minLength:"1" maxLength:"63" pattern:"^[a-z0-9]+(-[a-z0-9]+)*$"`
		Name string `json:"name" minLength:"1"`
	}
}

type zoneCreateOutput struct {
	Body struct {
		Zone zoneDTO `json:"zone"`
	}
}

type zoneListInput struct {
	Org string `path:"org"`
}

type zoneListOutput struct {
	Body struct {
		Zones []zoneDTO `json:"zones"`
	}
}

// hostDTO is the wire shape of one host.
type hostDTO struct {
	ID         string         `json:"id"`
	ZoneID     string         `json:"zone_id"`
	Name       string         `json:"name"`
	Address    string         `json:"address"`
	Managed    bool           `json:"managed"`
	Monitored  bool           `json:"monitored"`
	Facts      map[string]any `json:"facts"`
	LastSeenAt *time.Time     `json:"last_seen_at,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

func toHostDTO(h hosts.Host) hostDTO {
	return hostDTO{
		ID: h.ID.String(), ZoneID: h.ZoneID.String(), Name: h.Name, Address: h.Address,
		Managed: h.Managed, Monitored: h.Monitored, Facts: h.Facts, LastSeenAt: h.LastSeenAt, CreatedAt: h.CreatedAt,
	}
}

// hostInputBody is the request body shared by host creation and full
// replacement (PATCH /hosts/{id}): every mutable field is required, not
// optional — a partial update that must not disturb fields it does not
// mention is exactly what PATCH /hosts/{id}/capabilities exists for
// instead (hostCapabilitiesInput below), matching
// hosts.Service.UpdateHost's own HostInput contract.
type hostInputBody struct {
	Name    string `json:"name" minLength:"1" maxLength:"254"`
	ZoneID  string `json:"zone_id" minLength:"1"`
	Address string `json:"address,omitempty"`
	// Managed and Monitored default to false when omitted — this is the
	// create/full-replace body (hosts.Service.CreateHost/UpdateHost's
	// HostInput), where "not mentioned" and "explicitly false" are the
	// same thing; PATCH /hosts/{id}/capabilities (hostCapabilitiesInput
	// below) is the route where that distinction matters, and there both
	// fields are pointers instead.
	Managed   bool `json:"managed,omitempty"`
	Monitored bool `json:"monitored,omitempty"`
}

func (b hostInputBody) toInput() (hosts.HostInput, error) {
	zoneID, err := uuid.Parse(b.ZoneID)
	if err != nil {
		return hosts.HostInput{}, huma.Error400BadRequest("invalid zone_id")
	}
	return hosts.HostInput{Name: b.Name, Address: b.Address, ZoneID: zoneID, Managed: b.Managed, Monitored: b.Monitored}, nil
}

type hostCreateInput struct {
	Org  string `path:"org"`
	Body hostInputBody
}

type hostCreateOutput struct {
	Body struct {
		Host hostDTO `json:"host"`
	}
}

type hostGetInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

type hostGetOutput struct {
	Body struct {
		Host hostDTO `json:"host"`
	}
}

type hostUpdateInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body hostInputBody
}

// hostListInput's Zone/Managed/Monitored are plain strings, not uuid.UUID/
// *bool: like orgActivityListInput (audit_handlers.go), huma validates the
// shape it can and the handler parses and rejects a malformed value with
// 400 itself, and an empty string unambiguously means "this filter is not
// set" for all three.
type hostListInput struct {
	Org       string `path:"org"`
	Zone      string `query:"zone"`
	Managed   string `query:"managed"`
	Monitored string `query:"monitored"`
	Q         string `query:"q"`
	Cursor    string `query:"cursor"`
	Limit     int    `query:"limit"`
}

type hostListOutput struct {
	Body struct {
		Hosts      []hostDTO `json:"hosts"`
		NextCursor string    `json:"next_cursor"`
	}
}

type hostCapabilitiesInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body struct {
		// Managed and Monitored are pointers: an omitted field must leave
		// that capability exactly as it was, never reset it to false — see
		// hosts.Service.SetCapabilities' own doc comment for why this is
		// what keeps the two capabilities genuinely independent.
		Managed   *bool `json:"managed,omitempty"`
		Monitored *bool `json:"monitored,omitempty"`
	}
}

// registerHostRoutes wires zones and hosts: GET/POST /orgs/{org}/zones,
// GET/POST /orgs/{org}/hosts, GET/PATCH/DELETE /orgs/{org}/hosts/{id}, and
// PATCH /orgs/{org}/hosts/{id}/capabilities. Every route resolves its scope
// through requireOrg — reads gated on tenancy.PermHostsRead, writes on
// tenancy.PermHostsWrite — the same shape every other org-scoped resource
// in this package uses, and it is mounted on the bearer-enabled
// *huma.Group in api.go alongside the organization/member/token/audit
// routes.
func registerHostRoutes(hapi huma.API, deps Deps) {
	huma.Post(hapi, "/api/v1/orgs/{org}/zones", func(ctx context.Context, in *zoneCreateInput) (*zoneCreateOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermHostsWrite)
		if err != nil {
			return nil, err
		}
		z, err := deps.Hosts.CreateZone(ctx, scope.OrgID, in.Body.Slug, in.Body.Name)
		if err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		setAuditObject(ctx, z.ID.String(), "created zone "+z.Name)
		out := &zoneCreateOutput{}
		out.Body.Zone = toZoneDTO(z)
		return out, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createZone", "host"))

	huma.Get(hapi, "/api/v1/orgs/{org}/zones", func(ctx context.Context, in *zoneListInput) (*zoneListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermHostsRead)
		if err != nil {
			return nil, err
		}
		zones, err := deps.Hosts.ListZones(ctx, scope.OrgID)
		if err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		out := &zoneListOutput{}
		out.Body.Zones = make([]zoneDTO, len(zones))
		for i, z := range zones {
			out.Body.Zones[i] = toZoneDTO(z)
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/hosts", func(ctx context.Context, in *hostCreateInput) (*hostCreateOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermHostsWrite)
		if err != nil {
			return nil, err
		}
		hostIn, err := in.Body.toInput()
		if err != nil {
			return nil, err
		}
		h, err := deps.Hosts.CreateHost(ctx, scope.OrgID, hostIn)
		if err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		setAuditObject(ctx, h.ID.String(), "created host "+h.Name)
		out := &hostCreateOutput{}
		out.Body.Host = toHostDTO(h)
		return out, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createHost", "host"))

	huma.Get(hapi, "/api/v1/orgs/{org}/hosts", func(ctx context.Context, in *hostListInput) (*hostListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermHostsRead)
		if err != nil {
			return nil, err
		}
		filter, err := parseHostListFilter(in.Zone, in.Managed, in.Monitored, in.Q)
		if err != nil {
			return nil, err
		}
		list, next, err := deps.Hosts.ListHosts(ctx, scope.OrgID, filter, in.Cursor, in.Limit)
		if err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		out := &hostListOutput{}
		out.Body.Hosts = make([]hostDTO, len(list))
		for i, h := range list {
			out.Body.Hosts[i] = toHostDTO(h)
		}
		out.Body.NextCursor = next
		return out, nil
	})

	huma.Get(hapi, "/api/v1/orgs/{org}/hosts/{id}", func(ctx context.Context, in *hostGetInput) (*hostGetOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermHostsRead)
		if err != nil {
			return nil, err
		}
		id, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid host id")
		}
		h, err := deps.Hosts.GetHost(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		out := &hostGetOutput{}
		out.Body.Host = toHostDTO(h)
		return out, nil
	})

	huma.Patch(hapi, "/api/v1/orgs/{org}/hosts/{id}", func(ctx context.Context, in *hostUpdateInput) (*hostGetOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermHostsWrite)
		if err != nil {
			return nil, err
		}
		id, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid host id")
		}
		hostIn, err := in.Body.toInput()
		if err != nil {
			return nil, err
		}
		h, err := deps.Hosts.UpdateHost(ctx, scope.OrgID, id, hostIn)
		if err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		setAuditObject(ctx, h.ID.String(), "updated host "+h.Name)
		out := &hostGetOutput{}
		out.Body.Host = toHostDTO(h)
		return out, nil
	}, withAudit("updateHost", "host"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/hosts/{id}", func(ctx context.Context, in *hostGetInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermHostsWrite)
		if err != nil {
			return nil, err
		}
		id, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid host id")
		}
		if err := deps.Hosts.DeleteHost(ctx, scope.OrgID, id); err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "deleted host")
		return &okOutput{}, nil
	}, withAudit("deleteHost", "host"))

	huma.Patch(hapi, "/api/v1/orgs/{org}/hosts/{id}/capabilities", func(ctx context.Context, in *hostCapabilitiesInput) (*hostGetOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermHostsWrite)
		if err != nil {
			return nil, err
		}
		id, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid host id")
		}
		h, err := deps.Hosts.SetCapabilities(ctx, scope.OrgID, id, in.Body.Managed, in.Body.Monitored)
		if err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		setAuditObject(ctx, h.ID.String(), "updated capabilities for host "+h.Name)
		out := &hostGetOutput{}
		out.Body.Host = toHostDTO(h)
		return out, nil
	}, withAudit("setHostCapabilities", "host"))
}

// parseHostListFilter turns the list route's string-typed query parameters
// into a hosts.ListFilter, rejecting a malformed zone id or managed/
// monitored value with 400 rather than letting it silently match nothing —
// the same contract parseActivityFilter gives its own optional filters
// (audit_handlers.go).
func parseHostListFilter(zoneStr, managedStr, monitoredStr, query string) (hosts.ListFilter, error) {
	f := hosts.ListFilter{Query: query}
	if zoneStr != "" {
		id, err := uuid.Parse(zoneStr)
		if err != nil {
			return hosts.ListFilter{}, huma.Error400BadRequest("invalid zone")
		}
		f.ZoneID = &id
	}
	if managedStr != "" {
		b, err := strconv.ParseBool(managedStr)
		if err != nil {
			return hosts.ListFilter{}, huma.Error400BadRequest("invalid managed: must be true or false")
		}
		f.Managed = &b
	}
	if monitoredStr != "" {
		b, err := strconv.ParseBool(monitoredStr)
		if err != nil {
			return hosts.ListFilter{}, huma.Error400BadRequest("invalid monitored: must be true or false")
		}
		f.Monitored = &b
	}
	return f, nil
}

// mapHostsErr converts hosts package errors to HTTP errors, the same shape
// mapTenancyErr and mapTokenErr give their own packages' sentinels:
// anything not recognized is logged server-side and replaced with a
// generic 500 rather than ever reaching the client.
func mapHostsErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, hosts.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, hosts.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, hosts.ErrNameTaken):
		return huma.Error409Conflict(err.Error())
	default:
		slog.ErrorContext(ctx, "unmapped hosts error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
