package runrequests

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/templates"
)

// The services the resolver reads, each narrowed to what it uses.
type (
	TemplateSource interface {
		Get(ctx context.Context, orgID, id uuid.UUID) (templates.Template, error)
	}
	PlaybookSource interface {
		Get(ctx context.Context, orgID, id uuid.UUID) (playbooks.Playbook, error)
	}
	RepoSource interface {
		Get(ctx context.Context, orgID, id uuid.UUID) (repos.Repo, error)
	}
	JobRunner interface {
		RunTargets(ctx context.Context, orgID, templateID uuid.UUID) ([]uuid.UUID, error)
		RetryTargets(ctx context.Context, orgID, id uuid.UUID) (uuid.UUID, uuid.UUID, []uuid.UUID, error)
		EnqueueRunWith(ctx context.Context, orgID, templateID uuid.UUID, req jobs.RunRequest) ([]jobs.Job, error)
		RetryFailedInto(ctx context.Context, orgID, id uuid.UUID, actor *uuid.UUID, group uuid.UUID, followUps *[]uuid.UUID) ([]jobs.Job, error)
	}
	HostSource interface {
		ListHostsByIDs(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]hosts.Host, error)
	}
	SSHSource interface {
		HostSSH(ctx context.Context, orgID, hostID uuid.UUID) (sshconf.HostSSH, error)
		GetBastion(ctx context.Context, orgID, id uuid.UUID) (sshconf.Bastion, error)
	}
	CredentialSource interface {
		Get(ctx context.Context, orgID, id uuid.UUID) (credentials.Credential, error)
	}
	InventorySource interface {
		Preview(ctx context.Context, orgID uuid.UUID, scope render.Scope) (render.Inventory, error)
	}
)

// Sources is what NewResolver reads from.
type Sources struct {
	Templates   TemplateSource
	Playbooks   PlaybookSource
	Repos       RepoSource
	Jobs        JobRunner
	Hosts       HostSource
	SSH         SSHSource
	Credentials CredentialSource
	Inventory   InventorySource
}

// Resolver implements Pictures, Enqueuer and Validator over the existing
// domain services, so the manager and the API tests wire the same code.
type Resolver struct{ src Sources }

// NewResolver wires the resolver.
func NewResolver(src Sources) *Resolver { return &Resolver{src: src} }

// ValidateRun implements Validator: the template exists, and the one-host
// narrowing names one of its targets.
func (r *Resolver) ValidateRun(ctx context.Context, orgID, templateID uuid.UUID, onlyHost *uuid.UUID) (int64, error) {
	tmpl, err := r.src.Templates.Get(ctx, orgID, templateID)
	if err != nil {
		return 0, mapNotFound(err)
	}
	if onlyHost != nil {
		targets, err := r.src.Jobs.RunTargets(ctx, orgID, templateID)
		if err != nil {
			return 0, mapNotFound(err)
		}
		if !slices.Contains(targets, *onlyHost) {
			return 0, fmt.Errorf("%w: %s", ErrInvalidInput, jobs.ErrHostNotInScope)
		}
	}
	return tmpl.Revision, nil
}

// ValidateRetry implements Validator with the retry's own checks.
func (r *Resolver) ValidateRetry(ctx context.Context, orgID, jobID uuid.UUID) (uuid.UUID, int64, error) {
	templateID, _, _, err := r.src.Jobs.RetryTargets(ctx, orgID, jobID)
	if err != nil {
		if errors.Is(err, jobs.ErrNothingToRetry) || errors.Is(err, jobs.ErrNotRunning) || errors.Is(err, jobs.ErrInvalidInput) {
			return uuid.Nil, 0, fmt.Errorf("%w: %s", ErrInvalidInput, err.Error())
		}
		return uuid.Nil, 0, mapNotFound(err)
	}
	tmpl, err := r.src.Templates.Get(ctx, orgID, templateID)
	if err != nil {
		return uuid.Nil, 0, mapNotFound(err)
	}
	return templateID, tmpl.Revision, nil
}

