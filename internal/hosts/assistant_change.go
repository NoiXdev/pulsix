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

// updateWatched writes h (UpdateHostWith); an assistant's write that makes
// the host managed is handed on in the same transaction.
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
		if out, err = s.store.UpdateHostWith(ctx, h, x); err != nil {
			return err
		}
		return s.handOnManaged(ctx, actor, before, out, "managed", false, true)
	})
	if err != nil {
		return Host{}, err
	}
	return out, nil
}
