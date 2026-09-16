package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type credentialUsageDTO struct {
	Bastions int `json:"bastions"`
	Hosts    int `json:"hosts"`
}

type credentialDTO struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Kind        string             `json:"kind" enum:"ssh_key,ssh_password,git_key,git_token"`
	Username    string             `json:"username"`
	PublicKey   string             `json:"public_key,omitempty"`
	Fingerprint string             `json:"fingerprint,omitempty"`
	KeyType     string             `json:"key_type,omitempty"`
	ExpiresAt   *time.Time         `json:"expires_at,omitempty"`
	Expired     bool               `json:"expired"`
	ExpiresSoon bool               `json:"expires_soon"`
	UsedBy      credentialUsageDTO `json:"used_by"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

type credentialSnippetsDTO struct {
	AuthorizedKeysCommand string `json:"authorized_keys_command"`
	CloudInit             string `json:"cloud_init"`
	Terraform             string `json:"terraform"`
}

func toCredentialDTO(deps Deps, c credentials.Credential, usage credentials.Usage, orgSlug string) credentialDTO {
	expired, soon := deps.Credentials.ExpiryState(c)
	return credentialDTO{
		ID: c.ID.String(), Name: c.Name, Kind: string(c.Kind), Username: c.Username,
		PublicKey: credentials.AuthorizedKey(c, orgSlug), Fingerprint: c.Fingerprint, KeyType: c.KeyType,
		ExpiresAt: c.ExpiresAt, Expired: expired, ExpiresSoon: soon,
		UsedBy:    credentialUsageDTO{Bastions: usage.Bastions, Hosts: usage.Hosts},
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

func toSnippetsDTO(c credentials.Credential, orgSlug string) *credentialSnippetsDTO {
	s := credentials.SnippetsFor(c, orgSlug)
	if s == (credentials.Snippets{}) {
		return nil
	}
	return &credentialSnippetsDTO{AuthorizedKeysCommand: s.AuthorizedKeysCommand, CloudInit: s.CloudInit, Terraform: s.Terraform}
}

// credentialMaterial is the secret part of a create or update body. None of
// these fields is ever echoed back.
//
// It is not embedded into credentialCreateInput.Body/credentialUpdateInput.Body:
// an anonymous field takes its type's name as the field name, and huma's
// schema flattening (unlike encoding/json's) only promotes an anonymous
// field that is itself exported (huma/v2's getFields, schema.go, checks
// reflect.StructField.IsExported() before deciding whether to flatten it) —
// "credentialMaterial" starts with a lowercase letter, so huma left every
// one of these five fields out of the generated request schema and rejected
// them as unexpected properties, even though encoding/json would have
// decoded them correctly. The five fields are copied into both bodies
// instead (deviation from the brief's embedded-struct draft, per the
// brief's own documented fallback), and toMaterial is a plain function
// taking them rather than a method on this type; the wire format is
// unchanged.
type credentialMaterial struct {
	Generate   bool
	PrivateKey string
	Passphrase string
	Password   string
	Token      string
}

func (m credentialMaterial) toMaterial() credentials.Material {
	return credentials.Material{Generate: m.Generate, PrivateKey: m.PrivateKey, Passphrase: m.Passphrase, Password: m.Password, Token: m.Token}
}

type credentialListInput struct {
	Org string `path:"org"`
}

type credentialListOutput struct {
	Body struct {
		Credentials []credentialDTO `json:"credentials"`
	}
}

type credentialGetInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

// credentialRotateInput is the rotate request (no body).
type credentialRotateInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

// rotationStartOutput is the 202 body of POST .../rotate: the rotation
// group's first job (so the SPA can navigate to the run view) and the
// number of rotate_key jobs the group carries. A credential nothing
// references re-keys immediately: job_count is 0 and job_id is empty.
type rotationStartOutput struct {
	Body struct {
		JobID    string `json:"job_id"`
		JobCount int    `json:"job_count"`
	}
}

type credentialDetailOutput struct {
	Body struct {
		Credential credentialDTO          `json:"credential"`
		Snippets   *credentialSnippetsDTO `json:"snippets,omitempty"`
	}
}

type credentialCreateInput struct {
	Org  string `path:"org"`
	Body struct {
		Name       string     `json:"name" minLength:"1" maxLength:"63"`
		Kind       string     `json:"kind" enum:"ssh_key,ssh_password,git_key,git_token"`
		Username   string     `json:"username,omitempty" maxLength:"64"`
		ExpiresAt  *time.Time `json:"expires_at,omitempty"`
		Generate   bool       `json:"generate,omitempty" doc:"Key kinds: generate a new ed25519 key"`
		PrivateKey string     `json:"private_key,omitempty" maxLength:"16384" doc:"Key kinds: import this private key"`
		Passphrase string     `json:"passphrase,omitempty" maxLength:"1024" doc:"Decrypts private_key once; never stored"`
		Password   string     `json:"password,omitempty" maxLength:"1024"`
		Token      string     `json:"token,omitempty" maxLength:"4096"`
	}
}

func (b credentialCreateInput) material() credentialMaterial {
	return credentialMaterial{Generate: b.Body.Generate, PrivateKey: b.Body.PrivateKey, Passphrase: b.Body.Passphrase, Password: b.Body.Password, Token: b.Body.Token}
}

type credentialUpdateInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body struct {
		Name       string     `json:"name" minLength:"1" maxLength:"63"`
		Username   string     `json:"username,omitempty" maxLength:"64"`
		ExpiresAt  *time.Time `json:"expires_at,omitempty"`
		Generate   bool       `json:"generate,omitempty" doc:"Key kinds: generate a new ed25519 key"`
		PrivateKey string     `json:"private_key,omitempty" maxLength:"16384" doc:"Key kinds: import this private key"`
		Passphrase string     `json:"passphrase,omitempty" maxLength:"1024" doc:"Decrypts private_key once; never stored"`
		Password   string     `json:"password,omitempty" maxLength:"1024"`
		Token      string     `json:"token,omitempty" maxLength:"4096"`
	}
}

func (b credentialUpdateInput) material() credentialMaterial {
	return credentialMaterial{Generate: b.Body.Generate, PrivateKey: b.Body.PrivateKey, Passphrase: b.Body.Passphrase, Password: b.Body.Password, Token: b.Body.Token}
}

// registerCredentialRoutes wires the credential store: reads need
// credentials.read (operator), writes credentials.write (admin). Responses
// carry metadata, the public key with its Pulsix comment and, for SSH keys,
// install snippets; never a private key, password or token.
func registerCredentialRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/credentials", func(ctx context.Context, in *credentialListInput) (*credentialListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermCredentialsRead)
		if err != nil {
			return nil, err
		}
		list, err := deps.Credentials.List(ctx, scope.OrgID)
		if err != nil {
			return nil, mapCredentialsErr(ctx, err)
		}
		usage, err := deps.Credentials.Usage(ctx, scope.OrgID)
		if err != nil {
			return nil, mapCredentialsErr(ctx, err)
		}
		out := &credentialListOutput{}
		out.Body.Credentials = make([]credentialDTO, len(list))
		for i, c := range list {
			out.Body.Credentials[i] = toCredentialDTO(deps, c, usage[c.ID], scope.Org.Slug)
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/credentials", func(ctx context.Context, in *credentialCreateInput) (*credentialDetailOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermCredentialsWrite)
		if err != nil {
			return nil, err
		}
		creator := scope.UserID
		c, err := deps.Credentials.Create(ctx, scope.OrgID, credentials.CreateInput{
			Name: in.Body.Name, Kind: credentials.Kind(in.Body.Kind), Username: in.Body.Username,
			ExpiresAt: in.Body.ExpiresAt, CreatedBy: &creator, Material: in.material().toMaterial(),
		})
		if err != nil {
			return nil, mapCredentialsErr(ctx, err)
		}
		setAuditObject(ctx, c.ID.String(), "created credential "+c.Name)
		return credentialDetail(deps, c, credentials.Usage{}, scope.Org.Slug), nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createCredential", "credential"))

	huma.Post(hapi, "/api/v1/orgs/{org}/credentials/{id}/rotate", func(ctx context.Context, in *credentialRotateInput) (*rotationStartOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermCredentialsWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "credential id")
		if err != nil {
			return nil, err
		}
		actor := scope.UserID
		result, err := deps.Credentials.Rotate(ctx, scope.OrgID, id, &actor)
		if err != nil {
			return nil, mapCredentialsErr(ctx, err)
		}
		summary := fmt.Sprintf("started credential rotation (%d job(s))", result.Count)
		if result.Count == 0 {
			summary = "rotated credential (nothing referenced it; re-keyed immediately)"
		}
		setAuditObject(ctx, id.String(), summary)
		out := &rotationStartOutput{}
		if result.Count > 0 {
			out.Body.JobID = result.FirstJobID.String()
			out.Body.JobCount = result.Count
		}
		return out, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusAccepted }, withAudit("rotateCredential", "credential"))

	huma.Get(hapi, "/api/v1/orgs/{org}/credentials/{id}", func(ctx context.Context, in *credentialGetInput) (*credentialDetailOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermCredentialsRead)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "credential id")
		if err != nil {
			return nil, err
		}
		c, err := deps.Credentials.Get(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapCredentialsErr(ctx, err)
		}
		usage, err := deps.Credentials.Usage(ctx, scope.OrgID)
		if err != nil {
			return nil, mapCredentialsErr(ctx, err)
		}
		return credentialDetail(deps, c, usage[c.ID], scope.Org.Slug), nil
	})

	huma.Patch(hapi, "/api/v1/orgs/{org}/credentials/{id}", func(ctx context.Context, in *credentialUpdateInput) (*credentialDetailOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermCredentialsWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "credential id")
		if err != nil {
			return nil, err
		}
		c, err := deps.Credentials.Update(ctx, scope.OrgID, id, credentials.UpdateInput{
			Name: in.Body.Name, Username: in.Body.Username, ExpiresAt: in.Body.ExpiresAt, Material: in.material().toMaterial(),
		})
		if err != nil {
			return nil, mapCredentialsErr(ctx, err)
		}
		usage, err := deps.Credentials.Usage(ctx, scope.OrgID)
		if err != nil {
			return nil, mapCredentialsErr(ctx, err)
		}
		setAuditObject(ctx, c.ID.String(), "updated credential "+c.Name)
		return credentialDetail(deps, c, usage[c.ID], scope.Org.Slug), nil
	}, withAudit("updateCredential", "credential"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/credentials/{id}", func(ctx context.Context, in *credentialGetInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermCredentialsWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "credential id")
		if err != nil {
			return nil, err
		}
		if err := deps.Credentials.Delete(ctx, scope.OrgID, id); err != nil {
			return nil, mapCredentialsErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "deleted credential")
		return &okOutput{}, nil
	}, withAudit("deleteCredential", "credential"))
}

func credentialDetail(deps Deps, c credentials.Credential, usage credentials.Usage, orgSlug string) *credentialDetailOutput {
	out := &credentialDetailOutput{}
	out.Body.Credential = toCredentialDTO(deps, c, usage, orgSlug)
	out.Body.Snippets = toSnippetsDTO(c, orgSlug)
	return out
}

func mapCredentialsErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, credentials.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, credentials.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, credentials.ErrNameTaken):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, credentials.ErrInUse):
		return huma.Error409Conflict("the credential is still used by a jump host or a host")
	case errors.Is(err, jobs.ErrHostKeyChanged):
		// A rotation target's host key changed: the rotation is refused.
		return huma.Error409Conflict(err.Error())
	default:
		slog.ErrorContext(ctx, "unmapped credentials error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
