package hosts

import (
	"context"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
)

// SetChangeWatcher wires the watcher an assistant's host that newly falls
// into run scopes is handed to (decision D7): it pauses what runs
// unattended there until a person reviews it. Without one nothing is
// handed on.
func (s *Service) SetChangeWatcher(w assistantchange.Watcher) { s.watcher = w }

func (s *Service) watches(actor assistantchange.Actor) bool {
	return actor.Assistant && s.watcher != nil
}

// handOnManaged tells the watcher about an assistant's write that turned
// before into after when after is managed and before was not (a new host
// is an unmanaged before): every run whose scope selects it reaches it
// now. field, old and next are what the picture shows.
func (s *Service) handOnManaged(ctx context.Context, actor assistantchange.Actor, before, after Host, field string, old, next any) error {
	if !s.watches(actor) || before.Managed || !after.Managed {
		return nil
	}
	c := assistantchange.New(assistantchange.SubjectHost, after.ID, after.Name, []assistantchange.Field{{Name: field, Old: old, New: next}}, actor)
	c.Reach = &assistantchange.Reach{Hosts: []uuid.UUID{after.ID}}
	return s.watcher.Changed(ctx, after.OrgID, c)
}

// updateWatched writes h (UpdateHostWith). An assistant's write to a
// managed host is handed on in the same transaction when it makes the host
// managed, moves it to another zone (another worker reaches it) or changes
// its jump host (decision D7): what runs there reaches it otherwise now.
func (s *Service) updateWatched(ctx context.Context, h Host, x HostExtras, actor assistantchange.Actor) (Host, error) {
	if !s.watches(actor) || !h.Managed {
		return s.store.UpdateHostWith(ctx, h, x)
	}
	var out Host
	err := s.store.InTx(ctx, h.OrgID, func(ctx context.Context) error {
		before, err := s.store.GetHost(ctx, h.OrgID, h.ID)
		if err != nil {
			return err
		}
		var jumpBefore Jump
		if x.Jump != nil {
			if jumpBefore, err = s.store.GetJump(ctx, h.OrgID, h.ID); err != nil {
				return err
			}
		}
		if out, err = s.store.UpdateHostWith(ctx, h, x); err != nil {
			return err
		}
		var fields []assistantchange.Field
		if !before.Managed {
			fields = append(fields, assistantchange.Field{Name: "managed", Old: false, New: true})
		}
		if before.ZoneID != out.ZoneID {
			fields = append(fields, assistantchange.Field{Name: "zone", Old: s.zoneLabel(ctx, h.OrgID, before.ZoneID), New: s.zoneLabel(ctx, h.OrgID, out.ZoneID)})
		}
		if x.Jump != nil && !sameJump(jumpBefore, *x.Jump) {
			fields = append(fields, assistantchange.Field{Name: "jump_host", Old: jumpLabel(jumpBefore), New: jumpLabel(*x.Jump)})
		}
		return s.handOnHost(ctx, actor, out, fields)
	})
	if err != nil {
		return Host{}, err
	}
	return out, nil
}

// handOnHost tells the watcher about an assistant's change of host h
// moving fields; nothing when none moved.
func (s *Service) handOnHost(ctx context.Context, actor assistantchange.Actor, h Host, fields []assistantchange.Field) error {
	if !s.watches(actor) || len(fields) == 0 {
		return nil
	}
	c := assistantchange.New(assistantchange.SubjectHost, h.ID, h.Name, fields, actor)
	c.Reach = &assistantchange.Reach{Hosts: []uuid.UUID{h.ID}}
	return s.watcher.Changed(ctx, h.OrgID, c)
}

// zoneLabel names a zone by its slug, or its id when it cannot be read.
func (s *Service) zoneLabel(ctx context.Context, orgID, id uuid.UUID) string {
	if z, err := s.store.GetZone(ctx, orgID, id); err == nil {
		return z.Slug
	}
	return id.String()
}

func sameJump(a, b Jump) bool {
	if a.Mode != b.Mode {
		return false
	}
	if a.BastionID == nil || b.BastionID == nil {
		return a.BastionID == b.BastionID
	}
	return *a.BastionID == *b.BastionID
}

// jumpLabel is a jump host setting as the picture shows it: the mode, and
// the jump host's id when one is named.
func jumpLabel(j Jump) string {
	if j.BastionID != nil {
		return j.Mode + " " + j.BastionID.String()
	}
	return j.Mode
}