// Run implements Enqueuer through the manual run path.
func (r *Resolver) Run(ctx context.Context, orgID, templateID uuid.UUID, onlyHost *uuid.UUID, actor uuid.UUID, key string, group uuid.UUID) error {
	_, err := r.src.Jobs.EnqueueRunWith(ctx, orgID, templateID, jobs.RunRequest{
		Trigger: jobs.TriggerManual, Actor: &actor, OnlyHost: onlyHost, IdempotencyKey: key, RunGroupID: group,
	})
	return err
}

// RetryFailed implements Enqueuer through the retry path.
func (r *Resolver) RetryFailed(ctx context.Context, orgID, jobID, actor, group uuid.UUID) error {
	_, err := r.src.Jobs.RetryFailedInto(ctx, orgID, jobID, &actor, group, nil)
	return err
}

// Picture implements Pictures: everything the run would use now.
func (r *Resolver) Picture(ctx context.Context, orgID uuid.UUID, req Request) (Picture, error) {
	tmpl, err := r.src.Templates.Get(ctx, orgID, req.TemplateID)
	if err != nil {
		return Picture{}, mapNotFound(err)
	}
	p := Picture{
		TemplateID: tmpl.ID.String(), TemplateName: tmpl.Name, TemplateRevision: tmpl.Revision, Limit: tmpl.Limit,
		ExtraVars: schema.MaskVars(tmpl.ExtraVars), VaultIdentities: []PictureVault{}, Targets: []PictureHost{},
	}
	if p.ExtraVars == nil {
		p.ExtraVars = map[string]any{}
	}
	if req.HostID != nil {
		p.OnlyHost = req.HostID.String()
	}
	if tmpl.CredentialID != nil {
		p.SSHCredential = r.credentialName(ctx, orgID, *tmpl.CredentialID)
	}
	var repoVault []repos.VaultIdentity
	if tmpl.PlaybookID != uuid.Nil {
		pb, err := r.src.Playbooks.Get(ctx, orgID, tmpl.PlaybookID)
		if err != nil {
			return Picture{}, mapNotFound(err)
		}
		p.Playbook = PicturePlaybook{ID: pb.ID.String(), Source: pb.Source}
		if pb.Path != nil {
			p.Playbook.Path = *pb.Path
		}
		if pb.Inline != nil {
			sum := sha256.Sum256([]byte(*pb.Inline))
			p.Playbook.InlineDigest = hex.EncodeToString(sum[:])
		}
		if pb.RepoID != nil {
			repo, err := r.src.Repos.Get(ctx, orgID, *pb.RepoID)
			if err != nil {
				return Picture{}, mapNotFound(err)
			}
			p.Repo = &PictureRepo{ID: repo.ID.String(), Name: repo.Name, URL: repo.URL, Branch: repo.Branch, Subpath: repo.Subpath}
			repoVault = repo.VaultIdentities
		}
	}
	templateVault := make([]repos.VaultIdentity, len(tmpl.VaultCredentials))
	for i, vc := range tmpl.VaultCredentials {
		templateVault[i] = vc.ToVaultIdentity()
	}
	for _, id := range repos.ResolveVaultIdentities(repoVault, templateVault) {
		p.VaultIdentities = append(p.VaultIdentities, PictureVault{Label: id.Label, Credential: r.credentialName(ctx, orgID, id.CredentialID)})
	}

	targets, err := r.targets(ctx, orgID, req, &p)
	if err != nil {
		return Picture{}, err
	}
	if err := r.describeHosts(ctx, orgID, targets, &p); err != nil {
		return Picture{}, err
	}
	digest, err := r.varsDigest(ctx, orgID, targets, tmpl.ExtraVars)
	if err != nil {
		return Picture{}, err
	}
	p.VarsDigest = digest
	return p, nil
}

