package runrequests

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/triggers"
)

// ReviewStore is the part of the store the Reviewer writes.
type ReviewStore interface {
	Insert(ctx context.Context, r Request) error
	// OpenReview is the review of kind for target pending and unexpired
	// at now; ErrNotFound when there is none.
	OpenReview(ctx context.Context, orgID uuid.UUID, kind Kind, target uuid.UUID, now time.Time) (Request, error)
	// RefreshReview makes the pending review id show change and expire
	// at expiresAt.
	RefreshReview(ctx context.Context, orgID, id uuid.UUID, change ReviewChange, expiresAt time.Time, templateRevision int64) error
	// CloseReviews decides the open reviews of kind for target as
	// approved by by.
	CloseReviews(ctx context.Context, orgID uuid.UUID, kind Kind, target, by uuid.UUID, at time.Time) error
}

// The services the Reviewer reads and stops, each narrowed to what it uses.
type (
	// ReviewTemplates names a template's revision and the templates of a
	// repository (templates.Service).
	ReviewTemplates interface {
		Get(ctx context.Context, orgID, id uuid.UUID) (templates.Template, error)
		IDsOfRepo(ctx context.Context, orgID, repoID uuid.UUID) ([]uuid.UUID, error)
		IDsOfPlaybook(ctx context.Context, orgID, playbookID uuid.UUID) ([]uuid.UUID, error)
	}
	// ReviewHistory names who last did one of actions to an object (the
	// activity log, audit.SQLStore): who had paused an item before.
	ReviewHistory interface {
		LastActor(ctx context.Context, orgID uuid.UUID, objectType, objectID string, actions []string) (*uuid.UUID, error)
	}
	// ReviewSchedules lists and pauses schedules (schedules.Service).
	ReviewSchedules interface {
		ListOfTemplates(ctx context.Context, orgID uuid.UUID, templateIDs []uuid.UUID) ([]schedules.Schedule, error)
		PauseForReview(ctx context.Context, orgID, id uuid.UUID) (schedules.Schedule, bool, error)
	}
	// ReviewTriggers lists and disables webhook triggers
	// (triggers.Service).
	ReviewTriggers interface {
		ListForReview(ctx context.Context, orgID uuid.UUID, templateIDs []uuid.UUID, repoID *uuid.UUID) ([]triggers.Trigger, error)
		DisableForReview(ctx context.Context, orgID, id uuid.UUID) (triggers.Trigger, bool, error)
	}
	// Auditor records each pause in the activity log.
	Auditor interface {
		Record(ctx context.Context, e audit.Event) error
	}
)

// ReviewSources is what NewReviewer reads and stops.
type ReviewSources struct {
	Templates ReviewTemplates
	Schedules ReviewSchedules
	Triggers  ReviewTriggers
	// History names who had stopped an item before; nil names nobody.
	History ReviewHistory
}

// The activity log actions that stop a schedule or a trigger (the API's
// "<method> <operation>"): who did the last of them had stopped the item.
var (
	schedulePauseActions  = []string{"POST pauseSchedule"}
	triggerDisableActions = []string{"POST disableTrigger", "PUT updateTrigger", "POST createTrigger"}
)

// Reviewer stops what runs unattended after an assistant's change to what
// it runs (spec 2026-11-25 assistant API gaps, C2, decision D5): it
// implements assistantchange.Watcher for templates and repositories, and
// closes a review when a person resumes or enables its item directly
// (schedules.Reviews, triggers.Reviews). Every call runs inside the
// caller's transaction: it opens none of its own.
type Reviewer struct {
	store ReviewStore
	src   ReviewSources
	audit Auditor
	now   func() time.Time
	newID func() uuid.UUID
}

// NewReviewer wires the reviewer.
func NewReviewer(store ReviewStore, src ReviewSources, auditor Auditor, now func() time.Time, newID func() uuid.UUID) *Reviewer {
	return &Reviewer{store: store, src: src, audit: auditor, now: now, newID: newID}
}

