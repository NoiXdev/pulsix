package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type bastionDTO struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Address      string  `json:"address"`
	Port         int     `json:"port"`
	Username     string  `json:"username"`
	CredentialID *string `json:"credential_id,omitempty"`
	ViaBastionID *string `json:"via_bastion_id,omitempty"`
	// SSHHostKey is the jump host's pinned key fingerprint (backlog 2.40),
	// absent while the jump host is unpinned (trust on first use).
	SSHHostKey *string `json:"ssh_host_key,omitempty"`
	// PinState is the jump host's pin state (backlog 2.43's field, filled
	// by 2.40): "full" when the pin carries the full public key
	// (enforceable), "fingerprint_only" when it holds only the fingerprint
	// — not enforceable until a connection test confirms the fingerprint
	// and fills the key. Absent while unpinned.
	PinState *string `json:"pin_state,omitempty" enum:"full,fingerprint_only"`
	// ObservedKey, ObservedState and ObservedAt are the newest hop
	// observation of the jump host from the connection tests — the
	// pin-what-you-saw surface: only what a test saw can be pinned. All
	// three are absent when no test has ever observed the jump host.
	ObservedKey   *string    `json:"observed_key,omitempty"`
	ObservedState *string    `json:"observed_state,omitempty"`
	ObservedAt    *time.Time `json:"observed_at,omitempty"`
}

// bastionPinData is the pin enrichment of one or more jump hosts: the pin
// state and pinned fingerprint from the hosts service's reads, plus the
// newest hop observation from the connection tests (the pin-what-you-saw
// surface).
type bastionPinData struct {
	pinStates map[uuid.UUID]string
	fps       map[uuid.UUID]string
	obs       map[uuid.UUID]hosts.BastionHopObservation
}

// resolveBastionPinData gathers the enrichment for the given jump hosts.
// A lookup failure fails the request.
func resolveBastionPinData(ctx context.Context, deps Deps, orgID uuid.UUID, bastionIDs []uuid.UUID) (*bastionPinData, error) {
	data := &bastionPinData{}
	states, err := deps.Hosts.BastionPinStates(ctx, orgID, bastionIDs)
	if err != nil {
		return nil, err
	}
	data.pinStates = states
	fps, err := deps.Hosts.PinnedBastionKeys(ctx, orgID, bastionIDs)
	if err != nil {
		return nil, err
	}
	data.fps = fps
	obs, err := deps.Hosts.LatestBastionObservations(ctx, orgID, bastionIDs)
	if err != nil {
		return nil, err
	}
	data.obs = obs
	return data, nil
}

func toBastionDTO(b sshconf.Bastion, data *bastionPinData) bastionDTO {
	out := bastionDTO{
		ID: b.ID.String(), Name: b.Name, Address: b.Address, Port: b.Port, Username: b.Username,
		CredentialID: optionalString(b.CredentialID), ViaBastionID: optionalString(b.ViaBastionID),
	}
	if data == nil {
		return out
	}
	if state, ok := data.pinStates[b.ID]; ok {
		out.PinState = &state
	}
	if fp, ok := data.fps[b.ID]; ok {
		out.SSHHostKey = &fp
	}
	if obs, ok := data.obs[b.ID]; ok {
		key, state := obs.Fingerprint, obs.State
		out.ObservedKey, out.ObservedState, out.ObservedAt = &key, &state, &obs.At
	}
	return out
}

type bastionBodyDTO struct {
	Name         string  `json:"name" minLength:"1" maxLength:"63"`
	Address      string  `json:"address" minLength:"1" maxLength:"253"`
	Port         int     `json:"port,omitempty" minimum:"0" maximum:"65535" doc:"0 or omitted means 22"`
	Username     string  `json:"username" minLength:"1" maxLength:"64"`
	CredentialID *string `json:"credential_id,omitempty"`
	ViaBastionID *string `json:"via_bastion_id,omitempty"`
}

