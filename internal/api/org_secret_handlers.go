// The organization's secret store (backlog 3.13d): GET, PUT and DELETE
// under /orgs/{org}/secrets. The credentials the server-run checks use
// (snmp's community or user) are kept here, sealed; no response carries a
// value.
package api

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/orgsecrets"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// OrgSecretStore is what the API may do with the organization's secrets:
// list them (Names for the host page's missing-secret warning), write and
// delete them. It deliberately has no Fields — opening a secret's values
// is the probe runner's alone, which internal/app hands the concrete
// *orgsecrets.Service; no handler can reach a value.
type OrgSecretStore interface {
	List(ctx context.Context, orgID uuid.UUID) ([]orgsecrets.Secret, error)
	Names(ctx context.Context, orgID uuid.UUID) ([]string, error)
	Put(ctx context.Context, orgID uuid.UUID, name string, fields map[string]string) (orgsecrets.Secret, error)
	Create(ctx context.Context, orgID uuid.UUID, name string, fields map[string]string) (orgsecrets.Secret, error)
	Delete(ctx context.Context, orgID uuid.UUID, name string) error
}

// orgSecretDTO is a secret as the API shows it: everything but its values.
type orgSecretDTO struct {
	Name       string    `json:"name"`
	FieldNames []string  `json:"field_names" doc:"The secret's field names, sorted; the values are never returned"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	UsedBy     []string  `json:"used_by" doc:"Keys of the server-run checks naming this secret, sorted; a secret in use cannot be deleted"`
}

func toOrgSecretDTO(s orgsecrets.Secret) orgSecretDTO {
	used := s.UsedBy
	if used == nil {
		used = []string{}
	}
	return orgSecretDTO{Name: s.Name, FieldNames: s.FieldNames, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, UsedBy: used}
}

type orgSecretListOutput struct {
	Body struct {
		Secrets []orgSecretDTO `json:"secrets"`
	}
}

type orgSecretPathInput struct {
	Org  string `path:"org"`
	Name string `path:"name"`
}

type orgSecretPutInput struct {
	Org         string `path:"org"`
	Name        string `path:"name"`
	IfNoneMatch string `header:"If-None-Match" doc:"* creates the secret only when the name is free (412 when it exists); without it PUT creates or replaces"`
	Body        struct {
		Fields orgSecretFields `json:"fields"`
	}
}

// orgSecretFields is the PUT body's fields object. Its schema bounds what
// a schema can: 1 to 16 properties, string values of 1 to 4096
// characters. The service stays the authority — maxLength counts
// characters where the rule is 4096 bytes, and a schema cannot bound the
// keys (field names) — and repeats every check.
type orgSecretFields map[string]string

// Schema implements huma.SchemaProvider.
func (orgSecretFields) Schema(huma.Registry) *huma.Schema {
	minProps, maxProps, minLen, maxLen := 1, 16, 1, 4096
	value := &huma.Schema{Type: huma.TypeString, MinLength: &minLen, MaxLength: &maxLen}
	value.PrecomputeMessages()
	return &huma.Schema{
		Type:                 huma.TypeObject,
		Description:          "The secret's fields, replacing every stored one: 1 to 16, names of 1 to 64 characters of A-Z a-z 0-9 _ . -, values of 1 to 4096 bytes without NUL",
		MinProperties:        &minProps,
		MaxProperties:        &maxProps,
		AdditionalProperties: value,
	}
}

type orgSecretOutput struct {
	Body struct {
		Secret orgSecretDTO `json:"secret"`
	}
}

// registerOrgSecretRoutes wires the secret store: the list needs
// checks.read (it carries names only), writes credentials.write (admin),
// the bar of the other stored credentials.
func registerOrgSecretRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/secrets", func(ctx context.Context, in *struct {
		Org string `path:"org"`
	}) (*orgSecretListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksRead)
		if err != nil {
			return nil, err
		}
		list, err := deps.OrgSecrets.List(ctx, scope.OrgID)
		if err != nil {
			return nil, mapOrgSecretsErr(ctx, err)
		}
		out := &orgSecretListOutput{}
		out.Body.Secrets = make([]orgSecretDTO, len(list))
		for i, s := range list {
			out.Body.Secrets[i] = toOrgSecretDTO(s)
		}
		return out, nil
	})

	huma.Put(hapi, "/api/v1/orgs/{org}/secrets/{name}", func(ctx context.Context, in *orgSecretPutInput) (*orgSecretOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermCredentialsWrite)
		if err != nil {
			return nil, err
		}
		write := deps.OrgSecrets.Put
		switch strings.TrimSpace(in.IfNoneMatch) {
		case "":
		case "*":
			write = deps.OrgSecrets.Create
		default:
			// A secret has no ETag a value could match.
			return nil, huma.Error400BadRequest("If-None-Match takes only *")
		}
		stored, err := write(ctx, scope.OrgID, in.Name, in.Body.Fields)
		if err != nil {
			return nil, mapOrgSecretsErr(ctx, err)
		}
		setAuditObject(ctx, stored.Name, "stored secret "+stored.Name+" (fields: "+strings.Join(stored.FieldNames, ", ")+")")
		out := &orgSecretOutput{}
		out.Body.Secret = toOrgSecretDTO(stored)
		return out, nil
	}, withAudit("putOrgSecret", "secret"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/secrets/{name}", func(ctx context.Context, in *orgSecretPathInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermCredentialsWrite)
		if err != nil {
			return nil, err
		}
		if err := deps.OrgSecrets.Delete(ctx, scope.OrgID, in.Name); err != nil {
			return nil, mapOrgSecretsErr(ctx, err)
		}
		setAuditObject(ctx, in.Name, "deleted secret "+in.Name)
		return &okOutput{}, nil
	}, withAudit("deleteOrgSecret", "secret"))
}

// mapOrgSecretsErr converts the store's errors; none of their texts
// carries a value. A 409 also lists the checks using the secret, one
// error detail per check key (location used_by, the key as its message:
// redactErrorValues clears every detail's value). The service returns
// ErrConflict only as an *InUseError, so the first case covers it.
func mapOrgSecretsErr(ctx context.Context, err error) error {
	var inUse *orgsecrets.InUseError
	switch {
	case errors.As(err, &inUse):
		details := make([]error, len(inUse.Keys))
		for i, key := range inUse.Keys {
			details[i] = &huma.ErrorDetail{Location: "used_by", Message: key}
		}
		return huma.Error409Conflict(err.Error(), details...)
	case errors.Is(err, orgsecrets.ErrExists):
		return huma.Error412PreconditionFailed("a secret of this name already exists")
	case errors.Is(err, orgsecrets.ErrInvalidInput):
		return huma.Error422UnprocessableEntity(err.Error())
	case errors.Is(err, orgsecrets.ErrNotFound):
		return huma.Error404NotFound("not found")
	default:
		slog.ErrorContext(ctx, "unmapped secret store error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