// Changed implements assistantchange.Watcher: every active schedule of the
// templates c touches (the template itself, every template whose playbook
// comes from the repository, or every template that runs the editor
// playbook) is paused and every enabled trigger that runs one of them, or
// syncs the repository, is disabled, each with the reason assistant_change,
// an activity entry and one review a person approves to resume it. An item
// a person had paused or disabled gets the reason and a review as well,
// which remembers who stopped it (PausedBefore), so the notice shows before
// anyone resumes it; approving that review leaves it stopped. An item
// already stopped for review gets c added to its open review instead.
func (rv *Reviewer) Changed(ctx context.Context, orgID uuid.UUID, c assistantchange.Change) error {
	var templateIDs []uuid.UUID
	var repoID *uuid.UUID
	switch c.Subject {
	case assistantchange.SubjectTemplate:
		templateIDs = []uuid.UUID{c.ID}
	case assistantchange.SubjectRepo:
		ids, err := rv.src.Templates.IDsOfRepo(ctx, orgID, c.ID)
		if err != nil {
			return fmt.Errorf("list the repository's templates: %w", err)
		}
		templateIDs, repoID = ids, &c.ID
	case assistantchange.SubjectPlaybook:
		ids, err := rv.src.Templates.IDsOfPlaybook(ctx, orgID, c.ID)
		if err != nil {
			return fmt.Errorf("list the playbook's templates: %w", err)
		}
		templateIDs = ids
	default:
		return fmt.Errorf("an assistant change of an unknown subject %q", c.Subject)
	}
	revisions := map[uuid.UUID]int64{}
	if err := rv.pauseSchedules(ctx, orgID, c, templateIDs, revisions); err != nil {
		return err
	}
	return rv.disableTriggers(ctx, orgID, c, templateIDs, repoID, revisions)
}

