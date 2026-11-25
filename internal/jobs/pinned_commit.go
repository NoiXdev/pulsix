// Pinned commits (spec 2026-11-25 assistant API gaps, A, decision D3): an
// approved run request runs exactly the commit its approver saw, the
// commit of the repository's last successful sync, instead of the branch
// tip. The approval passes the commit as RunRequest.PinnedCommit; every
// job of the run (and of its retries) stores it, the claim hands such a
// job only to a worker that announced ClaimCaps.SupportsPinnedCommit and
// ships the commit in the repository payload, and the worker checks it
// out after its fetch.
package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"
)

// commitPattern is a full commit id, SHA-1 or SHA-256, as git prints it.
var commitPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// checkPin refuses a pin the run cannot honour: one that is no full commit
// id, one on a run nobody approved (only an approval pins; schedules,
// webhooks and chained runs run the branch tip), and one on a template
// whose playbook comes from no repository.
func checkPin(tmpl TemplateRow, req RunRequest) error {
	if req.PinnedCommit == "" {
		return nil
	}
	if !commitPattern.MatchString(req.PinnedCommit) {
		return fmt.Errorf("%w: %q is not a full commit id", ErrInvalidInput, req.PinnedCommit)
	}
	if req.Trigger != TriggerManual && req.Trigger != TriggerChain {
		return fmt.Errorf("%w: only an approved run and its follow-ups are pinned to a commit, not a %s run", ErrInvalidInput, req.Trigger)
	}
	if tmpl.PlaybookRepo == nil {
		return fmt.Errorf("%w: the template's playbook comes from no repository, so there is no commit to pin", ErrInvalidInput)
	}
	return nil
}

// chainPin is the commit a chained run inherits (controller ruling, spec
// 2026-11-25 A): its parent run group's pin when the parent ran a playbook
// of the same repository as tmpl, so a follow-up of an approved run runs
// the code its approver saw. A follow-up of another repository, an editor
// playbook, or a parent without a pin is not pinned, as before. The
// parent's repository is its template's as it is now; a parent template
// that is gone pins nothing.
func (s *Service) chainPin(ctx context.Context, orgID uuid.UUID, tmpl TemplateRow, req RunRequest) (string, error) {
	if req.Trigger != TriggerChain || req.TriggerRef == nil || req.PinnedCommit != "" || tmpl.PlaybookRepo == nil {
		return req.PinnedCommit, nil
	}
	parents, _, err := s.store.ListJobs(ctx, orgID, Filter{RunGroupID: req.TriggerRef}, "", replayListLimit)
	if err != nil {
		return "", fmt.Errorf("list the parent run: %w", err)
	}
	for _, p := range parents {
		if p.PinnedCommit == "" || p.TemplateID == nil {
			continue
		}
		pt, err := s.templates.TemplateLookup(ctx, orgID, *p.TemplateID)
		if errors.Is(err, ErrNotFound) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("look up the parent run's template: %w", err)
		}
		if pt.PlaybookRepo != nil && *pt.PlaybookRepo == *tmpl.PlaybookRepo {
			return p.PinnedCommit, nil
		}
		return "", nil
	}
	return "", nil
}

// pinnedRepo is a pinned job's repository: repo when the job has a pin, nil
// otherwise.
func pinnedRepo(commit string, repo *uuid.UUID) *uuid.UUID {
	if commit == "" {
		return nil
	}
	return repo
}

// ErrPinnedRepoMoved refuses a retry of a pinned job whose template now
// runs a playbook of another repository, or none: the commit the retry
// would run again belongs to the repository the original ran.
var ErrPinnedRepoMoved = errors.New("the run being retried ran a commit of another repository than the template's playbook uses now; start a new run instead of a retry")

