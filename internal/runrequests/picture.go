package runrequests

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/google/uuid"
)

// Picture is what a run request would use if it were approved now (spec
// 2026-11-16 assistant access, "Run requests"): the approval page shows it,
// and the approval's fingerprint covers it. It never carries a secret:
// credentials and jump hosts appear by id and name (the name for the page,
// the id so a swap for another of the same name changes the fingerprint),
// extra vars with their secrets masked, and the real values of the group,
// membership and extra vars only as the keyed VarsDigest.
type Picture struct {
	TemplateID       string          `json:"template_id"`
	TemplateName     string          `json:"template_name"`
	TemplateRevision int64           `json:"template_revision"`
	Limit            string          `json:"limit"`
	OnlyHost         string          `json:"only_host,omitempty"`
	Playbook         PicturePlaybook `json:"playbook"`
	Repo             *PictureRepo    `json:"repo,omitempty"`
	Targets          []PictureHost   `json:"targets"`
	SSHCredential    string          `json:"ssh_credential,omitempty"`
	SSHCredentialID  string          `json:"ssh_credential_id,omitempty"`
	VaultIdentities  []PictureVault  `json:"vault_identities"`
	ExtraVars        map[string]any  `json:"extra_vars"`
	Retry            *PictureRetry   `json:"retry,omitempty"`
	// VarsDigest is the keyed HMAC of the values the run receives (see
	// Resolver.varsDigest): it changes with any of them, secrets
	// included, and reveals none.
	VarsDigest string `json:"vars_digest"`
	// FollowUps is nil when the request has no direct follow-ups, so the
	// canonical JSON of such a picture is unchanged. A dry run starts none,
	// so its picture has none either.
	FollowUps *PictureFollowUps `json:"follow_ups,omitempty"`

	// The request's overrides (Overrides, spec 2026-11-25 A). Each is empty
	// for a request without them, so the canonical JSON of a picture of a
	// request made before they existed is unchanged and so is its
	// fingerprint. CheckMode makes the run a dry run, Diff shows its
	// changes; LimitOverride is the request's limit, which narrows Targets
	// (Limit applies on top at the run); ExtraVarsOverride replaces the
	// template's top-level extra vars of the same name and is shown by
	// value (it never holds a secret; the run gets it marked unsafe for
	// Ansible); RequestedHostIDs are the host ids the request asked for.
	// Targets are always exactly the hosts the run is kept to.
	CheckMode         bool           `json:"check_mode,omitempty"`
	Diff              bool           `json:"diff,omitempty"`
	LimitOverride     string         `json:"limit_override,omitempty"`
	ExtraVarsOverride map[string]any `json:"extra_vars_override,omitempty"`
	RequestedHostIDs  []string       `json:"requested_host_ids,omitempty"`

	// Commit is the commit the run runs (spec 2026-11-25 A, decision D3):
	// nil when the playbook comes from no repository, and for a request
	// approved before pins existed, so the canonical JSON of its picture
	// is unchanged and its approval still matches at the claim.
	Commit *PictureCommit `json:"commit,omitempty"`

	// AgentEnrollment is set for a template with agent enrollment (spec
	// 2026-11-25 B): the hosts the run enrols the agent on. nil for every
	// other template, so the canonical JSON of its picture is unchanged.
	AgentEnrollment *PictureAgentEnrollment `json:"agent_enrollment,omitempty" doc:"Set when the template enrols agents: the hosts the run enrols the agent on"`

	// Schedule is the picture of a schedule request (spec 2026-11-25 C):
	// the schedule as it will be and as it is. A schedule request's
	// picture names its template (TemplateID, TemplateName,
	// TemplateRevision) and leaves every run field empty; nil for a run or
	// a retry, so the canonical JSON of their pictures is unchanged.
	Schedule *PictureSchedule `json:"schedule,omitempty" doc:"Set for a schedule request: the schedule as the approval stores it and as it is now"`

	// Trigger is the picture of a trigger request (spec 2026-11-25 C): the
	// webhook trigger as it will be and as it is, never its secret. A
	// template run trigger's picture names its template like a schedule
	// request's; a repository sync trigger's names none (the repository
	// is in the trigger's states). nil for every other kind.
	Trigger *PictureTrigger `json:"trigger,omitempty" doc:"Set for a trigger request: the webhook trigger as the approval stores it and as it is now; never its secret"`
}

