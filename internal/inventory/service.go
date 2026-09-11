// Package inventory implements Ansible inventory groups (spec §6, backlog
// 2.1-2.6): a group tree per organization, a typed variable schema per
// group, group-scoped values on the group, host-scoped values and overrides
// on each membership, and the renderer service that turns all of it into an
// Ansible dynamic inventory. Validation is internal/inventory/schema's;
// structure is internal/inventory/grouptree's; rendering is
// internal/inventory/render's. This package wires them to storage and seals
// secret values per row.
package inventory

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory/grouptree"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/sshconf"
)

// Sentinel errors mapped to HTTP status by the transport layer. Invalid
// values come back as *schema.ValidationError instead, so the API can point
// at each field.
var (
	ErrInvalidInput     = errors.New("invalid input")
	ErrNotFound         = errors.New("not found")
	ErrNameTaken        = errors.New("name already in use")
	ErrHasChildren      = errors.New("group has child groups")
	ErrRevisionMismatch = errors.New("revision does not match")
	ErrNotManaged       = errors.New("host is not managed")
	// ErrUnrenderable means the stored inventory cannot be rendered; the
	// wrapping error names the group or host and the reason, never a secret.
	ErrUnrenderable = render.ErrUnrenderable
)

// MaxGroupDepth is the number of levels a group tree may have.
const MaxGroupDepth = 10

const maxDescription = 1024

var groupNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// reservedGroupNames are Ansible's implicit groups.
var reservedGroupNames = map[string]bool{"all": true, "ungrouped": true}

