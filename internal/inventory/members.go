package inventory

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// Assign is AssignBy for a person.
func (s *Service) Assign(ctx context.Context, orgID, groupID, hostID uuid.UUID, vars map[string]any, revision *int64) (MemberView, error) {
	return s.AssignBy(ctx, orgID, groupID, hostID, vars, revision, assistantchange.Actor{})
}

// AssignBy creates or replaces a host's membership in a group on actor's
// behalf. The host must be managed. vars may carry the group's host-scoped
// fields and overrides of its overridable group-scoped fields. A non-nil
// revision makes the write conditional on the stored membership's
// revision. An assistant's write marks the keys it set or changed and may
// not set one Ansible or Pulsix act on; a person's clears the marks of the
// values it changes (decision D6, marks.go). An assistant's write that
// moves a value of an existing membership is handed to the watcher in the
// write's transaction; a host joining or leaving a group is not.
func (s *Service) AssignBy(ctx context.Context, orgID, groupID, hostID uuid.UUID, vars map[string]any, revision *int64, actor assistantchange.Actor) (MemberView, error) {
	var result MemberView
	err := s.store.InTx(ctx, orgID, func(ctx context.Context) error {
		g, err := s.store.GetGroup(ctx, orgID, groupID)
		if err != nil {
			return err
		}
		h, err := s.hosts.GetHost(ctx, orgID, hostID)
		if errors.Is(err, hosts.ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !h.Managed {
			return ErrNotManaged
		}
		prev, err := s.store.GetMembership(ctx, orgID, groupID, hostID)
		exists := err == nil
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if revision != nil && (!exists || prev.Revision != *revision) {
			return ErrRevisionMismatch
		}
		sealed, err := s.prepareVars(MemberAAD(orgID, groupID, hostID), g.Schema.MembershipFields(), vars, prev.Vars)
		if err != nil {
			return err
		}
		changed := ChangedKeys(prev.Vars, sealed)
		if actor.Assistant {
			if err := checkAssistantValues(sealed, changed); err != nil {
				return err
			}
		}
		m := Membership{GroupID: groupID, HostID: hostID, OrgID: orgID, Vars: sealed,
			AssistantKeys: valueMarks(prev.AssistantKeys, sealed, changed, actor)}
		var saved Membership
		if revision != nil {
			m.Revision = *revision
			saved, err = s.store.UpdateMembership(ctx, m)
		} else {
			saved, err = s.store.PutMembership(ctx, m)
		}
		if err != nil {
			return err
		}
		saved.HostName, saved.GroupName = h.Name, g.Name
		result = memberView(g, saved)
		if !exists {
			// A host joining a group pauses nothing, whatever values it
			// joins with (spec C2): they are marked plain text all the
			// same.
			return nil
		}
		return s.handOnMembership(ctx, orgID, actor, g, hostID, h.Name, valueChanges(g.Schema.MembershipFields(), prev.Vars, sealed))
	})
	if err != nil {
		return MemberView{}, err
	}
	return result, nil
}

// Unassign removes a host from a group.
func (s *Service) Unassign(ctx context.Context, orgID, groupID, hostID uuid.UUID) error {
	return s.store.DeleteMembership(ctx, orgID, groupID, hostID)
}

// ListMemberships returns every membership of the organization, unfiltered.
// The job queue's scope resolver (internal/jobs) reads it to walk a scope
// group's subtree.
func (s *Service) ListMemberships(ctx context.Context, orgID uuid.UUID) ([]Membership, error) {
	return s.store.ListMemberships(ctx, orgID)
}

// Members lists a group's members by host name, each with its completeness
// against the current schema.
func (s *Service) Members(ctx context.Context, orgID, groupID uuid.UUID) ([]MemberView, error) {
	g, err := s.store.GetGroup(ctx, orgID, groupID)
	if err != nil {
		return nil, err
	}
	ms, err := s.store.ListGroupMembers(ctx, orgID, groupID)
	if err != nil {
		return nil, err
	}
	out := make([]MemberView, len(ms))
	for i, m := range ms {
		m.GroupName = g.Name
		out[i] = memberView(g, m)
	}
	return out, nil
}

// HostGroups lists the groups a host is a direct member of, by group name.
func (s *Service) HostGroups(ctx context.Context, orgID, hostID uuid.UUID) ([]MemberView, error) {
	if _, err := s.hosts.GetHost(ctx, orgID, hostID); errors.Is(err, hosts.ErrNotFound) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	ms, err := s.store.ListHostMemberships(ctx, orgID, hostID)
	if err != nil {
		return nil, err
	}
	groups, err := s.store.ListGroups(ctx, orgID)
	if err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]Group, len(groups))
	for _, g := range groups {
		byID[g.ID] = g
	}
	out := make([]MemberView, len(ms))
	for i, m := range ms {
		out[i] = memberView(byID[m.GroupID], m)
	}
	return out, nil
}

func memberView(g Group, m Membership) MemberView {
	fields := g.Schema.MembershipFields()
	v := MemberView{Membership: m, Complete: true, Orphaned: schema.Orphans(fields, m.Vars)}
	v.Vars = schema.MaskDeclared(fields, m.Vars)
	if err := schema.ValidateStored(fields, m.Vars); err != nil {
		var ve *schema.ValidationError
		if errors.As(err, &ve) {
			v.Complete, v.Problems = false, ve.Problems
		}
	}
	return v
}
