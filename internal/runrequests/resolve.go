package runrequests

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/templates"
)

// Resolver implements Pictures, Enqueuer, Validator, Syncer and Automation over the existing
// domain services, so the manager and the API tests wire the same code.
type Resolver struct{ src Sources }

// NewResolver wires the resolver.
func NewResolver(src Sources) *Resolver { return &Resolver{src: src} }

// ValidateRun implements Validator: the template exists, and a run of it
// started now would reach a host: its scope under its own limit selects
// one, and the one-host narrowing names a host of those (jobs.CheckRunScope,
// the enqueue's own check). The approval checks again: things can change
// in between. Overrides, when given, pass checkOverrides, and every host
// they narrow to is one of those targets.
func (r *Resolver) ValidateRun(ctx context.Context, orgID, templateID uuid.UUID, onlyHost *uuid.UUID, ov *Overrides) (int64, error) {
	tmpl, err := r.src.Templates.Get(ctx, orgID, templateID)
	if err != nil {
		return 0, mapNotFound(err)
	}
	if err := r.src.Jobs.CheckRunScope(ctx, orgID, templateID, onlyHost); err != nil {
		switch {
		case errors.Is(err, jobs.ErrEmptyScope):
			return 0, fmt.Errorf("%w: the template's scope matches no host", ErrInvalidInput)
		case errors.Is(err, jobs.ErrHostNotInScope):
			return 0, fmt.Errorf("%w: %s", ErrInvalidInput, jobs.ErrHostNotInScope)
		}
		return 0, mapNotFound(err)
	}
	if ov == nil {
		return tmpl.Revision, nil
	}
	if err := r.checkOverrides(ctx, orgID, tmpl, ov); err != nil {
		return 0, err
	}
	targets, err := r.src.Jobs.LimitedRunTargets(ctx, orgID, templateID)
	if err != nil {
		return 0, mapNotFound(err)
	}
	for _, h := range ov.HostIDs {
		if !slices.Contains(targets, h) {
			return 0, fmt.Errorf("%w: host %s is not among the template's targets", ErrInvalidInput, h)
		}
	}
	if onlyHost != nil {
		targets = []uuid.UUID{*onlyHost}
	}
	kept, err := r.narrowTargets(ctx, orgID, ov, targets)
	if err != nil {
		return 0, err
	}
	if len(kept) == 0 {
		return 0, errNoHost
	}
	if err := r.checkSecretShadows(ctx, orgID, tmpl, ov, kept); err != nil {
		return 0, err
	}
	return tmpl.Revision, nil
}

// ValidateRetry implements Validator with the retry's own checks; it also
// returns the retried job's run group.
func (r *Resolver) ValidateRetry(ctx context.Context, orgID, jobID uuid.UUID) (uuid.UUID, uuid.UUID, int64, error) {
	templateID, group, _, err := r.src.Jobs.RetryTargets(ctx, orgID, jobID)
	if err != nil {
		if errors.Is(err, jobs.ErrNothingToRetry) || errors.Is(err, jobs.ErrNotRunning) || errors.Is(err, jobs.ErrInvalidInput) {
			return uuid.Nil, uuid.Nil, 0, fmt.Errorf("%w: %s", ErrInvalidInput, err.Error())
		}
		return uuid.Nil, uuid.Nil, 0, mapNotFound(err)
	}
	tmpl, err := r.src.Templates.Get(ctx, orgID, templateID)
	if err != nil {
		return uuid.Nil, uuid.Nil, 0, mapNotFound(err)
	}
	return templateID, group, tmpl.Revision, nil
}

