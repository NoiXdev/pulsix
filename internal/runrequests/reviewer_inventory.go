package runrequests

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/inventory/render"
)

// errNoReach refuses an inventory change when the reviewer cannot tell
// what it reaches: the change fails rather than run unreviewed.
var errNoReach = errors.New("the reviewer has no inventory reach wired")

// inventoryChanged pauses what runs unattended on the hosts an assistant's
// inventory change reaches (decisions D6, D7): c.Reach names them — a
// group's members and its descendant groups' members, single hosts, and
// the groups that held them before (a host left, a group moved away). A
// template's run reaches such a host when the template's scope selects it
// now, or the template is scoped to one of the former groups. The limit
// does not narrow this: a limited run still renders every scoped host of
// its zones, so a play reads their values through hostvars. Those
// templates, and every template that starts one of them through
// follow-ups, have their schedules paused and their triggers disabled.
// An organization with no schedule and no template trigger at all has
// nothing to pause and is answered without reading the inventory.
func (rv *Reviewer) inventoryChanged(ctx context.Context, orgID uuid.UUID, c assistantchange.Change) error {
	if rv.src.Reach == nil {
		return errNoReach
	}
	if c.Reach == nil {
		return fmt.Errorf("an inventory change of %s %s names no reach", c.Subject, c.ID)
	}
	has, err := rv.store.HasAutomation(ctx, orgID)
	if err != nil {
		return fmt.Errorf("look for schedules and triggers: %w", err)
	}
	if !has {
		return nil
	}
	var changed []render.Scope
	if c.Reach.Group != nil {
		changed = append(changed, render.Scope{GroupID: c.Reach.Group})
	}
	if len(c.Reach.Hosts) > 0 {
		changed = append(changed, render.Scope{HostIDs: c.Reach.Hosts})
	}
	list, err := rv.src.Templates.List(ctx, orgID)
	if err != nil {
		return fmt.Errorf("list the templates an inventory change may reach: %w", err)
	}
	scopes := make(map[uuid.UUID]render.Scope, len(list))
	var direct []uuid.UUID
	for _, t := range list {
		if t.Scope.GroupID != nil && slices.Contains(c.Reach.FormerGroups, *t.Scope.GroupID) {
			direct = append(direct, t.ID)
			continue
		}
		scopes[t.ID] = render.Scope{GroupID: t.Scope.GroupID, HostIDs: t.Scope.HostIDs}
	}
	if len(changed) > 0 && len(scopes) > 0 {
		reached, err := rv.src.Reach.Overlap(ctx, orgID, changed, scopes)
		if err != nil {
			return fmt.Errorf("find the hosts the inventory change reaches: %w", err)
		}
		for id := range reached {
			direct = append(direct, id)
		}
	}
	if len(direct) == 0 {
		return nil
	}
	templateIDs, err := rv.src.Templates.IDsReaching(ctx, orgID, direct)
	if err != nil {
		return fmt.Errorf("list the templates that start the reached ones: %w", err)
	}
	revisions := map[uuid.UUID]int64{}
	if err := rv.pauseSchedules(ctx, orgID, c, templateIDs, nil, revisions); err != nil {
		return err
	}
	return rv.disableTriggers(ctx, orgID, c, templateIDs, nil, nil, revisions)
}
