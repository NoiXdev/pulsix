// Package chain stores and resolves job chaining (backlog 2.121): the
// follow-ups a run group was planned with, what becomes of them when the
// group finishes, and the sweep that resolves groups whose finish was never
// reported. Starting the follow-ups is the starter's job.
package chain

import (
	"time"

	"github.com/google/uuid"
)

// State is where a follow-up stands.
type State string

const (
	// StatePending: the parent run has not finished yet.
	StatePending State = "pending"
	// StateDue: the parent succeeded; the starter will launch it.
	StateDue State = "due"
	// StateStarted: the follow-up run was launched.
	StateStarted State = "started"
	// StateSkipped: the follow-up will never run; Reason says why.
	StateSkipped State = "skipped"
	// StateDismissed: a person cancelled it before it was decided.
	StateDismissed State = "dismissed"
)

// Reasons the parent run's outcome gives for skipping its follow-ups.
const (
	ReasonRunFailed    = "the run failed"
	ReasonRunCancelled = "the run was cancelled"
	ReasonRunNotStart  = "the run did not start"
	ReasonNoHostPlayed = "the run played on no host"
)

// conditionSuccess is the only follow-up condition.
const conditionSuccess = "success"

// FollowUp is one follow-up of a run group.
type FollowUp struct {
	ID, OrgID, ParentGroupID, TemplateID uuid.UUID
	// TemplateName is the template's current name, empty once it is deleted.
	TemplateName    string
	Position        int
	State           State
	Reason          string
	ChildGroupID    *uuid.UUID
	ChildFirstJobID *uuid.UUID
	HostIDs         []uuid.UUID
	RootTrigger     string
	RootRef         *uuid.UUID
	Depth           int
	Path            []uuid.UUID
	ConfiguredBy    *uuid.UUID
	Attempts        int
	NextAttemptAt   *time.Time
	CreatedAt       time.Time
}