// Group is an inventory group. Inside the store, Vars carries sealed
// secrets; every value this package returns to callers has them masked.
type Group struct {
	ID          uuid.UUID
	OrgID       uuid.UUID
	ParentID    *uuid.UUID
	Name        string
	Description string
	Schema      schema.Schema
	Vars        map[string]any
	BastionID   *uuid.UUID
	Revision    int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Membership assigns a managed host to a group with its host-scoped values
// and overrides. HostName and GroupName are filled by the list queries.
type Membership struct {
	GroupID   uuid.UUID
	HostID    uuid.UUID
	OrgID     uuid.UUID
	HostName  string
	GroupName string
	Vars      map[string]any
	Revision  int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// GroupInput creates a group.
type GroupInput struct {
	Name        string
	Description string
	ParentID    *uuid.UUID
}

// GroupPatch fully replaces a group's name, description, parent and jump
// host. A nil ParentID makes it a root; a nil BastionID clears the jump host.
type GroupPatch struct {
	Name        string
	Description string
	ParentID    *uuid.UUID
	BastionID   *uuid.UUID
}

// GroupView is a group as a reader sees it: vars masked, plus what is wrong
// with the stored values against the current schema.
type GroupView struct {
	Group
	Problems []schema.Problem
	Orphaned []string
}

// GroupSummary is a list entry.
type GroupSummary struct {
	GroupView
	Members int
}

// MemberView is a membership as a reader sees it.
type MemberView struct {
	Membership
	Complete bool
	Problems []schema.Problem
	Orphaned []string
}

// Store persists groups and memberships. Lookups that find nothing return
// ErrNotFound; UpdateGroup and UpdateMembership write only when the given
// Revision is current and return ErrRevisionMismatch otherwise.
type Store interface {
	CreateGroup(ctx context.Context, g Group) (Group, error)
	GetGroup(ctx context.Context, orgID, id uuid.UUID) (Group, error)
	ListGroups(ctx context.Context, orgID uuid.UUID) ([]Group, error)
	UpdateGroup(ctx context.Context, g Group) (Group, error)
	// DeleteGroup returns ErrHasChildren while child groups exist.
	DeleteGroup(ctx context.Context, orgID, id uuid.UUID) error
	MemberCounts(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID]int, error)
	// PutMembership creates the membership or replaces its vars.
	PutMembership(ctx context.Context, m Membership) (Membership, error)
	UpdateMembership(ctx context.Context, m Membership) (Membership, error)
	GetMembership(ctx context.Context, orgID, groupID, hostID uuid.UUID) (Membership, error)
	DeleteMembership(ctx context.Context, orgID, groupID, hostID uuid.UUID) error
	ListMemberships(ctx context.Context, orgID uuid.UUID) ([]Membership, error)
	ListGroupMembers(ctx context.Context, orgID, groupID uuid.UUID) ([]Membership, error)
	ListHostMemberships(ctx context.Context, orgID, hostID uuid.UUID) ([]Membership, error)
}

// HostLookup is the slice of hosts.Service this package needs.
type HostLookup interface {
	GetHost(ctx context.Context, orgID, id uuid.UUID) (hosts.Host, error)
}

// BastionLookup is the slice of sshconf.Service this package needs.
type BastionLookup interface {
	GetBastion(ctx context.Context, orgID, id uuid.UUID) (sshconf.Bastion, error)
}

// Sealer is the slice of crypto.Box this package needs.
type Sealer interface {
	SealAAD(plain, aad []byte) ([]byte, error)
	OpenAAD(sealed, aad []byte) ([]byte, error)
}

// Service implements inventory groups and memberships.
type Service struct {
	store    Store
	hosts    HostLookup
	bastions BastionLookup
	box      Sealer
}

// NewService wires the store, the lookups and the sealing box.
func NewService(store Store, hostLookup HostLookup, bastions BastionLookup, box Sealer) *Service {
	return &Service{store: store, hosts: hostLookup, bastions: bastions, box: box}
}

// CreateGroup creates an empty group, optionally below a parent.
func (s *Service) CreateGroup(ctx context.Context, orgID uuid.UUID, in GroupInput) (GroupView, error) {
	if err := validateGroupMeta(in.Name, in.Description); err != nil {
		return GroupView{}, err
	}
	if in.ParentID != nil {
		t, err := s.tree(ctx, orgID)
		if err != nil {
			return GroupView{}, err
		}
		if !t.Has(*in.ParentID) {
			return GroupView{}, fmt.Errorf("%w: parent group does not exist", ErrInvalidInput)
		}
		if t.Depth(*in.ParentID)+2 > MaxGroupDepth {
			return GroupView{}, errTooDeep()
		}
	}
	g := Group{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, ParentID: in.ParentID, Name: in.Name, Description: in.Description,
		Schema: schema.Schema{Fields: []schema.Field{}}, Vars: map[string]any{},
	}
	created, err := s.store.CreateGroup(ctx, g)
	if err != nil {
		return GroupView{}, err
	}
	return groupView(created), nil
}

// GetGroup returns one group with masked vars and its value problems.
func (s *Service) GetGroup(ctx context.Context, orgID, id uuid.UUID) (GroupView, error) {
	g, err := s.store.GetGroup(ctx, orgID, id)
	if err != nil {
		return GroupView{}, err
	}
	return groupView(g), nil
}

// ListGroups returns every group, ordered by name, with its member count.
func (s *Service) ListGroups(ctx context.Context, orgID uuid.UUID) ([]GroupSummary, error) {
	groups, err := s.store.ListGroups(ctx, orgID)
	if err != nil {
		return nil, err
	}
	counts, err := s.store.MemberCounts(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]GroupSummary, len(groups))
	for i, g := range groups {
		out[i] = GroupSummary{GroupView: groupView(g), Members: counts[g.ID]}
	}
	return out, nil
}

// UpdateGroup renames, moves, describes and sets the jump host of a group.
// A move may not place a group below itself and may not make the tree
// deeper than MaxGroupDepth.
func (s *Service) UpdateGroup(ctx context.Context, orgID, id uuid.UUID, revision int64, in GroupPatch) (GroupView, error) {
	if err := validateGroupMeta(in.Name, in.Description); err != nil {
		return GroupView{}, err
	}
	cur, err := s.store.GetGroup(ctx, orgID, id)
	if err != nil {
		return GroupView{}, err
	}
	t, err := s.tree(ctx, orgID)
	if err != nil {
		return GroupView{}, err
	}
	levels := 1 + t.Height(id)
	if in.ParentID != nil {
		p := *in.ParentID
		switch {
		case p == id || t.IsDescendant(p, id):
			return GroupView{}, fmt.Errorf("%w: a group cannot move below itself", ErrInvalidInput)
		case !t.Has(p):
			return GroupView{}, fmt.Errorf("%w: parent group does not exist", ErrInvalidInput)
		}
		levels += t.Depth(p) + 1
	}
	if levels > MaxGroupDepth {
		return GroupView{}, errTooDeep()
	}
	if in.BastionID != nil {
		if _, err := s.bastions.GetBastion(ctx, orgID, *in.BastionID); errors.Is(err, sshconf.ErrNotFound) {
			return GroupView{}, fmt.Errorf("%w: jump host does not exist", ErrInvalidInput)
		} else if err != nil {
			return GroupView{}, err
		}
	}
	next := cur
	next.Name, next.Description, next.ParentID, next.BastionID, next.Revision = in.Name, in.Description, in.ParentID, in.BastionID, revision
	updated, err := s.store.UpdateGroup(ctx, next)
	if err != nil {
		return GroupView{}, err
	}
	return groupView(updated), nil
}

