package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

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
}

func toBastionDTO(b sshconf.Bastion) bastionDTO {
	return bastionDTO{
		ID: b.ID.String(), Name: b.Name, Address: b.Address, Port: b.Port, Username: b.Username,
		CredentialID: optionalString(b.CredentialID), ViaBastionID: optionalString(b.ViaBastionID),
	}
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
		out := &bastionListOutput{}
		out.Body.Bastions = make([]bastionDTO, len(list))
		for i, b := range list {
			out.Body.Bastions[i] = toBastionDTO(b)
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
		out.Body.Bastion = toBastionDTO(b)
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
		out := &bastionOutput{}
		out.Body.Bastion = toBastionDTO(b)
		return out, nil
	})

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
		out.Body.Bastion = toBastionDTO(b)
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