func (r *Resolver) targets(ctx context.Context, orgID uuid.UUID, req Request, p *Picture) ([]uuid.UUID, error) {
	switch {
	case req.Kind == KindRetryFailed && req.JobID != nil:
		_, _, failed, err := r.src.Jobs.RetryTargets(ctx, orgID, *req.JobID)
		if err != nil {
			// A retry that can no longer run still shows; approving it fails
			// in the retry path itself.
			p.Retry = &PictureRetry{JobID: req.JobID.String(), FailedHosts: []string{}}
			return nil, nil
		}
		p.Retry = &PictureRetry{JobID: req.JobID.String()}
		for _, id := range failed {
			p.Retry.FailedHosts = append(p.Retry.FailedHosts, id.String())
		}
		return failed, nil
	case req.HostID != nil:
		return []uuid.UUID{*req.HostID}, nil
	default:
		ids, err := r.src.Jobs.RunTargets(ctx, orgID, req.TemplateID)
		if err != nil {
			return nil, mapNotFound(err)
		}
		return ids, nil
	}
}

func (r *Resolver) describeHosts(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID, p *Picture) error {
	if len(ids) == 0 {
		return nil
	}
	hs, err := r.src.Hosts.ListHostsByIDs(ctx, orgID, ids)
	if err != nil {
		return fmt.Errorf("list target hosts: %w", err)
	}
	for _, h := range hs {
		ph := PictureHost{ID: h.ID.String(), Name: h.Name, Address: h.Address}
		ssh, err := r.src.SSH.HostSSH(ctx, orgID, h.ID)
		if err == nil {
			ph.SSHUser, ph.SSHPort = ssh.Username, ssh.Port
			if ssh.CredentialID != nil {
				ph.Credential = r.credentialName(ctx, orgID, *ssh.CredentialID)
			}
			if ssh.BastionID != nil {
				if b, err := r.src.SSH.GetBastion(ctx, orgID, *ssh.BastionID); err == nil {
					ph.JumpHost = b.Name
				} else {
					ph.JumpHost = ssh.BastionID.String()
				}
			}
			ph.JumpHost += bastionModeSuffix(ssh)
		}
		p.Targets = append(p.Targets, ph)
	}
	return nil
}

// bastionModeSuffix keeps the bastion mode in the picture, so switching it
// changes the fingerprint even when no bastion id is involved.
func bastionModeSuffix(ssh sshconf.HostSSH) string {
	if ssh.BastionMode == "" {
		return ""
	}
	return " (" + string(ssh.BastionMode) + ")"
}

// varsDigest hashes the inventory the targets would be rendered with (group
// and membership vars, parents included) and the template's stored extra
// vars, so any change to them changes the fingerprint without the page
// listing them.
func (r *Resolver) varsDigest(ctx context.Context, orgID uuid.UUID, targets []uuid.UUID, extraVars map[string]any) (string, error) {
	var inv any
	if len(targets) > 0 {
		rendered, err := r.src.Inventory.Preview(ctx, orgID, render.Scope{HostIDs: targets})
		if err != nil {
			return "", fmt.Errorf("render target inventory: %w", err)
		}
		inv = rendered
	}
	doc, err := json.Marshal(struct {
		Inventory any            `json:"inventory"`
		ExtraVars map[string]any `json:"extra_vars"`
	}{inv, extraVars})
	if err != nil {
		return "", fmt.Errorf("encode vars digest: %w", err)
	}
	sum := sha256.Sum256(doc)
	return hex.EncodeToString(sum[:]), nil
}

func (r *Resolver) credentialName(ctx context.Context, orgID, id uuid.UUID) string {
	c, err := r.src.Credentials.Get(ctx, orgID, id)
	if err != nil {
		return id.String()
	}
	return c.Name
}

func mapNotFound(err error) error {
	if errors.Is(err, templates.ErrNotFound) || errors.Is(err, jobs.ErrNotFound) || errors.Is(err, playbooks.ErrNotFound) ||
		errors.Is(err, repos.ErrNotFound) || errors.Is(err, hosts.ErrNotFound) {
		return fmt.Errorf("%w: %s", ErrNotFound, err.Error())
	}
	return err
}
