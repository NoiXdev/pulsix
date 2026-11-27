package runrequests

import (
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
)

// ChangedObjectsOf is changedObjects: assistant changes as a review's
// picture shows them.
func ChangedObjectsOf(changes []assistantchange.Change) []ChangedObject {
	return changedObjects(changes)
}

// WithChainPath is n with its chain path set, as the picture walk sets it
// on a bound follow-up: a test's fake picture keys its bindings with it.
func WithChainPath(n PictureFollowUp, path ...uuid.UUID) PictureFollowUp {
	n.chainPath = path
	return n
}

// FollowUpKey is followUpKey: the key of a follow-up's binding.
func FollowUpKey(path []uuid.UUID, templateID uuid.UUID) string { return followUpKey(path, templateID) }
