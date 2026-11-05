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

// linkDeps is what the variables link adds to the service: the clock an
// apply's time is read from and where what it does not let fail is
// logged.
type linkDeps struct {
	now func() time.Time
	log *slog.Logger
}

// SetLogger sets where the automatic apply reports a template it could
// not apply to, and the list a review it could not compute; slog.Default
// without.
func (s *Service) SetLogger(l *slog.Logger) { s.link.log = l }

// SetClock sets the clock an apply's time is read from (time.Now by
// default).
func (s *Service) SetClock(now func() time.Time) { s.link.now = now }

func (s *Service) logger() *slog.Logger {
	if s.link.log != nil {
		return s.link.log
	}
	return slog.Default()
}

func (s *Service) now() time.Time {
	if s.link.now != nil {
		return s.link.now()
	}
	return time.Now()
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
// write names the revision and the applied source revision read, so a
// person's save or another apply in between is never overwritten: on a
// lost race the template and its playbook's sources are read again and
// applied once more, and a second lost race is left to the next sync.
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
		err := s.writeAutomatic(ctx, t, src)
		switch {
		case errors.Is(err, ErrRevisionMismatch) && attempt == 0:
		case errors.Is(err, ErrNotFound):
			return nil
		default:
			return err
		}
		// What won the race may have applied a newer read: read both again.
		delete(sources, t.PlaybookID)
		if t, err = s.store.Get(ctx, orgID, t.ID); err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			return err
		}
	}
}

// writeAutomatic writes src applied the automatic way to t, guarded by
// t's revision and applied source revision: the fields and the revision
// when the fields change, only the applied source revision (no new
// revision, so nobody editing the template is sent back to reload) when
// a new read changes none of them, nothing when the read was applied.
func (s *Service) writeAutomatic(ctx context.Context, t Template, src varsync.Source) error {
	current := fieldsOf(t)
	fields := varsync.Apply(current, varsync.Compute(current, src, t.ExtraVars), varsync.ApplyOptions{Automatic: true})
	same, err := sameFields(current, fields)
	if err != nil {
		return err
	}
	next := t
	next.VarsSourceRev = varsync.AppliedRev(t.VarsSourceRev, src)
	at := s.now()
	next.VarsAppliedAt = &at
	switch {
	case same && next.VarsSourceRev == t.VarsSourceRev:
		return nil
	case same:
		_, err = s.store.RecordSourceRev(ctx, next, t.Revision, t.VarsSourceRev)
		return err
	}
	next.ExtraVarsSchema = &schema.Schema{Fields: fields}
	if err := next.ExtraVarsSchema.Check(); err != nil {
		return fmt.Errorf("the applied fields do not check: %w", err)
	}
	_, err = s.store.UpdateVarsSource(ctx, next, &t.Revision, &t.VarsSourceRev)
	return err
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