// checkRetryPinRepo refuses a retry of original when its pin belongs to a
// repository the template no longer runs (final review M4). A job pinned
// before its repository was recorded is not checked here (a template
// without a repository is still refused by checkPin).
func checkRetryPinRepo(original Job, tmpl TemplateRow) error {
	if original.PinnedCommit == "" || original.PinnedRepoID == nil {
		return nil
	}
	if tmpl.PlaybookRepo == nil || *tmpl.PlaybookRepo != *original.PinnedRepoID {
		return fmt.Errorf("%w: %w", ErrInvalidInput, ErrPinnedRepoMoved)
	}
	return nil
}

// checkClaimPin refuses, at the claim, a pinned job that cannot run its
// pin: only a template run whose playbook comes from a repository can,
// and only to a full commit id. Such a job fails rather than run the
// branch tip.
func checkClaimPin(job Job, isRepo bool) error {
	if job.PinnedCommit == "" {
		return nil
	}
	if job.Kind != KindRun || !isRepo {
		return fmt.Errorf("%w: %s job %s is pinned to commit %s, but runs no repository playbook", ErrInvalidInput, job.Kind, job.ID, job.PinnedCommit)
	}
	if !commitPattern.MatchString(job.PinnedCommit) {
		return fmt.Errorf("%w: job %s is pinned to %q, which is not a full commit id", ErrInvalidInput, job.ID, job.PinnedCommit)
	}
	return nil
}

// Pin is the commit a job is pinned to and the repository it belongs to;
// zero for an unpinned job (RepoID nil also for a job pinned before
// migration 00126).
type Pin struct {
	Commit string
	RepoID *uuid.UUID
}

// PinnedCommit is the pin of job id. A retry request's picture shows it:
// the retry runs that commit again, of that repository.
func (s *Service) PinnedCommit(ctx context.Context, orgID, id uuid.UUID) (Pin, error) {
	j, err := s.store.GetJob(ctx, orgID, id)
	if err != nil {
		return Pin{}, err
	}
	return Pin{Commit: j.PinnedCommit, RepoID: j.PinnedRepoID}, nil
}

// SyncedRef is the digest of what a sync fetched: the repository's URL,
// branch and subpath as its claim sent them to the worker. The sync's
// record keeps it (repo_var_sources.synced_ref) beside the commit it
// read, so a pin can tell that the repository changed after the sync. A
// digest rather than the values, so no copy of a URL (which may carry a
// credential from before backlog 7.103) is kept beside it.
func SyncedRef(url, branch, subpath string) string {
	doc, err := json.Marshal([]string{url, branch, subpath})
	if err != nil {
		// Three strings always encode.
		panic("synced ref: " + err.Error())
	}
	sum := sha256.Sum256(doc)
	return hex.EncodeToString(sum[:])
}

// recordSyncedRef writes what a sync's claim sends the worker to fetch, the
// repository of its built kind payload, as synced_ref onto the job's
// stored payload: the finish hands it to the source sink beside the commit
// the sync read, so the record says which URL, branch and subpath that
// commit came from, whatever is edited while the sync runs. Only that key
// is set (jsonb_set), so every other key of the stored payload stays as
// it is. A re-claim writes it again with what that claim sends.
func (q *SQLQueue) recordSyncedRef(ctx context.Context, job Job, kindPayload string) error {
	var sent struct {
		Repo struct {
			URL     string `json:"url"`
			Branch  string `json:"branch"`
			Subpath string `json:"subpath"`
		} `json:"repo"`
	}
	if err := json.Unmarshal([]byte(kindPayload), &sent); err != nil {
		return fmt.Errorf("sync_repo job %s: read its claim's repository: %w", job.ID, err)
	}
	ref := SyncedRef(sent.Repo.URL, sent.Repo.Branch, sent.Repo.Subpath)
	// A job no longer running (cancelled in between) records nothing: its
	// finish stores no sources.
	if _, err := q.db.Exec(ctx,
		`UPDATE jobs SET payload = jsonb_set(payload, '{synced_ref}', to_jsonb($2::text))
		 WHERE id = $1 AND status = 'running' AND jsonb_typeof(payload) = 'object'`, job.ID, ref); err != nil {
		return fmt.Errorf("record what sync %s fetches: %w", job.ID, err)
	}
	return nil
}
