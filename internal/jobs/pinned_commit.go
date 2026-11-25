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
	if req.Trigger != TriggerManual {
		return fmt.Errorf("%w: only an approved run is pinned to a commit, not a %s run", ErrInvalidInput, req.Trigger)
	}
	if tmpl.PlaybookRepo == nil {
		return fmt.Errorf("%w: the template's playbook comes from no repository, so there is no commit to pin", ErrInvalidInput)
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

// PinnedCommit is the commit job id is pinned to, "" for none. A retry
// request's picture shows it: the retry runs that commit again.
func (s *Service) PinnedCommit(ctx context.Context, orgID, id uuid.UUID) (string, error) {
	j, err := s.store.GetJob(ctx, orgID, id)
	if err != nil {
		return "", err
	}
	return j.PinnedCommit, nil
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

// recordSyncedRef writes what a sync's claim sends the worker to fetch,
// the repository of its built kind payload, onto the job's stored payload:
// the finish hands it to the source sink beside the commit the sync read,
// so the record says which URL, branch and subpath that commit came from,
// whatever is edited while the sync runs. A re-claim writes it again with
// what that claim sends.
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
	opened, err := q.openKindPayload(job)
	if err != nil {
		return err
	}
	var sp syncPayload
	if err := json.Unmarshal([]byte(opened), &sp); err != nil {
		return fmt.Errorf("sync_repo job %s: kind payload: %w", job.ID, err)
	}
	sp.SyncedRef = SyncedRef(sent.Repo.URL, sent.Repo.Branch, sent.Repo.Subpath)
	b, err := json.Marshal(sp)
	if err != nil {
		return fmt.Errorf("encode sync payload: %w", err)
	}
	if _, err := q.db.Exec(ctx, `UPDATE jobs SET payload = $2 WHERE id = $1 AND status = 'running'`, job.ID, b); err != nil {
		return fmt.Errorf("record what sync %s fetches: %w", job.ID, err)
	}
	return nil
}
