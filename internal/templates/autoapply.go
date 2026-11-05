// The automatic apply (spec 7.75 goal 5, plan decisions 6 and 9): after a
// repository's sync stored new variable sources (jobs.SourceApplier), or
// an editor playbook was saved (playbooks.InlineApplier), every template
// of those playbooks in automatic mode takes the additions and the safe
// changes and marks gone fields; removals and changes that need review
// wait for a person. Only the fields and the applied revision are
// written: a stored value is never sealed, unsealed or dropped.
package templates

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/varsync"
)

// SetLogger sets where the automatic apply reports a template it could
// not apply to; slog.Default without.
func (s *Service) SetLogger(l *slog.Logger) { s.log = l }

// SetClock sets the clock an apply's time is read from (time.Now by
// default).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

func (s *Service) logger() *slog.Logger {
	if s.log != nil {
		return s.log
	}
	return slog.Default()
}

// ApplyRepoSources applies the repository's new variable sources to the
// organization's automatic templates of its playbooks (jobs.SourceApplier).
// A template that cannot be applied to is logged and skipped; only a
// failure to list them is returned.
func (s *Service) ApplyRepoSources(ctx context.Context, orgID, repoID uuid.UUID) error {
	ctx = db.WithOrgScope(ctx, orgID)
	list, err := s.store.ListAutomaticForRepo(ctx, orgID, repoID)
	if err != nil {
		return err
	}
	s.applyAutomatic(ctx, orgID, list)
	return nil
}

// ApplyPlaybookSources applies an editor playbook's new variable sources
// to its automatic templates (playbooks.InlineApplier), like
// ApplyRepoSources.
func (s *Service) ApplyPlaybookSources(ctx context.Context, orgID, playbookID uuid.UUID) error {
	ctx = db.WithOrgScope(ctx, orgID)
	list, err := s.store.ListAutomaticForPlaybook(ctx, orgID, playbookID)
	if err != nil {
		return err
	}
	s.applyAutomatic(ctx, orgID, list)
	return nil
}

// applyAutomatic applies to each template of list, reading each
// playbook's sources once, and logs a template it could not apply to.
func (s *Service) applyAutomatic(ctx context.Context, orgID uuid.UUID, list []Template) {
	sources := map[uuid.UUID]varsync.Source{}
	for _, t := range list {
		if err := s.applyTo(ctx, orgID, t, sources); err != nil {
			s.logger().ErrorContext(ctx, "apply variable sources to a template; the next sync tries again",
				"org", orgID, "template", t.ID, "err", err)
		}
	}
}

// applyTo applies its playbook's sources to the template t as read. The
// write names the revision read, so a person's save in between is never
// overwritten: on a lost race the template is read and applied to once
// more, and a second lost race is left to the next sync.
func (s *Service) applyTo(ctx context.Context, orgID uuid.UUID, t Template, sources map[uuid.UUID]varsync.Source) error {
	for attempt := 0; ; attempt++ {
		if t.VarsMode != ModeAutomatic || t.SharedTemplateID != nil {
			return nil
		}
		src, ok := sources[t.PlaybookID]
		if !ok {
			vs, err := s.varSource(ctx, orgID, t.PlaybookID)
			if err != nil {
				return err
			}
			src = toSource(vs)
			sources[t.PlaybookID] = src
		}
		if !src.Available {
			return nil
		}
		next, write, err := automatic(t, src, s.now())
		if err != nil || !write {
			return err
		}
		_, err = s.store.UpdateVarsSource(ctx, next, &t.Revision)
		switch {
		case errors.Is(err, ErrRevisionMismatch) && attempt == 0:
		case errors.Is(err, ErrNotFound):
			return nil
		default:
			return err
		}
		if t, err = s.store.Get(ctx, orgID, t.ID); err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			return err
		}
	}
}

// automatic is t with src applied the automatic way, and whether that
// changes its fields or its applied revision (a new read that changes no
// field still records its revision, so the page does not offer it as
// newer). Applying the same read twice changes nothing.
func automatic(t Template, src varsync.Source, now time.Time) (Template, bool, error) {
	current := fieldsOf(t)
	fields := varsync.Apply(current, varsync.Compute(current, src, t.ExtraVars), varsync.ApplyOptions{Automatic: true})
	rev := varsync.AppliedRev(t.VarsSourceRev, src)
	same, err := sameFields(current, fields)
	if err != nil || (same && rev == t.VarsSourceRev) {
		return t, false, err
	}
	sc := &schema.Schema{Fields: fields}
	if err := sc.Check(); err != nil {
		return t, false, fmt.Errorf("the applied fields do not check: %w", err)
	}
	t.ExtraVarsSchema, t.VarsSourceRev, t.VarsAppliedAt = sc, rev, &now
	return t, true, nil
}

// sameFields reports whether a and b store the same.
func sameFields(a, b []schema.Field) (bool, error) {
	ja, err := json.Marshal(append([]schema.Field{}, a...))
	if err != nil {
		return false, fmt.Errorf("encode fields: %w", err)
	}
	jb, err := json.Marshal(append([]schema.Field{}, b...))
	if err != nil {
		return false, fmt.Errorf("encode fields: %w", err)
	}
	return bytes.Equal(ja, jb), nil
}
