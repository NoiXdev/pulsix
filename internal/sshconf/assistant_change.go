package sshconf

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
)

// SetChangeWatcher wires the watcher an assistant's change of how runs
// reach a host is handed to (decision D7): it pauses what runs unattended
// there until a person reviews it. Without one nothing is handed on.
func (s *Service) SetChangeWatcher(w assistantchange.Watcher) { s.watcher = w }

func (s *Service) watches(actor assistantchange.Actor) bool {
	return actor.Assistant && s.watcher != nil
}

// SetHostSSHBy is SetHostSSH on actor's behalf: an assistant's change of
// the host's SSH user, port, credential or jump host is handed to the
// watcher in the write's transaction (decision D7), since every run that
// reaches the host now reaches it another way.
func (s *Service) SetHostSSHBy(ctx context.Context, orgID, hostID uuid.UUID, in HostSSHInput, actor assistantchange.Actor) (HostSSH, error) {
	if !s.watches(actor) {
		return s.SetHostSSH(ctx, orgID, hostID, in)
	}
	var out HostSSH
	err := s.store.InTx(ctx, orgID, func(ctx context.Context) error {
		before, err := s.HostSSH(ctx, orgID, hostID)
		if err != nil {
			return err
		}
		if out, err = s.SetHostSSH(ctx, orgID, hostID, in); err != nil {
			return err
		}
		fields := s.routeChanges(ctx, orgID, before, out)
		if len(fields) == 0 {
			return nil
		}
		name := hostID.String()
		if h, err := s.hosts.GetHost(ctx, orgID, hostID); err == nil {
			name = h.Name
		}
		c := assistantchange.New(assistantchange.SubjectHost, hostID, name, fields, actor)
		c.Reach = &assistantchange.Reach{Hosts: []uuid.UUID{hostID}}
		return s.watcher.Changed(ctx, orgID, c)
	})
	if err != nil {
		return HostSSH{}, err
	}
	return out, nil
}

// SetZoneBastionBy is SetZoneBastion on actor's behalf: an assistant's
// change of a zone's default jump host is handed to the watcher in the
// write's transaction (decision D7), reaching every managed host of the
// zone (a host or group with its own jump host included: fail closed).
func (s *Service) SetZoneBastionBy(ctx context.Context, orgID, zoneID uuid.UUID, bastionID *uuid.UUID, actor assistantchange.Actor) error {
	if !s.watches(actor) {
		return s.SetZoneBastion(ctx, orgID, zoneID, bastionID)
	}
	return s.store.InTx(ctx, orgID, func(ctx context.Context) error {
		zones, err := s.store.ZoneBastions(ctx, orgID)
		if err != nil {
			return err
		}
		var before *uuid.UUID
		if id, ok := zones[zoneID]; ok {
			before = &id
		}
		if err := s.SetZoneBastion(ctx, orgID, zoneID, bastionID); err != nil {
			return err
		}
		if sameID(before, bastionID) {
			return nil
		}
		all, err := s.hosts.ListScopeHosts(ctx, orgID)
		if err != nil {
			return fmt.Errorf("list the zone's hosts: %w", err)
		}
		var reached []uuid.UUID
		for _, h := range all {
			if h.ZoneID == zoneID && h.Managed {
				reached = append(reached, h.ID)
			}
		}
		if len(reached) == 0 {
			return nil
		}
		fields := []assistantchange.Field{{Name: "jump_host", Old: s.bastionLabel(ctx, orgID, before), New: s.bastionLabel(ctx, orgID, bastionID)}}
		c := assistantchange.New(assistantchange.SubjectZone, zoneID, s.zoneLabel(ctx, orgID, zoneID), fields, actor)
		c.Reach = &assistantchange.Reach{Hosts: reached}
		return s.watcher.Changed(ctx, orgID, c)
	})
}

// routeChanges lists what of a host's SSH settings moved from before to
// after, as a person reads it.
func (s *Service) routeChanges(ctx context.Context, orgID uuid.UUID, before, after HostSSH) []assistantchange.Field {
	var out []assistantchange.Field
	if before.Username != after.Username {
		out = append(out, assistantchange.Field{Name: "ssh_user", Old: optional(before.Username), New: optional(after.Username)})
	}
	if before.Port != after.Port {
		out = append(out, assistantchange.Field{Name: "ssh_port", Old: before.Port, New: after.Port})
	}
	if !sameID(before.CredentialID, after.CredentialID) {
		out = append(out, assistantchange.Field{Name: "ssh_credential", Old: s.credentialLabel(ctx, orgID, before.CredentialID), New: s.credentialLabel(ctx, orgID, after.CredentialID)})
	}
	if before.BastionMode != after.BastionMode || !sameID(before.BastionID, after.BastionID) {
		out = append(out, assistantchange.Field{Name: "jump_host",
			Old: jumpLabel(before.BastionMode, s.bastionLabel(ctx, orgID, before.BastionID)),
			New: jumpLabel(after.BastionMode, s.bastionLabel(ctx, orgID, after.BastionID))})
	}
	return out
}

func jumpLabel(mode BastionMode, bastion any) any {
	if bastion == nil {
		return string(mode)
	}
	return fmt.Sprintf("%s %v", mode, bastion)
}

func (s *Service) bastionLabel(ctx context.Context, orgID uuid.UUID, id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	if b, err := s.store.GetBastion(ctx, orgID, *id); err == nil {
		return b.Name
	}
	return id.String()
}

func (s *Service) credentialLabel(ctx context.Context, orgID uuid.UUID, id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	if c, err := s.creds.Get(ctx, orgID, *id); err == nil {
		return c.Name
	}
	return id.String()
}

// zoneLabel names a zone by its slug, or its id when it cannot be read.
func (s *Service) zoneLabel(ctx context.Context, orgID, id uuid.UUID) string {
	if zones, err := s.hosts.ListZones(ctx, orgID); err == nil {
		for _, z := range zones {
			if z.ID == id {
				return z.Slug
			}
		}
	}
	return id.String()
}

func optional(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func sameID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
