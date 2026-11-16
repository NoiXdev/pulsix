package chain

import (
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// followUpRow is the joined row both read queries return.
type followUpRow = gen.ListRunFollowUpsRow

func toFollowUp(r followUpRow) FollowUp {
	f := FollowUp{
		ID: r.ID, OrgID: r.OrgID, ParentGroupID: r.ParentGroupID, TemplateID: r.TemplateID, TemplateName: r.TemplateName,
		Position: int(r.Position), State: State(r.State), Reason: r.Reason, ChildGroupID: db.UUIDPtr(r.ChildGroupID),
		HostIDs: r.HostIds, RootTrigger: r.RootTrigger, RootRef: db.UUIDPtr(r.RootRef), Depth: int(r.Depth), Path: r.Path,
		ConfiguredBy: db.UUIDPtr(r.ConfiguredBy), Attempts: int(r.Attempts), CreatedAt: r.CreatedAt,
	}
	if r.ChildFirstJobID != uuid.Nil {
		id := r.ChildFirstJobID
		f.ChildFirstJobID = &id
	}
	if r.NextAttemptAt.Valid {
		t := r.NextAttemptAt.Time
		f.NextAttemptAt = &t
	}
	return f
}
