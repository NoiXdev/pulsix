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

	"github.com/noixdev/pulsix/internal/orgsecrets"
	"github.com/noixdev/pulsix/internal/tenancy"
)

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
	Org  string `path:"org"`
	Name string `path:"name"`
	Body struct {
		Fields map[string]string `json:"fields" doc:"The secret's fields, replacing every stored one: 1 to 16, names of 1 to 64 characters of A-Z a-z 0-9 _ . -, values of 1 to 4096 bytes without NUL"`
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
		if err := deps.OrgSecrets.Put(ctx, scope.OrgID, in.Name, in.Body.Fields); err != nil {
			return nil, mapOrgSecretsErr(ctx, err)
		}
		list, err := deps.OrgSecrets.List(ctx, scope.OrgID)
		if err != nil {
			return nil, mapOrgSecretsErr(ctx, err)
		}
		out := &orgSecretOutput{}
		for _, s := range list {
			if s.Name == in.Name {
				out.Body.Secret = toOrgSecretDTO(s)
			}
		}
		setAuditObject(ctx, in.Name, "stored secret "+in.Name+" (fields: "+strings.Join(out.Body.Secret.FieldNames, ", ")+")")
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
// carries a value.
func mapOrgSecretsErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, orgsecrets.ErrInvalidInput):
		return huma.Error422UnprocessableEntity(err.Error())
	case errors.Is(err, orgsecrets.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, orgsecrets.ErrConflict):
		return huma.Error409Conflict(err.Error())
	default:
		slog.ErrorContext(ctx, "unmapped secret store error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