// Run implements Enqueuer through the manual run path. The follow-ups the
// group holds unstarted are cleared first; followUps is then the whole list
// the run plans (empty, never nil, so nothing is inherited). A request's
// overrides are checked again (runOverrides) and travel as the run's
// jobs.RunOverrides; a dry run plans no follow-ups at all.
func (r *Resolver) Run(ctx context.Context, orgID uuid.UUID, req Request, actor uuid.UUID, key string, group uuid.UUID, followUps []uuid.UUID) error {
	run := jobs.RunRequest{Trigger: jobs.TriggerManual, Actor: &actor, OnlyHost: req.HostID, IdempotencyKey: key, RunGroupID: group,
		PinnedCommit: req.PinnedCommit, EnrollHostIDs: req.EnrollHostIDs, RefuseCoveredZoneGrowth: true}
	if req.Overrides != nil {
		// The run keeps to the picture's targets, its one host included.
		run.OnlyHost = nil
		var err error
		if run.Overrides, run.OnlyHosts, err = r.runOverrides(ctx, orgID, req); err != nil {
			return err
		}
	}
	if err := r.src.FollowUpRuns.ClearUnstarted(ctx, orgID, group); err != nil {
		return fmt.Errorf("clear unstarted follow-ups: %w", err)
	}
	if req.Overrides == nil || !req.Overrides.Check {
		list := nonNil(followUps)
		run.FollowUps = &list
	}
	_, err := r.src.Jobs.EnqueueRunWith(ctx, orgID, req.TemplateID, run)
	return r.nameGrowth(ctx, orgID, err)
}

// nameGrowth names the zones and hosts of a replay the queue refused
// because a covered zone grew (backlog 2.142); any other error passes.
func (r *Resolver) nameGrowth(ctx context.Context, orgID uuid.UUID, err error) error {
	var grew *jobs.CoveredZoneGrowthError
	if errors.As(err, &grew) {
		return r.namedGrowth(ctx, orgID, grew)
	}
	return err
}

// RetryFailed implements Enqueuer through the retry path, with Run's
// follow-up handling and idempotency key; approved is what the approval
// stored (its commit pins a retry of a job that has no pin of its own).
// The extra vars the retry repeats are checked again (RetryRunOverrides).
func (r *Resolver) RetryFailed(ctx context.Context, orgID uuid.UUID, req Request, actor uuid.UUID, key string, group uuid.UUID, followUps []uuid.UUID, approved jobs.ApprovedRun) error {
	ov, err := r.RetryRunOverrides(ctx, orgID, *req.JobID, req.Overrides)
	if err != nil {
		return err
	}
	if err := r.src.FollowUpRuns.ClearUnstarted(ctx, orgID, group); err != nil {
		return fmt.Errorf("clear unstarted follow-ups: %w", err)
	}
	list := nonNil(followUps)
	_, err = r.src.Jobs.RetryFailedInto(ctx, orgID, *req.JobID, &actor, key, group, &list, approved, ov)
	return r.nameGrowth(ctx, orgID, err)
}

// Picture implements Pictures: everything the run would use now.
// A schedule request's picture is the schedule's (schedulePicture), a
// trigger request's the trigger's (triggerPicture); a kind this version
// does not know is refused.
func (r *Resolver) Picture(ctx context.Context, orgID uuid.UUID, req Request) (Picture, error) {
	switch req.Kind {
	case KindSchedule:
		return r.schedulePicture(ctx, orgID, req)
	case KindTrigger:
		return r.triggerPicture(ctx, orgID, req)
	case KindScheduleReview, KindTriggerReview:
		return r.reviewPicture(ctx, orgID, req)
	case KindRun, KindRetryFailed:
	default:
		// A kind a newer version wrote is no run: never shown as one.
		return Picture{}, fmt.Errorf("%w: requests of kind %q are not known to this version", ErrInvalidInput, req.Kind)
	}
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
	pictureOverrides(req.Overrides, &p)
	if tmpl.CredentialID != nil {
		p.SSHCredentialID, p.SSHCredential = tmpl.CredentialID.String(), r.credentialName(ctx, orgID, *tmpl.CredentialID)
	}
	var repoVault []repos.VaultIdentity
	var repo *repos.Repo
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
			got, err := r.src.Repos.Get(ctx, orgID, *pb.RepoID)
			if err != nil {
				return Picture{}, mapNotFound(err)
			}
			repo = &got
			p.Repo = &PictureRepo{ID: repo.ID.String(), Name: repo.Name, URL: repo.URL, Branch: repo.Branch, Subpath: repo.Subpath}
			if repo.CredentialID != nil {
				p.Repo.GitCredentialID, p.Repo.GitCredential = repo.CredentialID.String(), r.credentialName(ctx, orgID, *repo.CredentialID)
			}
			repoVault = repo.VaultIdentities
		}
	}
	templateVault := make([]repos.VaultIdentity, len(tmpl.VaultCredentials))
	for i, vc := range tmpl.VaultCredentials {
		templateVault[i] = vc.ToVaultIdentity()
	}
	for _, id := range repos.ResolveVaultIdentities(repoVault, templateVault) {
		p.VaultIdentities = append(p.VaultIdentities, PictureVault{
			Label: id.Label, CredentialID: id.CredentialID.String(), Credential: r.credentialName(ctx, orgID, id.CredentialID),
		})
	}

	if repo != nil || req.Kind == KindRetryFailed {
		if p.Commit, err = r.commit(ctx, orgID, req, repo); err != nil {
			return Picture{}, err
		}
	}
	retry := r.retryOf(ctx, orgID, req)
	targets, err := r.targets(ctx, orgID, req, retry, &p)
	if err != nil {
		return Picture{}, err
	}
	if err := r.describeHosts(ctx, orgID, targets, &p); err != nil {
		return Picture{}, err
	}
	if tmpl.AgentEnrollment {
		if p.AgentEnrollment, err = r.agentEnrollment(ctx, orgID, req, p); err != nil {
			return Picture{}, err
		}
	}
	digest, err := r.varsDigest(ctx, orgID, req.ID, tmpl.ID, targets)
	if err != nil {
		return Picture{}, err
	}
	p.VarsDigest = digest
	if p.CheckMode {
		// A dry run starts no follow-ups (jobs.checkRunRequest).
		return p, nil
	}
	var pinRepo *uuid.UUID
	if p.Commit != nil && repo != nil {
		pinRepo = &repo.ID
	}
	if p.FollowUps, err = r.followUps(ctx, orgID, tmpl, retry, pinRepo); err != nil {
		return Picture{}, err
	}
	return p, nil
}

