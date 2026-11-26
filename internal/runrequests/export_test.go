package runrequests

import "github.com/noixdev/pulsix/internal/assistantchange"

// ChangedObjectsOf is changedObjects: assistant changes as a review's
// picture shows them.
func ChangedObjectsOf(changes []assistantchange.Change) []ChangedObject {
	return changedObjects(changes)
}