// Why a run of a template with agent enrollment enrols no host
// (PictureAgentEnrollment.NoneReason): a dry run enrols nothing; an
// approval that stored no list (the template enrolled no agents when it
// was approved, or it was approved before the list existed) enrols
// nothing either.
const (
	EnrollNoneDryRun         = "dry_run"
	EnrollNoneApprovedBefore = "approved_before_enrollment"
)

// PictureAgentEnrollment is what a run of a template with agent enrollment
// enrols. While the request is pending it is live: the targets that have
// no agent now (a host enrolled meanwhile changes the picture). An
// approval stores the list on the request and the run's jobs; from then on
// the picture shows the stored list, so an agent enrolled after the
// approval does not fail the claim.
type PictureAgentEnrollment struct {
	// Hosts are the hosts' names in the order of HostIDs, by name; a
	// stored host that is no longer a target shows by id.
	Hosts   []string `json:"hosts" doc:"The names of the hosts the run enrols the agent on: the targets without an agent"`
	HostIDs []string `json:"host_ids" doc:"The ids of those hosts, in the same order"`
	// NoneReason says why the run enrols no host although the template
	// enrols agents: EnrollNoneDryRun or EnrollNoneApprovedBefore. Empty
	// otherwise.
	NoneReason string `json:"none_reason,omitempty" enum:"dry_run,approved_before_enrollment" doc:"Why the run enrols nothing: dry_run, a dry run mints no enrollment token; approved_before_enrollment, the request was approved before the template enrolled agents"`
}

// BlockedSyncRunning is PictureCommit.Blocked's reason for a sync of the
// repository that is queued or running: the commit it fetches may differ.
// The other reasons are repos.SyncedCommit's Stale values.
const BlockedSyncRunning = "sync_running"

// BlockedRepoMoved is PictureCommit.Blocked's reason for a retry of a
// pinned run whose template's playbook now comes from another repository,
// or from none: the commit the retry would run again is not in it.
const BlockedRepoMoved = "repo_moved"

// PictureCommit is the commit a run of a repository playbook runs. While
// the request is pending it is live: the commit of the repository's last
// successful sync (a newer sync changes the picture). An approval stores
// it on the request and the run's jobs; from then on the picture shows
// the stored commit. A retry shows its original job's commit when that
// job was pinned.
type PictureCommit struct {
	// SHA is the full commit id; empty when no sync recorded one.
	SHA string `json:"sha,omitempty" doc:"The full commit id; absent when no sync recorded one"`
	// Blocked says why an approval is refused now: one of the
	// repos.Commit* reasons (sync the repository first) and
	// BlockedSyncRunning, in that order. Empty when the commit can be
	// pinned.
	Blocked []string `json:"blocked,omitempty" enum:"never_synced,not_reported,sync_unrecorded,repo_changed,sync_running,repo_moved" doc:"Why the request cannot be approved now: the repository needs a sync first, a sync of it is queued or running, or (a retry) the template no longer runs the repository the original run's commit belongs to. Absent when the commit can be pinned"`
	// Source is CommitFromOriginalRun for a retry that runs its original
	// job's commit again; empty for the repository's last sync (or, once
	// approved, the commit the approval stored).
	Source string `json:"source,omitempty" enum:"original_run" doc:"original_run when a retry runs the commit its original run used; absent otherwise"`
}

// CommitFromOriginalRun is PictureCommit.Source for a retry of a pinned
// job.
const CommitFromOriginalRun = "original_run"

// PictureFollowUps is what an approval does about the template's follow-ups.
type PictureFollowUps struct {
	// Start is the organization setting: true when an approval starts them.
	Start bool `json:"start"`
	// Templates are the direct follow-ups in start order.
	Templates []PictureFollowUp `json:"templates"`
	// Truncated is set when the tree had more than MaxPictureFollowUps nodes;
	// an approval can then start none of them.
	Truncated bool `json:"truncated,omitempty"`
}