// retryLookup is what one RetryTargets call told a retry's picture: the
// run group the retry copies its follow-ups from and the failed hosts it
// targets, or the error that says it can no longer run.
type retryLookup struct {
	jobID  uuid.UUID
	group  uuid.UUID
	failed []uuid.UUID
	err    error
}

// retryOf looks a retry request's job up once per picture, for both its
// targets and its follow-up roots; nil for a request that is no retry.
func (r *Resolver) retryOf(ctx context.Context, orgID uuid.UUID, req Request) *retryLookup {
	if req.Kind != KindRetryFailed || req.JobID == nil {
		return nil
	}
	_, group, failed, err := r.src.Jobs.RetryTargets(ctx, orgID, *req.JobID)
	return &retryLookup{jobID: *req.JobID, group: group, failed: failed, err: err}
}

func (r *Resolver) targets(ctx context.Context, orgID uuid.UUID, req Request, retry *retryLookup, p *Picture) ([]uuid.UUID, error) {
	switch {
	case retry != nil:
		if retry.err != nil {
			// A retry that can no longer run still shows; approving it fails
			// in the retry path itself.
			p.Retry = &PictureRetry{JobID: retry.jobID.String(), FailedHosts: []string{}}
			return nil, nil
		}
		p.Retry = &PictureRetry{JobID: retry.jobID.String()}
		for _, id := range retry.failed {
			p.Retry.FailedHosts = append(p.Retry.FailedHosts, id.String())
		}
		return retry.failed, nil
	default:
		return r.runTargets(ctx, orgID, req)
	}
}

// runTargets is the hosts a run request's run reaches: its one host, or
// the template's scope under its own limit (LimitedRunTargets), narrowed
// by the request's overrides (narrowTargets) when it has any.
func (r *Resolver) runTargets(ctx context.Context, orgID uuid.UUID, req Request) ([]uuid.UUID, error) {
	ids := []uuid.UUID{}
	if req.HostID != nil {
		ids = append(ids, *req.HostID)
	} else {
		var err error
		if ids, err = r.src.Jobs.LimitedRunTargets(ctx, orgID, req.TemplateID); err != nil {
			return nil, mapNotFound(err)
		}
	}
	if req.Overrides == nil {
		return ids, nil
	}
	return r.narrowTargets(ctx, orgID, req.Overrides, ids)
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
				ph.CredentialID, ph.Credential = ssh.CredentialID.String(), r.credentialName(ctx, orgID, *ssh.CredentialID)
			}
			if ssh.BastionID != nil {
				ph.JumpHostID = ssh.BastionID.String()
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
