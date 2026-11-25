package runrequests

import (
	"context"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/chain"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/triggers"
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
		// LimitedRunTargets is the template's scope under its own limit,
		// what the run is enqueued for; empty when the limit leaves nothing.
		LimitedRunTargets(ctx context.Context, orgID, templateID uuid.UUID) ([]uuid.UUID, error)
		CheckRunScope(ctx context.Context, orgID, templateID uuid.UUID, onlyHost *uuid.UUID) error
		RetryTargets(ctx context.Context, orgID, id uuid.UUID) (uuid.UUID, uuid.UUID, []uuid.UUID, error)
		EnqueueRunWith(ctx context.Context, orgID, templateID uuid.UUID, req jobs.RunRequest) ([]jobs.Job, error)
		RetryFailedInto(ctx context.Context, orgID, id uuid.UUID, actor *uuid.UUID, idempotencyKey string, group uuid.UUID, followUps *[]uuid.UUID, approved jobs.ApprovedRun, overrides *jobs.RunOverrides) ([]jobs.Job, error)
	}
	// CommitSource is the commit of a repository's last successful sync
	// and why a run cannot be pinned to it (repos.Service).
	CommitSource interface {
		SyncedCommit(ctx context.Context, orgID uuid.UUID, repo repos.Repo) (repos.SyncedCommit, error)
	}
	// SyncJobSource is what the queue says about a repository's syncs and
	// a job's pin (jobs.Service).
	SyncJobSource interface {
		HasActiveSync(ctx context.Context, orgID, repoID uuid.UUID) (bool, error)
		PinnedCommit(ctx context.Context, orgID, jobID uuid.UUID) (jobs.Pin, error)
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
	// FollowUpSettings is the org's switch for starting follow-ups.
	FollowUpSettings interface {
		StartsFollowUps(ctx context.Context, orgID uuid.UUID) (bool, error)
	}
	// OverrideValidator checks per-run overrides against a template the way
	// schedules and webhook triggers are checked (templates.Service).
	OverrideValidator interface {
		ValidateOverrides(ctx context.Context, orgID, templateID uuid.UUID, in templates.Overrides, opt templates.OverrideOptions) (templates.Overrides, error)
	}
	// FollowUpRunSource reads and clears the follow-up rows of a run group.
	FollowUpRunSource interface {
		ListForGroup(ctx context.Context, orgID, groupID uuid.UUID) ([]chain.FollowUp, error)
		ClearUnstarted(ctx context.Context, orgID, groupID uuid.UUID) error
	}
	// InventorySecretSource names the variables the inventory holds a
	// secret under for some hosts (inventory.Service).
	InventorySecretSource interface {
		SecretVarNames(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (map[string]bool, error)
	}
	// AgentSource names the hosts that run an agent (agents.Service): a
	// run of a template with agent enrollment enrols the others.
	AgentSource interface {
		HostsWithAgent(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (map[uuid.UUID]bool, error)
	}
	// PlaybookVarSource is a playbook's recorded variable source, the
	// variables it declares (playbooks.Service).
	PlaybookVarSource interface {
		VarSource(ctx context.Context, orgID, playbookID uuid.UUID) (playbooks.VarSource, error)
	}
	// ScheduleSource reads, checks and writes schedules for schedule
	// requests (schedules.Service).
	ScheduleSource interface {
		Get(ctx context.Context, orgID, id uuid.UUID) (schedules.Schedule, error)
		Validate(ctx context.Context, orgID, id uuid.UUID, in schedules.Input) error
		CreateAs(ctx context.Context, orgID, id, actor uuid.UUID, in schedules.Input) (schedules.Schedule, error)
		Update(ctx context.Context, orgID, id uuid.UUID, revision int64, in schedules.Input) (schedules.Schedule, error)
		ResumeAt(ctx context.Context, orgID, id uuid.UUID, revision int64) (schedules.Schedule, error)
	}
	// TriggerSource reads, checks and writes webhook triggers for trigger
	// requests (triggers.Service). Its writes return the new secret, which
	// the resolver hands to the approval and nowhere else.
	TriggerSource interface {
		Get(ctx context.Context, orgID, id uuid.UUID) (triggers.Trigger, error)
		Validate(ctx context.Context, orgID, id uuid.UUID, in triggers.Input) (triggers.Trigger, error)
		CreateAs(ctx context.Context, orgID, id, actor uuid.UUID, in triggers.Input) (triggers.Created, error)
		Update(ctx context.Context, orgID, id uuid.UUID, revision int64, in triggers.Input) (triggers.Trigger, error)
		RotateSecretAt(ctx context.Context, orgID, id uuid.UUID, revision int64) (triggers.Created, error)
		// EnableAt enables a trigger at the revision a person saw (an
		// approved review).
		EnableAt(ctx context.Context, orgID, id uuid.UUID, revision int64) (triggers.Trigger, error)
		URL(t triggers.Trigger) string
	}
	// RunVarsSource reads the variable values a run would receive, secrets
	// in the clear. The resolver feeds them only into the keyed vars
	// digest; they never enter a picture.
	RunVarsSource interface {
		// InventoryVars is the targets' rendered inventory, secrets
		// revealed, as the claim renders it: the document with its group
		// and membership vars, and how each host is reached.
		InventoryVars(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (render.Inventory, error)
		// ExtraVars is the template's extra vars as a run receives them,
		// secrets opened.
		ExtraVars(ctx context.Context, orgID, templateID uuid.UUID) (map[string]any, error)
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
	Vars        RunVarsSource
	// VarsKey keys the vars digest (VarsDigestKey): server-side only, so
	// the digest the browser sees cannot be checked against a guess.
	VarsKey      []byte
	Settings     FollowUpSettings
	FollowUpRuns FollowUpRunSource
	// Overrides checks a run request's limit and extra vars.
	Overrides OverrideValidator
	// Commits and SyncJobs give a repository playbook's picture its
	// commit (spec 2026-11-25 A, D3).
	Commits  CommitSource
	SyncJobs SyncJobSource
	// RepoSyncs starts the sync a sync_first request waits for.
	RepoSyncs RepoSyncer
	// InventorySecrets and PlaybookVars name the secrets a run's other
	// variable sources hold, which a request's extra vars may not replace
	// (checkSecretShadows).
	InventorySecrets InventorySecretSource
	PlaybookVars     PlaybookVarSource
	// Agents gives a template with agent enrollment its picture's hosts
	// (spec 2026-11-25 B).
	Agents AgentSource
	// Schedules checks, pictures and applies schedule requests (spec
	// 2026-11-25 C).
	Schedules ScheduleSource
	// Triggers checks, pictures and applies trigger requests (spec
	// 2026-11-25 C).
	Triggers TriggerSource
}
