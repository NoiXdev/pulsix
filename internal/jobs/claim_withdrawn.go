package jobs

import (
	"errors"
	"fmt"
)

// A run job whose approval was withdrawn before its first claim (backlog
// 2.126). An approval records its decision first and then enqueues one job
// per zone; when an insert fails partway it puts the request back to
// pending, and the jobs it did create stay queued. The claim guard refuses
// each of them with ErrApprovalWithdrawn, and the queue fails it with the
// fixed text WithdrawnAtClaimError. Such a job ran nothing on any host and
// belongs to an approval that no longer stands, so:
//
//   - a replay of its run group (reserveRunGroup) does not count it as
//     covering its zone: re-approving the request enqueues that zone again
//     into the same group;
//   - the group's outcome leaves it out (runGroupQuery, groupSummaryQuery,
//     ListUnreportedFinishedRunGroups, chain.sql RunGroupOutcome): it is
//     neither counted nor the group's first job, the anchor of the
//     exactly-once finish report. A group of only such jobs is not
//     finished: it gets no finish report and no group summary. Once a
//     re-approval refills its zones, the group finishes when those jobs do
//     and reports once, and the follow-ups that approval planned start on
//     success. A group never re-approved (its request was rejected or
//     expired) keeps its follow-ups pending until the follow-up sweep
//     skips them as "the run did not start".
//
// Every other refusal (the run picture changed after approval, a failed
// lookup) is an ordinary error of the run: the job is counted, the group
// reports failed, and a replay keeps it as covering its zone.

// claimRefusedPrefix starts the error text of every job the claim guard
// refused; only the guard's path writes it.
const claimRefusedPrefix = "refused at claim: "

// approvalWithdrawnReason is ErrApprovalWithdrawn's text.
const approvalWithdrawnReason = "the run's request is not approved"

// ErrApprovalWithdrawn is the claim guard's refusal of a run whose request
// is not approved: the approval that enqueued it was undone by a failed
// enqueue. A guard returns it (or an error wrapping it) from CheckRunClaim.
var ErrApprovalWithdrawn = errors.New(approvalWithdrawnReason)

// WithdrawnAtClaimError is the whole error text of a job refused at claim
// with ErrApprovalWithdrawn, whatever the guard wrapped around it. The
// queries that leave such a job out of its group's outcome take it as a
// parameter (withdrawnAtClaimSQL), so the text lives only here.
const WithdrawnAtClaimError = claimRefusedPrefix + approvalWithdrawnReason

// withdrawnAtClaim reports whether j is a job refused at its first and only
// claim because its approval was withdrawn. A job refused on a later claim
// was claimed before (by a worker since lost) and may have run the play
// then, so it still counts as having run. withdrawnAtClaimSQL is the same
// test in SQL.
func withdrawnAtClaim(j Job) bool {
	return j.Status == StatusError && j.Attempts == 1 && j.Error == WithdrawnAtClaimError
}

// WithdrawnAtClaim reports whether the job is one refused at its first
// claim because its approval was withdrawn (see withdrawnAtClaim). The API
// exposes it so a client can tell such a job from an ordinary error.
func (j Job) WithdrawnAtClaim() bool { return withdrawnAtClaim(j) }

// withdrawnAtClaimSQL is withdrawnAtClaim as a SQL condition over a jobs
// row, the error text bound as parameter n. The sqlc queries spell the same
// condition with a named parameter; TestWithdrawnAtClaimSQLMatchesGo pins
// them all to the Go predicate.
func withdrawnAtClaimSQL(n int) string {
	return fmt.Sprintf(`(status = 'error' AND attempts = 1 AND error = $%d)`, n)
}

// claimRefusal is the reason the queue fails a job with when the claim
// guard refuses it: the fixed WithdrawnAtClaimError for a withdrawn
// approval, so the outcome queries can match it exactly, and the prefix
// and the guard's own error otherwise.
func claimRefusal(err error) error {
	if errors.Is(err, ErrApprovalWithdrawn) {
		return errors.New(WithdrawnAtClaimError)
	}
	return fmt.Errorf("%s%w", claimRefusedPrefix, err)
}