// DeleteGroup removes a group without children, and its memberships.
func (s *Service) DeleteGroup(ctx context.Context, orgID, id uuid.UUID) error {
	return s.store.DeleteGroup(ctx, orgID, id)
}

// SetSchema replaces a group's schema. Stored values are never rewritten;
// the second result counts the members whose values no longer satisfy the
// new schema.
func (s *Service) SetSchema(ctx context.Context, orgID, id uuid.UUID, revision int64, sc schema.Schema) (GroupView, int, error) {
	if sc.Fields == nil {
		sc.Fields = []schema.Field{}
	}
	if err := sc.Check(); err != nil {
		return GroupView{}, 0, err
	}
	cur, err := s.store.GetGroup(ctx, orgID, id)
	if err != nil {
		return GroupView{}, 0, err
	}
	next := cur
	next.Schema, next.Revision = sc, revision
	updated, err := s.store.UpdateGroup(ctx, next)
	if err != nil {
		return GroupView{}, 0, err
	}
	members, err := s.store.ListGroupMembers(ctx, orgID, id)
	if err != nil {
		return GroupView{}, 0, err
	}
	incomplete := 0
	for _, m := range members {
		if schema.ValidateStored(sc.MembershipFields(), m.Vars) != nil {
			incomplete++
		}
	}
	return groupView(updated), incomplete, nil
}

// SetVars replaces a group's group-scoped values after strict validation.
// Plaintext secrets are sealed; keep markers keep the stored secret.
func (s *Service) SetVars(ctx context.Context, orgID, id uuid.UUID, revision int64, vars map[string]any) (GroupView, error) {
	cur, err := s.store.GetGroup(ctx, orgID, id)
	if err != nil {
		return GroupView{}, err
	}
	sealed, err := s.prepareVars(GroupAAD(orgID, id), cur.Schema.GroupFields(), vars, cur.Vars)
	if err != nil {
		return GroupView{}, err
	}
	next := cur
	next.Vars, next.Revision = sealed, revision
	updated, err := s.store.UpdateGroup(ctx, next)
	if err != nil {
		return GroupView{}, err
	}
	return groupView(updated), nil
}

func (s *Service) tree(ctx context.Context, orgID uuid.UUID) (grouptree.Tree, error) {
	groups, err := s.store.ListGroups(ctx, orgID)
	if err != nil {
		return grouptree.Tree{}, err
	}
	return treeOf(groups), nil
}

func treeOf(groups []Group) grouptree.Tree {
	nodes := make([]grouptree.Node, len(groups))
	for i, g := range groups {
		nodes[i] = grouptree.Node{ID: g.ID, ParentID: g.ParentID, Name: g.Name}
	}
	return grouptree.New(nodes)
}

func validateGroupMeta(name, description string) error {
	if reservedGroupNames[name] {
		return fmt.Errorf("%w: %q is reserved by Ansible", ErrInvalidInput, name)
	}
	if !groupNamePattern.MatchString(name) {
		return fmt.Errorf("%w: group names start with a lowercase letter or underscore and contain only lowercase letters, digits and underscores (at most 63)", ErrInvalidInput)
	}
	if utf8.RuneCountInString(description) > maxDescription {
		return fmt.Errorf("%w: description must be at most %d characters", ErrInvalidInput, maxDescription)
	}
	return nil
}

func errTooDeep() error {
	return fmt.Errorf("%w: group trees have at most %d levels", ErrInvalidInput, MaxGroupDepth)
}

func groupView(g Group) GroupView {
	fields := g.Schema.GroupFields()
	v := GroupView{Group: g, Orphaned: schema.Orphans(fields, g.Vars)}
	v.Vars = schema.MaskVars(g.Vars)
	if err := schema.ValidateStored(fields, g.Vars); err != nil {
		var ve *schema.ValidationError
		if errors.As(err, &ve) {
			v.Problems = ve.Problems
		}
	}
	return v
}