// PictureFollowUp is one follow-up template and, when Start is set, the
// follow-ups its own run would start.
type PictureFollowUp struct {
	TemplateID       string `json:"template_id"`
	TemplateName     string `json:"template_name"`
	TemplateRevision int64  `json:"template_revision,omitempty"`
	// Stop says why this follow-up would not start: "deleted" (the template
	// is gone, retry only), "in_chain" (already on the chain's path),
	// "too_deep" (past the chain's depth limit).
	Stop string `json:"stop,omitempty"`
	// Commit says what the follow-up runs when the requested run is pinned
	// to a commit (the picture has a Commit; empty otherwise, so the JSON
	// of an unpinned picture is unchanged): FollowUpPinned, the same commit
	// (its playbook comes from the same repository, below pinned runs
	// only); FollowUpCurrent, the state of Repo when it starts;
	// FollowUpEditor, its editor playbook as saved when it starts.
	Commit    string            `json:"commit,omitempty" enum:"pinned,current,editor"`
	Repo      string            `json:"repo,omitempty"`
	FollowUps []PictureFollowUp `json:"follow_ups,omitempty"`
}

// What a follow-up of a pinned run runs (PictureFollowUp.Commit).
const (
	FollowUpPinned  = "pinned"
	FollowUpCurrent = "current"
	FollowUpEditor  = "editor"
)

// PicturePlaybook names the playbook a run uses.
type PicturePlaybook struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Path   string `json:"path,omitempty"`
	// InlineDigest is the SHA-256 of an editor playbook's YAML, so an edit
	// changes the fingerprint without the page having to show the YAML.
	InlineDigest string `json:"inline_digest,omitempty"`
}

// PictureRepo is the repository a repository playbook comes from, with the
// credential its checkout fetches with, if any.
type PictureRepo struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	URL             string `json:"url"`
	Branch          string `json:"branch"`
	Subpath         string `json:"subpath"`
	GitCredentialID string `json:"git_credential_id,omitempty"`
	GitCredential   string `json:"git_credential,omitempty"`
}

// PictureHost is one target host as the run would reach it.
type PictureHost struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Address      string `json:"address"`
	SSHUser      string `json:"ssh_user,omitempty"`
	SSHPort      int    `json:"ssh_port,omitempty"`
	JumpHost     string `json:"jump_host,omitempty"`
	JumpHostID   string `json:"jump_host_id,omitempty"`
	Credential   string `json:"credential,omitempty"`
	CredentialID string `json:"credential_id,omitempty"`
}

// PictureVault is one vault identity, by label and credential id and name.
type PictureVault struct {
	Label        string `json:"label"`
	CredentialID string `json:"credential_id"`
	Credential   string `json:"credential"`
}

// PictureRetry is what a retry request would run again.
type PictureRetry struct {
	JobID       string   `json:"job_id"`
	FailedHosts []string `json:"failed_hosts"`
}

// Fingerprint is the SHA-256 (hex) over the request id and the canonical
// JSON of p. encoding/json writes struct fields in declaration order and
// map keys sorted, so the only order left to fix is the slices': targets by
// id, vault identities by label then credential id then name, failed
// hosts, the requested hosts and the enrolled hosts' names and ids as
// given sorted.
func Fingerprint(requestID uuid.UUID, p Picture) string {
	p.Targets = slices.Clone(p.Targets)
	slices.SortFunc(p.Targets, func(a, b PictureHost) int { return cmp.Compare(a.ID, b.ID) })
	p.VaultIdentities = slices.Clone(p.VaultIdentities)
	slices.SortFunc(p.VaultIdentities, func(a, b PictureVault) int {
		return cmp.Or(cmp.Compare(a.Label, b.Label), cmp.Compare(a.CredentialID, b.CredentialID), cmp.Compare(a.Credential, b.Credential))
	})
	p.RequestedHostIDs = slices.Clone(p.RequestedHostIDs)
	slices.Sort(p.RequestedHostIDs)
	if p.AgentEnrollment != nil {
		e := *p.AgentEnrollment
		e.Hosts, e.HostIDs = slices.Clone(e.Hosts), slices.Clone(e.HostIDs)
		slices.Sort(e.Hosts)
		slices.Sort(e.HostIDs)
		p.AgentEnrollment = &e
	}
	if p.Retry != nil {
		r := *p.Retry
		r.FailedHosts = slices.Clone(r.FailedHosts)
		slices.Sort(r.FailedHosts)
		p.Retry = &r
	}
	doc, err := json.Marshal(struct {
		Request uuid.UUID `json:"request"`
		Picture Picture   `json:"picture"`
	}{requestID, p})
	if err != nil {
		// Picture holds only strings, numbers, slices of those and a
		// decoded JSON document; Marshal cannot fail on it.
		panic("fingerprint: " + err.Error())
	}
	sum := sha256.Sum256(doc)
	return hex.EncodeToString(sum[:])
}
