package runrequests

import "github.com/google/uuid"

// FollowUpBindings is what an approval binds of the follow-up runs it
// starts (backlog 2.143, the owner's decision of 2026-11-27): an entry per
// follow-up of the approved selection, its subtrees included, keyed by its
// chain path and template. The claim of a follow-up's job compares the live state with
// its entry (Service.checkFollowUpClaim). Stored with the decision; nil on
// a request approved before it existed, whose follow-ups run unchecked as
// before.
type FollowUpBindings struct {
	Nodes map[string]FollowUpBinding `json:"nodes"`
}

// FollowUpBinding is one follow-up's entry: the fingerprint of its hosts
// and vars as the approval saw them (FollowUpFingerprint) and the hosts
// they were taken from, its parent's bound hosts (the approved run's
// targets for a direct follow-up). Its hosts are its template's targets
// among Within.
type FollowUpBinding struct {
	Fingerprint string      `json:"fingerprint"`
	Within      []uuid.UUID `json:"within"`
}

// FollowUpOrigin is where a follow-up run sits: the run request whose run
// group is the root of its chain, and its own row's chain path and
// template, which key its binding.
type FollowUpOrigin struct {
	RequestID  uuid.UUID
	Path       []uuid.UUID
	TemplateID uuid.UUID
}