func (b bastionBodyDTO) toInput() (sshconf.BastionInput, error) {
	cred, err := parseOptionalUUID(b.CredentialID, "credential_id")
	if err != nil {
		return sshconf.BastionInput{}, err
	}
	via, err := parseOptionalUUID(b.ViaBastionID, "via_bastion_id")
	if err != nil {
		return sshconf.BastionInput{}, err
	}
	return sshconf.BastionInput{Name: b.Name, Address: b.Address, Port: b.Port, Username: b.Username, CredentialID: cred, ViaBastionID: via}, nil
}

type bastionListInput struct {
	Org string `path:"org"`
}

type bastionListOutput struct {
	Body struct {
		Bastions []bastionDTO `json:"bastions"`
	}
}

type bastionGetInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

// bastionKeyInput is the jump host pin request: the fingerprint the
// newest connection test observed at this hop (the same shape the host
// route's hostKeyInput takes).
type bastionKeyInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body struct {
		Fingerprint string `json:"fingerprint" minLength:"1" maxLength:"71" pattern:"^SHA256:[A-Za-z0-9+/]{43}$"`
	}
}

type bastionOutput struct {
	Body struct {
		Bastion bastionDTO `json:"bastion"`
	}
}

type bastionCreateInput struct {
	Org  string `path:"org"`
	Body bastionBodyDTO
}

type bastionUpdateInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body bastionBodyDTO
}

type zoneBastionInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body struct {
		BastionID *string `json:"bastion_id,omitempty" doc:"Omit or send an empty body to clear the zone default"`
	}
}

type hostSSHDTO struct {
	HostID       string  `json:"host_id"`
	Username     string  `json:"username"`
	Port         int     `json:"port"`
	CredentialID *string `json:"credential_id,omitempty"`
	BastionMode  string  `json:"bastion_mode" enum:"inherit,none,explicit"`
	BastionID    *string `json:"bastion_id,omitempty"`
	// PinState is the host's host-key pin state (backlog 2.43): "full"
	// when the pin carries the full public key (enforceable),
	// "fingerprint_only" when it holds only the fingerprint (a pin made
	// before migration 00021 — its claims fail with the connection-test
	// remedy until a test completes it). Absent while the host is
	// unpinned.
	PinState *string `json:"pin_state,omitempty" enum:"full,fingerprint_only"`
}

func toHostSSHDTO(h sshconf.HostSSH) hostSSHDTO {
	return hostSSHDTO{
		HostID: h.HostID.String(), Username: h.Username, Port: h.Port, CredentialID: optionalString(h.CredentialID),
		BastionMode: string(h.BastionMode), BastionID: optionalString(h.BastionID),
	}
}

type hostSSHGetInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

type hostSSHOutput struct {
	Body struct {
		SSH hostSSHDTO `json:"ssh"`
	}
}

type hostSSHPutInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body struct {
		Username     string  `json:"username,omitempty" maxLength:"64"`
		Port         int     `json:"port,omitempty" minimum:"0" maximum:"65535" doc:"0 or omitted means 22"`
		CredentialID *string `json:"credential_id,omitempty"`
		BastionMode  string  `json:"bastion_mode,omitempty" enum:"inherit,none,explicit" doc:"Omitted means inherit"`
		BastionID    *string `json:"bastion_id,omitempty"`
	}
}

