package inventory

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
)

// Accepting an assistant's value (controller's refinement of D6): a
// person's save makes live only the values it changed, so a person who
// wants an assistant's value live as it is accepts it explicitly. The
// transport allows this to an interactive session only; the service
// trusts its caller to be a person. Each accept is revision-guarded, so it
// accepts exactly what the person saw.

// AcceptVars makes the group values under keys, each written by an
// assistant, live unchanged.
func (s *Service) AcceptVars(ctx context.Context, orgID, id uuid.UUID, revision int64, keys []string) (GroupView, error) {
	cur, err := s.store.GetGroup(ctx, orgID, id)
	if err != nil {
		return GroupView{}, err
	}
	next := cur
	if next.AssistantKeys, err = acceptMarks(cur.AssistantKeys, keys); err != nil {
		return GroupView{}, err
	}
	next.Revision = revision
	updated, err := s.store.UpdateGroup(ctx, next)
	if err != nil {
		return GroupView{}, err
	}
	return groupView(updated), nil
}

// AcceptDefaults makes the schema defaults of the fields keys, each set by
// an assistant, live unchanged.
func (s *Service) AcceptDefaults(ctx context.Context, orgID, id uuid.UUID, revision int64, keys []string) (GroupView, error) {
	cur, err := s.store.GetGroup(ctx, orgID, id)
	if err != nil {
		return GroupView{}, err
	}
	next := cur
	if next.AssistantDefaultKeys, err = acceptMarks(cur.AssistantDefaultKeys, keys); err != nil {
		return GroupView{}, err
	}
	next.Revision = revision
	updated, err := s.store.UpdateGroup(ctx, next)
	if err != nil {
		return GroupView{}, err
	}
	return groupView(updated), nil
}

// AcceptMemberVars makes the host's values in the group under keys, each
// written by an assistant, live unchanged.
func (s *Service) AcceptMemberVars(ctx context.Context, orgID, groupID, hostID uuid.UUID, revision int64, keys []string) (MemberView, error) {
	g, err := s.store.GetGroup(ctx, orgID, groupID)
	if err != nil {
		return MemberView{}, err
	}
	cur, err := s.store.GetMembership(ctx, orgID, groupID, hostID)
	if err != nil {
		return MemberView{}, err
	}
	next := cur
	if next.AssistantKeys, err = acceptMarks(cur.AssistantKeys, keys); err != nil {
		return MemberView{}, err
	}
	next.Revision = revision
	saved, err := s.store.UpdateMembership(ctx, next)
	if err != nil {
		return MemberView{}, err
	}
	h, err := s.hosts.GetHost(ctx, orgID, hostID)
	if errors.Is(err, hosts.ErrNotFound) {
		return MemberView{}, ErrNotFound
	}
	if err != nil {
		return MemberView{}, err
	}
	saved.HostName, saved.GroupName = h.Name, g.Name
	return memberView(g, saved), nil
}