func (rv *Reviewer) pauseSchedules(ctx context.Context, orgID uuid.UUID, c assistantchange.Change, templateIDs []uuid.UUID, revisions map[uuid.UUID]int64) error {
	list, err := rv.src.Schedules.ListOfTemplates(ctx, orgID, templateIDs)
	if err != nil {
		return fmt.Errorf("list the schedules to pause: %w", err)
	}
	for _, s := range list {
		wasPaused, paused := s.Paused, false
		if s.PausedReason != schedules.PausedForReview {
			id := s.ID
			if s, paused, err = rv.src.Schedules.PauseForReview(ctx, orgID, id); err != nil {
				return fmt.Errorf("pause schedule %s for review: %w", id, err)
			}
		}
		var before *PausedBefore
		if paused && wasPaused {
			if before, err = rv.pausedBefore(ctx, orgID, "schedule", s.ID, schedulePauseActions); err != nil {
				return err
			}
		}
		rev, err := rv.revision(ctx, orgID, s.TemplateID, revisions)
		if err != nil {
			return err
		}
		id, err := rv.review(ctx, orgID, KindScheduleReview, s.ID, s.TemplateID, rev, c, before)
		if err != nil {
			return err
		}
		if paused {
			did := "paused schedule " + s.Name
			if wasPaused {
				did = "kept schedule " + s.Name + " paused"
			}
			if err := rv.record(ctx, orgID, c, "pauseScheduleForReview", "schedule", s.ID, did, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func (rv *Reviewer) disableTriggers(ctx context.Context, orgID uuid.UUID, c assistantchange.Change, templateIDs []uuid.UUID, repoID *uuid.UUID, revisions map[uuid.UUID]int64) error {
	list, err := rv.src.Triggers.ListForReview(ctx, orgID, templateIDs, repoID)
	if err != nil {
		return fmt.Errorf("list the triggers to disable: %w", err)
	}
	for _, t := range list {
		wasDisabled, disabled := !t.Enabled, false
		if t.DisabledReason != triggers.DisabledForReview {
			id := t.ID
			if t, disabled, err = rv.src.Triggers.DisableForReview(ctx, orgID, id); err != nil {
				return fmt.Errorf("disable trigger %s for review: %w", id, err)
			}
		}
		var before *PausedBefore
		if disabled && wasDisabled {
			if before, err = rv.pausedBefore(ctx, orgID, "trigger", t.ID, triggerDisableActions); err != nil {
				return err
			}
		}
		tmpl, rev := uuid.Nil, int64(0)
		if t.Kind == triggers.KindTemplateRun && t.TemplateID != nil {
			tmpl = *t.TemplateID
			if rev, err = rv.revision(ctx, orgID, tmpl, revisions); err != nil {
				return err
			}
		}
		id, err := rv.review(ctx, orgID, KindTriggerReview, t.ID, tmpl, rev, c, before)
		if err != nil {
			return err
		}
		if disabled {
			did := "disabled webhook trigger " + t.Name
			if wasDisabled {
				did = "kept webhook trigger " + t.Name + " disabled"
			}
			if err := rv.record(ctx, orgID, c, "disableTriggerForReview", "trigger", t.ID, did, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// pausedBefore records who had stopped the item (objectType id) before:
// the actor of the last of actions in the activity log, if any.
func (rv *Reviewer) pausedBefore(ctx context.Context, orgID uuid.UUID, objectType string, id uuid.UUID, actions []string) (*PausedBefore, error) {
	before := &PausedBefore{}
	if rv.src.History == nil {
		return before, nil
	}
	by, err := rv.src.History.LastActor(ctx, orgID, objectType, id.String(), actions)
	if err != nil {
		return nil, fmt.Errorf("find who stopped %s %s: %w", objectType, id, err)
	}
	before.By = by
	return before, nil
}

// revision is the template's revision now (after the change), read once
// per template.
func (rv *Reviewer) revision(ctx context.Context, orgID, templateID uuid.UUID, seen map[uuid.UUID]int64) (int64, error) {
	if rev, ok := seen[templateID]; ok {
		return rev, nil
	}
	t, err := rv.src.Templates.Get(ctx, orgID, templateID)
	if err != nil {
		return 0, fmt.Errorf("read template %s: %w", templateID, err)
	}
	seen[templateID] = t.Revision
	return t.Revision, nil
}

// review adds c to the open review of target, or opens one (with before,
// who had stopped the item): one open review per item, whose expiry starts
// again with each change and which keeps who had stopped it. It returns
// the review's id.
func (rv *Reviewer) review(ctx context.Context, orgID uuid.UUID, kind Kind, target, templateID uuid.UUID, rev int64, c assistantchange.Change, before *PausedBefore) (uuid.UUID, error) {
	now := rv.now()
	open, err := rv.store.OpenReview(ctx, orgID, kind, target, now)
	switch {
	case err == nil:
		change := ReviewChange{}
		if open.Review != nil {
			change = *open.Review
		}
		change.Changes = append(change.Changes, c)
		if n := len(change.Changes); n > MaxReviewChanges {
			change.Changes = change.Changes[n-MaxReviewChanges:]
		}
		if err := rv.store.RefreshReview(ctx, orgID, open.ID, change, now.Add(ReviewTTL), rev); err != nil {
			return uuid.Nil, err
		}
		return open.ID, nil
	case !errors.Is(err, ErrNotFound):
		return uuid.Nil, err
	}
	r := Request{
		ID: rv.newID(), OrgID: orgID, Kind: kind, TemplateID: templateID, TemplateRevision: rev,
		Reason: reviewReason(c), RequestedByUser: c.UserID, RequestedByToken: c.TokenID,
		Status: StatusPending, CreatedAt: now, ExpiresAt: now.Add(ReviewTTL), TargetID: &target,
		Review: &ReviewChange{Changes: []assistantchange.Change{c}, PausedBefore: before},
	}
	if err := rv.store.Insert(ctx, r); err != nil {
		return uuid.Nil, fmt.Errorf("store review: %w", err)
	}
	return r.ID, nil
}

// reviewReason is a review's one line: what the assistant changed.
func reviewReason(c assistantchange.Change) string {
	return fmt.Sprintf("An assistant changed %s %s; a person reviews the change before this runs unattended again", subjectWords(c.Subject), c.Name)
}

// subjectWords names a change's subject in a sentence.
func subjectWords(s assistantchange.Subject) string {
	switch s {
	case assistantchange.SubjectRepo:
		return "repository"
	case assistantchange.SubjectPlaybook:
		return "playbook"
	}
	return "job template"
}

// record writes the activity entry of one pause, as the assistant's token
// whose change caused it. The summary names the changed fields, never a
// value.
func (rv *Reviewer) record(ctx context.Context, orgID uuid.UUID, c assistantchange.Change, action, objectType string, id uuid.UUID, did string, review uuid.UUID) error {
	names := make([]string, len(c.Fields))
	for i, f := range c.Fields {
		names[i] = f.Name
	}
	what := subjectWords(c.Subject)
	user, kind := c.UserID, audit.ActorKindToken
	if c.TokenID == nil {
		kind = audit.ActorKindUser
	}
	err := rv.audit.Record(ctx, audit.Event{
		OrgID: &orgID, ActorID: &user, ActorKind: kind, TokenID: c.TokenID,
		Action: action, ObjectType: objectType, ObjectID: id.String(),
		Summary: fmt.Sprintf("%s for review: an assistant changed %s %s (%s)", did, what, c.Name, strings.Join(names, ", ")),
		Meta:    map[string]any{"review_request_id": review.String(), "changed": string(c.Subject), "changed_id": c.ID.String()},
	})
	if err != nil {
		return fmt.Errorf("record the pause: %w", err)
	}
	return nil
}

// ScheduleResumed implements schedules.Reviews: a person resumed the
// schedule directly, which decides its open review as theirs.
func (rv *Reviewer) ScheduleResumed(ctx context.Context, orgID, id, by uuid.UUID) error {
	return rv.store.CloseReviews(ctx, orgID, KindScheduleReview, id, by, rv.now())
}

// TriggerEnabled implements triggers.Reviews: a person enabled the trigger
// directly, which decides its open review as theirs.
func (rv *Reviewer) TriggerEnabled(ctx context.Context, orgID, id, by uuid.UUID) error {
	return rv.store.CloseReviews(ctx, orgID, KindTriggerReview, id, by, rv.now())
}
