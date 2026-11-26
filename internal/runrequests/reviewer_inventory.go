package runrequests

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/triggers"
)

// errNoReach refuses an inventory change when the reviewer cannot tell
// what it reaches: the change fails rather than run unreviewed.
var errNoReach = errors.New("the reviewer has no inventory reach wired")

// inventoryChanged pauses what runs unattended on the hosts an assistant's
// inventory change reaches (decision D6): a group change reaches every
// member of the group and of its descendant groups, a membership change
// its host. A template's run reaches such a host when the template's scope
// selects it and its limit keeps it or cannot be decided (fail closed);
// those templates, and every template that starts one of them through
// follow-ups, have their schedules paused and their triggers disabled.
// A schedule with its own limit decides on that limit instead of the
// template's, and a trigger that maps the limit from its payload counts
// as reaching every host its template's scope selects.
func (rv *Reviewer) inventoryChanged(ctx context.Context, orgID uuid.UUID, c assistantchange.Change) error {
	if rv.src.Reach == nil {
		return errNoReach
	}
	changed := render.Scope{GroupID: &c.ID}
	if c.Subject == assistantchange.SubjectInventoryMembership {
		if c.HostID == nil {
			return fmt.Errorf("a membership change of group %s names no host", c.ID)
		}
		changed = render.Scope{HostIDs: []uuid.UUID{*c.HostID}}
	}
	list, err := rv.src.Templates.List(ctx, orgID)
	if err != nil {
		return fmt.Errorf("list the templates an inventory change may reach: %w", err)
	}
	scopes := make(map[uuid.UUID]render.Scope, len(list))
	limits := make(map[uuid.UUID]string, len(list))
	for _, t := range list {
		scopes[t.ID] = render.Scope{GroupID: t.Scope.GroupID, HostIDs: t.Scope.HostIDs}
		limits[t.ID] = t.Limit
	}
	reached, err := rv.src.Reach.Overlap(ctx, orgID, changed, scopes)
	if err != nil {
		return fmt.Errorf("find the hosts the inventory change reaches: %w", err)
	}
	var direct []uuid.UUID
	for id, names := range reached {
		if limitKeeps(limits[id], names) {
			direct = append(direct, id)
		}
	}
	starting := map[uuid.UUID]bool{}
	if len(direct) > 0 {
		ids, err := rv.src.Templates.IDsReaching(ctx, orgID, direct)
		if err != nil {
			return fmt.Errorf("list the templates that start the reached ones: %w", err)
		}
		for _, id := range ids {
			starting[id] = true
		}
	}
	candidates := make([]uuid.UUID, 0, len(starting)+len(reached))
	for id := range starting {
		candidates = append(candidates, id)
	}
	for id := range reached {
		if !starting[id] {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	keepSchedule := func(s schedules.Schedule) bool {
		if s.Overrides.Limit != nil {
			if names := reached[s.TemplateID]; len(names) > 0 && limitKeeps(*s.Overrides.Limit, names) {
				return true
			}
		}
		return starting[s.TemplateID]
	}
	keepTrigger := func(t triggers.Trigger) bool {
		if t.Kind != triggers.KindTemplateRun || t.TemplateID == nil {
			return false
		}
		if t.Mapping.Limit != "" && len(reached[*t.TemplateID]) > 0 {
			return true
		}
		return starting[*t.TemplateID]
	}
	revisions := map[uuid.UUID]int64{}
	if err := rv.pauseSchedules(ctx, orgID, c, candidates, keepSchedule, revisions); err != nil {
		return err
	}
	return rv.disableTriggers(ctx, orgID, c, candidates, nil, keepTrigger, revisions)
}

// limitKeeps reports whether a run under limit ("" for none) reaches one
// of the host names: the limit matches it, or cannot decide (fail closed).
func limitKeeps(limit string, names []string) bool {
	for _, n := range names {
		if matched, decided := jobs.MatchLimit(limit, n); matched || !decided {
			return true
		}
	}
	return false
}
