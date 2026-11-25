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
}

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
// templates c touches (the template itself, or every template whose
// playbook comes from the repository) is paused and every enabled trigger
// that runs one of them, or syncs the repository, is disabled, each with
// the reason assistant_change, an activity entry and one review a person
// approves to resume it. An item already stopped for review gets c added
// to its open review instead; one a person paused or disabled is left
// alone (resuming it is that person's decision).
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
		paused := false
		switch {
		case !s.Paused:
			id := s.ID
			if s, paused, err = rv.src.Schedules.PauseForReview(ctx, orgID, id); err != nil {
				return fmt.Errorf("pause schedule %s for review: %w", id, err)
			}
		case s.PausedReason != schedules.PausedForReview:
			continue
		}
		if !paused && s.PausedReason != schedules.PausedForReview {
			continue // a person paused it meanwhile
		}
		rev, err := rv.revision(ctx, orgID, s.TemplateID, revisions)
		if err != nil {
			return err
		}
		id, err := rv.review(ctx, orgID, KindScheduleReview, s.ID, s.TemplateID, rev, c)
		if err != nil {
			return err
		}
		if paused {
			if err := rv.record(ctx, orgID, c, "pauseScheduleForReview", "schedule", s.ID, "paused schedule "+s.Name, id); err != nil {
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
		disabled := false
		switch {
		case t.Enabled:
			id := t.ID
			if t, disabled, err = rv.src.Triggers.DisableForReview(ctx, orgID, id); err != nil {
				return fmt.Errorf("disable trigger %s for review: %w", id, err)
			}
		case t.DisabledReason != triggers.DisabledForReview:
			continue
		}
		if !disabled && t.DisabledReason != triggers.DisabledForReview {
			continue
		}
		tmpl, rev := uuid.Nil, int64(0)
		if t.Kind == triggers.KindTemplateRun && t.TemplateID != nil {
			tmpl = *t.TemplateID
			if rev, err = rv.revision(ctx, orgID, tmpl, revisions); err != nil {
				return err
			}
		}
		id, err := rv.review(ctx, orgID, KindTriggerReview, t.ID, tmpl, rev, c)
		if err != nil {
			return err
		}
		if disabled {
			if err := rv.record(ctx, orgID, c, "disableTriggerForReview", "trigger", t.ID, "disabled webhook trigger "+t.Name, id); err != nil {
				return err
			}
		}
	}
	return nil
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

// review adds c to the open review of target, or opens one: one open
// review per item, whose expiry starts again with each change. It returns
// the review's id.
func (rv *Reviewer) review(ctx context.Context, orgID uuid.UUID, kind Kind, target, templateID uuid.UUID, rev int64, c assistantchange.Change) (uuid.UUID, error) {
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
		Review: &ReviewChange{Changes: []assistantchange.Change{c}},
	}
	if err := rv.store.Insert(ctx, r); err != nil {
		return uuid.Nil, fmt.Errorf("store review: %w", err)
	}
	return r.ID, nil
}

// reviewReason is a review's one line: what the assistant changed.
func reviewReason(c assistantchange.Change) string {
	what := "job template"
	if c.Subject == assistantchange.SubjectRepo {
		what = "repository"
	}
	return fmt.Sprintf("An assistant changed %s %s; a person reviews the change before this runs unattended again", what, c.Name)
}

// record writes the activity entry of one pause, as the assistant's token
// whose change caused it. The summary names the changed fields, never a
// value.
func (rv *Reviewer) record(ctx context.Context, orgID uuid.UUID, c assistantchange.Change, action, objectType string, id uuid.UUID, did string, review uuid.UUID) error {
	names := make([]string, len(c.Fields))
	for i, f := range c.Fields {
		names[i] = f.Name
	}
	what := "job template"
	if c.Subject == assistantchange.SubjectRepo {
		what = "repository"
	}
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
