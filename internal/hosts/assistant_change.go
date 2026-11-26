package hosts

import (
	"context"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
)

// Route is how runs reach a host, as a person reads it: the SSH user and
// port, the credential's name, the jump host mode and the jump host's name
// ("" for none).
type Route struct {
	User       string
	Port       int
	Credential string
	JumpMode   string
	JumpHost   string
}

// SetChangeWatcher wires the watcher an assistant's host change that
// alters which hosts runs reach, or how, is handed to (decision D7): it
// pauses what runs unattended there until a person reviews it. Without one
// nothing is handed on.
func (s *Service) SetChangeWatcher(w assistantchange.Watcher) { s.watcher = w }

func (s *Service) watches(actor assistantchange.Actor) bool {
	return actor.Assistant && s.watcher != nil
}

// connectionTarget is where a run connects to h: its address, or its name
// when it has none (the inventory renders ansible_host so).
func connectionTarget(h Host) string {
	if h.Address != "" {
		return h.Address
	}
	return h.Name
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

// enteringFields is what the picture shows of a host that newly enters
// run scopes (created managed, or made managed): first what made it
// enter, then where and how runs reach it (final review N4).
func (s *Service) enteringFields(ctx context.Context, h Host, first assistantchange.Field) ([]assistantchange.Field, error) {
	r, err := s.store.GetRoute(ctx, h.OrgID, h.ID)
	if err != nil {
		return nil, err
	}
	return []assistantchange.Field{
		first,
		{Name: "address", Old: nil, New: connectionTarget(h)},
		{Name: "zone", Old: nil, New: s.zoneLabel(ctx, h.OrgID, h.ZoneID)},
		{Name: "ssh_user", Old: nil, New: optional(r.User)},
		{Name: "ssh_port", Old: nil, New: r.Port},
		{Name: "ssh_credential", Old: nil, New: optional(r.Credential)},
		{Name: "jump_host", Old: nil, New: routeJumpLabel(r)},
	}, nil
}

// handOnEntering hands on a host that newly enters run scopes.
func (s *Service) handOnEntering(ctx context.Context, actor assistantchange.Actor, h Host, first assistantchange.Field) error {
	if !s.watches(actor) {
		return nil
	}
	fields, err := s.enteringFields(ctx, h, first)
	if err != nil {
		return err
	}
	return s.handOnHost(ctx, actor, h, fields)
}

// updateWatched writes h (UpdateHostWith) for actor. For an assistant:
//   - A managed host's connection target (its address, or its name while
//     it has none) is a person's to change: ErrAssistantConnection
//     (controller's ruling N1). A rename of a managed host that has an
//     address is allowed and handed on: inventory_hostname, which roles
//     derive names from, changes.
//   - A managed host's rename, zone or jump host change is handed on
//     before the write: the reviewer locks the schedules and triggers
//     first, while this transaction holds the host row only FOR NO KEY
//     UPDATE, which a job insert's FOR KEY SHARE does not wait on. The
//     rename's own lock (it changes the unique name) comes after, so a
//     schedule tick that holds its rows while it enqueues for this host
//     never waits on this write while this write waits on the tick
//     (final review N2; C2's order: items, then requests, then this row).
//     A later revision mismatch rolls the pause back with the write.
//   - A host made managed is handed on after the write, with its route:
//     it was in no run before, so no tick holds anything for it.
func (s *Service) updateWatched(ctx context.Context, h Host, x HostExtras, actor assistantchange.Actor) (Host, error) {
	if !actor.Assistant {
		return s.store.UpdateHostWith(ctx, h, x)
	}
	var out Host
	err := s.store.InTx(ctx, h.OrgID, func(ctx context.Context) error {
		before, err := s.store.GetHostForUpdate(ctx, h.OrgID, h.ID)
		if err != nil {
			return err
		}
		if before.Revision != h.Revision {
			return ErrRevisionMismatch
		}
		if before.Managed && connectionTarget(before) != connectionTarget(h) {
			return ErrAssistantConnection
		}
		if before.Managed && h.Managed {
			fields, err := s.routeChanges(ctx, before, h, x)
			if err != nil {
				return err
			}
			if err := s.handOnHost(ctx, actor, h, fields); err != nil {
				return err
			}
		}
		if out, err = s.store.UpdateHostWith(ctx, h, x); err != nil {
			return err
		}
		if !before.Managed && out.Managed {
			return s.handOnEntering(ctx, actor, out, assistantchange.Field{Name: "managed", Old: false, New: true})
		}
		return nil
	})
	if err != nil {
		return Host{}, err
	}
	return out, nil
}

// routeChanges lists what of a managed host an update moves that changes
// which hosts runs reach or how: its name, zone and jump host.
func (s *Service) routeChanges(ctx context.Context, before, h Host, x HostExtras) ([]assistantchange.Field, error) {
	var fields []assistantchange.Field
	if before.Name != h.Name {
		fields = append(fields, assistantchange.Field{Name: "name", Old: before.Name, New: h.Name})
	}
	if before.ZoneID != h.ZoneID {
		fields = append(fields, assistantchange.Field{Name: "zone", Old: s.zoneLabel(ctx, h.OrgID, before.ZoneID), New: s.zoneLabel(ctx, h.OrgID, h.ZoneID)})
	}
	if x.Jump != nil {
		jumpBefore, err := s.store.GetJump(ctx, h.OrgID, h.ID)
		if err != nil {
			return nil, err
		}
		if !sameJump(jumpBefore, *x.Jump) {
			fields = append(fields, assistantchange.Field{Name: "jump_host", Old: jumpLabel(jumpBefore), New: jumpLabel(*x.Jump)})
		}
	}
	return fields, nil
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

// routeJumpLabel is a route's jump host: the mode, and the jump host's
// name when one is named.
func routeJumpLabel(r Route) string {
	if r.JumpHost != "" {
		return r.JumpMode + " " + r.JumpHost
	}
	return r.JumpMode
}

func optional(v string) any {
	if v == "" {
		return nil
	}
	return v
}