// registerSSHRoutes wires jump hosts (objects written with credentials.write,
// because they bind credentials), zone defaults and host SSH settings
// (inventory.write: assigning an existing jump host or credential reveals
// nothing).
func registerSSHRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/bastions", func(ctx context.Context, in *bastionListInput) (*bastionListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryRead)
		if err != nil {
			return nil, err
		}
		list, err := deps.SSH.ListBastions(ctx, scope.OrgID)
		if err != nil {
			return nil, mapSSHErr(ctx, err)
		}
		ids := make([]uuid.UUID, 0, len(list))
		for _, b := range list {
			ids = append(ids, b.ID)
		}
		data, err := resolveBastionPinData(ctx, deps, scope.OrgID, ids)
		if err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		out := &bastionListOutput{}
		out.Body.Bastions = make([]bastionDTO, len(list))
		for i, b := range list {
			out.Body.Bastions[i] = toBastionDTO(b, data)
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/bastions", func(ctx context.Context, in *bastionCreateInput) (*bastionOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermCredentialsWrite)
		if err != nil {
			return nil, err
		}
		bi, err := in.Body.toInput()
		if err != nil {
			return nil, err
		}
		b, err := deps.SSH.CreateBastion(ctx, scope.OrgID, bi)
		if err != nil {
			return nil, mapSSHErr(ctx, err)
		}
		setAuditObject(ctx, b.ID.String(), "created jump host "+b.Name)
		out := &bastionOutput{}
		out.Body.Bastion = toBastionDTO(b, nil)
		return out, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createBastion", "bastion"))

	huma.Get(hapi, "/api/v1/orgs/{org}/bastions/{id}", func(ctx context.Context, in *bastionGetInput) (*bastionOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryRead)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "jump host id")
		if err != nil {
			return nil, err
		}
		b, err := deps.SSH.GetBastion(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapSSHErr(ctx, err)
		}
		data, err := resolveBastionPinData(ctx, deps, scope.OrgID, []uuid.UUID{id})
		if err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		out := &bastionOutput{}
		out.Body.Bastion = toBastionDTO(b, data)
		return out, nil
	})

	huma.Put(hapi, "/api/v1/orgs/{org}/bastions/{id}/host-key", func(ctx context.Context, in *bastionKeyInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryWrite)
		if err != nil {
			return nil, err
		}
		bastionID, err := parseUUID(in.ID, "jump host id")
		if err != nil {
			return nil, err
		}
		// The bastion is resolved first: a jump host of another
		// organization is as invisible as one that does not exist (404),
		// before anything about its test history is asked.
		if _, err := deps.SSH.GetBastion(ctx, scope.OrgID, bastionID); err != nil {
			return nil, mapSSHErr(ctx, err)
		}
		// Only pin what you saw: the pin must equal the fingerprint the
		// newest connection test observed at this hop. A pin made from a
		// value the manager never saw would blind trust-on-first-use —
		// the mismatch case this feature exists to catch.
		obs, err := deps.Hosts.LatestBastionObservation(ctx, scope.OrgID, bastionID)
		if err != nil {
			if errors.Is(err, hosts.ErrNotFound) {
				return nil, huma.Error422UnprocessableEntity("no connection test has observed this jump host's key yet; run a connection test for a host routed through it and pin the fingerprint it reports")
			}
			return nil, mapHostsErr(ctx, err)
		}
		if obs.Fingerprint == "" || obs.Fingerprint != in.Body.Fingerprint {
			return nil, huma.Error422UnprocessableEntity("the fingerprint does not match the latest connection test result; only the observed key can be pinned")
		}
		// The pin stores the observed key itself (the worker's known_hosts
		// enforces it on every connection). A test whose hop key arrived
		// without the full key cannot provide it: a fresh test must
		// observe it.
		if obs.PublicKey == "" {
			return nil, huma.Error422UnprocessableEntity("the latest connection test did not record the full jump host key; run a new connection test and pin its result")
		}
		if err := deps.Hosts.SetBastionKey(ctx, scope.OrgID, bastionID, in.Body.Fingerprint, obs.PublicKey); err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		setAuditObject(ctx, bastionID.String(), "pinned jump host key")
		return &okOutput{}, nil
	}, withAudit("setBastionKey", "bastion"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/bastions/{id}/host-key", func(ctx context.Context, in *bastionGetInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryWrite)
		if err != nil {
			return nil, err
		}
		bastionID, err := parseUUID(in.ID, "jump host id")
		if err != nil {
			return nil, err
		}
		if _, err := deps.SSH.GetBastion(ctx, scope.OrgID, bastionID); err != nil {
			return nil, mapSSHErr(ctx, err)
		}
		if err := deps.Hosts.ClearBastionKey(ctx, scope.OrgID, bastionID); err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		setAuditObject(ctx, bastionID.String(), "cleared jump host key pin")
		return &okOutput{}, nil
	}, withAudit("clearBastionKey", "bastion"))

	huma.Patch(hapi, "/api/v1/orgs/{org}/bastions/{id}", func(ctx context.Context, in *bastionUpdateInput) (*bastionOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermCredentialsWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "jump host id")
		if err != nil {
			return nil, err
		}
		bi, err := in.Body.toInput()
		if err != nil {
			return nil, err
		}
		b, err := deps.SSH.UpdateBastion(ctx, scope.OrgID, id, bi)
		if err != nil {
			return nil, mapSSHErr(ctx, err)
		}
		setAuditObject(ctx, b.ID.String(), "updated jump host "+b.Name)
		out := &bastionOutput{}
		out.Body.Bastion = toBastionDTO(b, nil)
		return out, nil
	}, withAudit("updateBastion", "bastion"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/bastions/{id}", func(ctx context.Context, in *bastionGetInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermCredentialsWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "jump host id")
		if err != nil {
			return nil, err
		}
		if err := deps.SSH.DeleteBastion(ctx, scope.OrgID, id); err != nil {
			return nil, mapSSHErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "deleted jump host")
		return &okOutput{}, nil
	}, withAudit("deleteBastion", "bastion"))

	huma.Put(hapi, "/api/v1/orgs/{org}/zones/{id}/bastion", func(ctx context.Context, in *zoneBastionInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryWrite)
		if err != nil {
			return nil, err
		}
		zoneID, err := parseUUID(in.ID, "zone id")
		if err != nil {
			return nil, err
		}
		bastionID, err := parseOptionalUUID(in.Body.BastionID, "bastion_id")
		if err != nil {
			return nil, err
		}
		if err := deps.SSH.SetZoneBastion(ctx, scope.OrgID, zoneID, bastionID); err != nil {
			return nil, mapSSHErr(ctx, err)
		}
		summary := "cleared the zone's default jump host"
		if bastionID != nil {
			summary = "set the zone's default jump host"
		}
		setAuditObject(ctx, zoneID.String(), summary)
		return &okOutput{}, nil
	}, withAudit("setZoneBastion", "zone"))

	huma.Get(hapi, "/api/v1/orgs/{org}/hosts/{id}/ssh", func(ctx context.Context, in *hostSSHGetInput) (*hostSSHOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryRead)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "host id")
		if err != nil {
			return nil, err
		}
		h, err := deps.SSH.HostSSH(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapSSHErr(ctx, err)
		}
		out := &hostSSHOutput{}
		out.Body.SSH = toHostSSHDTO(h)
		pin, err := hostPinState(ctx, deps, scope.OrgID, id)
		if err != nil {
			return nil, mapSSHErr(ctx, err)
		}
		out.Body.SSH.PinState = pin
		return out, nil
	})

	huma.Put(hapi, "/api/v1/orgs/{org}/hosts/{id}/ssh", func(ctx context.Context, in *hostSSHPutInput) (*hostSSHOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "host id")
		if err != nil {
			return nil, err
		}
		cred, err := parseOptionalUUID(in.Body.CredentialID, "credential_id")
		if err != nil {
			return nil, err
		}
		bastion, err := parseOptionalUUID(in.Body.BastionID, "bastion_id")
		if err != nil {
			return nil, err
		}
		h, err := deps.SSH.SetHostSSH(ctx, scope.OrgID, id, sshconf.HostSSHInput{
			Username: in.Body.Username, Port: in.Body.Port, CredentialID: cred,
			BastionMode: sshconf.BastionMode(in.Body.BastionMode), BastionID: bastion,
		})
		if err != nil {
			return nil, mapSSHErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "updated SSH settings")
		out := &hostSSHOutput{}
		out.Body.SSH = toHostSSHDTO(h)
		// The pin is untouched by this write, but the response keeps the
		// read's shape: the same pin state travels back.
		pin, err := hostPinState(ctx, deps, scope.OrgID, id)
		if err != nil {
			return nil, mapSSHErr(ctx, err)
		}
		out.Body.SSH.PinState = pin
		return out, nil
	}, withAudit("setHostSSH", "host"))
}

func mapSSHErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, sshconf.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, sshconf.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, sshconf.ErrNameTaken):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, sshconf.ErrInUse):
		return huma.Error409Conflict("the jump host is still used by a zone, group, host or another jump host")
	default:
		slog.ErrorContext(ctx, "unmapped sshconf error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
