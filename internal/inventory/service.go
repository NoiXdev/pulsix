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

	"github.com/noixdev/pulsix/internal/assistantchange"
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

// reservedGroupNames are Ansible's implicit groups and the dynamic
// inventory's hostvars block, which a group of that name would overwrite.
var reservedGroupNames = map[string]bool{"all": true, "ungrouped": true, "_meta": true}

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
	// AssistantKeys names the keys of Vars an assistant wrote and
	// AssistantDefaultKeys the fields whose schema default an assistant
	// set (decision D6, marks.go): the renderer emits them as plain text
	// until a person changes them or accepts them (marks_accept.go).
	AssistantKeys        []string
	AssistantDefaultKeys []string
	Revision             int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
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
	// AssistantKeys names the keys of Vars an assistant wrote (marks.go).
	AssistantKeys []string
	Revision      int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
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
	// PutMembership creates the membership or replaces its vars; inserted
	// says it was created (a host joining the group).
	PutMembership(ctx context.Context, m Membership) (saved Membership, inserted bool, err error)
	// GetMembershipForUpdate is GetMembership with the row locked until
	// the transaction (InTx) ends.
	GetMembershipForUpdate(ctx context.Context, orgID, groupID, hostID uuid.UUID) (Membership, error)
	UpdateMembership(ctx context.Context, m Membership) (Membership, error)
	GetMembership(ctx context.Context, orgID, groupID, hostID uuid.UUID) (Membership, error)
	DeleteMembership(ctx context.Context, orgID, groupID, hostID uuid.UUID) error
	ListMemberships(ctx context.Context, orgID uuid.UUID) ([]Membership, error)
	ListGroupMembers(ctx context.Context, orgID, groupID uuid.UUID) ([]Membership, error)
	ListHostMemberships(ctx context.Context, orgID, hostID uuid.UUID) ([]Membership, error)
	// WithTreeLock runs fn against a Store bound to one transaction holding
	// orgID's group-tree advisory lock (backlog 2.15): a tree read inside fn
	// followed by a write inside the same fn call is atomic against any
	// concurrent WithTreeLock call for the same organization. Every write
	// that depends on a tree snapshot (a move's cycle/depth check, a
	// create's parent check, a delete) must run inside it.
	//
	// fn must call through txStore alone, never through the outer ctx, a
	// field on Service (s.bastions, s.hosts), or any other *db.DB-backed
	// store or service not bound to this transaction. A call through the
	// ambient ctx opens its own transaction on its own connection from
	// the same pool, while this transaction already holds one — with a
	// small pool and enough contention on the lock, that second
	// connection may never come free (every other connection is a caller
	// blocked on this same advisory lock, and none of them releases
	// theirs until the lock holder — which is now stuck waiting for a
	// connection — finishes), deadlocking the pool. Any reference this
	// closure needs to validate belongs either before WithTreeLock is
	// called (re-checked, if it matters, by the store's own constraints —
	// composite foreign keys, or a fresh read under the lock) or through
	// txStore's own methods.
	WithTreeLock(ctx context.Context, orgID uuid.UUID, fn func(Store) error) error
	// WithReadTx runs fn against a context bound to one repeatable-read
	// transaction scoped to orgID (backlog 2.16): every store built on the
	// same underlying *db.DB that reads through the ctx fn receives — not
	// just this Store — sees one consistent snapshot instead of one
	// transaction per statement. The renderer is the one caller that needs
	// this, since its reads span several domain stores.
	WithReadTx(ctx context.Context, orgID uuid.UUID, fn func(ctx context.Context) error) error
	// InTx runs fn inside one organization-scoped write transaction: every
	// store call fn makes with the context it is handed — this store's and
	// any other built on the same database — joins it, and all of it
	// commits together or none of it does. A values or schema write and
	// the pause an assistant's change causes (SetChangeWatcher) are one.
	// Not nested: fn must not call InTx again.
	InTx(ctx context.Context, orgID uuid.UUID, fn func(ctx context.Context) error) error
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
	// watcher is handed an assistant's change of the values a run
	// receives (assistant_change.go); nil hands on nothing.
	watcher assistantchange.Watcher
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
	g := Group{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, ParentID: in.ParentID, Name: in.Name, Description: in.Description,
		Schema: schema.Schema{Fields: []schema.Field{}}, Vars: map[string]any{},
		AssistantKeys: []string{}, AssistantDefaultKeys: []string{},
	}
	if in.ParentID == nil {
		// A root has nothing to check against the tree: no lock needed.
		created, err := s.store.CreateGroup(ctx, g)
		if err != nil {
			return GroupView{}, err
		}
		return groupView(created), nil
	}
	var result GroupView
	err := s.store.WithTreeLock(ctx, orgID, func(txStore Store) error {
		t, err := treeVia(ctx, txStore, orgID)
		if err != nil {
			return err
		}
		if !t.Has(*in.ParentID) {
			return fmt.Errorf("%w: parent group does not exist", ErrInvalidInput)
		}
		if t.Depth(*in.ParentID)+2 > MaxGroupDepth {
			return errTooDeep()
		}
		created, err := txStore.CreateGroup(ctx, g)
		if err != nil {
			return err
		}
		result = groupView(created)
		return nil
	})
	if err != nil {
		return GroupView{}, err
	}
	return result, nil
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

// UpdateGroup is UpdateGroupBy for a person.
func (s *Service) UpdateGroup(ctx context.Context, orgID, id uuid.UUID, revision int64, in GroupPatch) (GroupView, error) {
	return s.UpdateGroupBy(ctx, orgID, id, revision, in, assistantchange.Actor{})
}

// UpdateGroupBy renames, moves, describes and sets the jump host of a
// group on actor's behalf. A move may not place a group below itself and
// may not make the tree deeper than MaxGroupDepth. An assistant's move
// (a parent change), rename or jump host change is handed to the watcher
// in the update's transaction (decision D7): the moved subtree's hosts
// receive other ancestors' values and runs scoped to the old or the new
// ancestors reach other hosts; a rename changes the group names a run
// sees; a jump host changes how a run reaches the group's hosts.
func (s *Service) UpdateGroupBy(ctx context.Context, orgID, id uuid.UUID, revision int64, in GroupPatch, actor assistantchange.Actor) (GroupView, error) {
	if !s.watches(actor) {
		return s.updateGroup(ctx, orgID, id, revision, in, nil)
	}
	var result GroupView
	err := s.store.InTx(ctx, orgID, func(ctx context.Context) error {
		var moved *move
		var err error
		if result, err = s.updateGroup(ctx, orgID, id, revision, in, &moved); err != nil {
			return err
		}
		var fields []assistantchange.Field
		reach := &assistantchange.Reach{Group: &id}
		if moved.oldName != moved.newName {
			// group_names and groups[...] a run sees change (decision D7).
			fields = append(fields, assistantchange.Field{Name: "name", Old: moved.oldName, New: moved.newName})
		}
		if moved.parentMoved {
			fields = append(fields, assistantchange.Field{Name: "parent", Old: moved.oldParent, New: moved.newParent})
			reach.FormerGroups = moved.former
		}
		if !sameParent(moved.oldBastion, moved.newBastion) {
			fields = append(fields, assistantchange.Field{Name: "jump_host", Old: s.bastionLabel(ctx, orgID, moved.oldBastion), New: s.bastionLabel(ctx, orgID, moved.newBastion)})
		}
		return s.handOnGroup(ctx, orgID, actor, result.Group, fields, reach)
	})
	if err != nil {
		return GroupView{}, err
	}
	return result, nil
}

// move is what updateGroup tells a watched caller about the update: the
// old and the new name, whether the parent changed with the old and the
// new parent's names (nil for a root) and the groups above the group
// before the move, and the old and the new jump host.
type move struct {
	oldName, newName       string
	parentMoved            bool
	oldParent, newParent   any
	former                 []uuid.UUID
	oldBastion, newBastion *uuid.UUID
}

// bastionLabel names a jump host, or none.
func (s *Service) bastionLabel(ctx context.Context, orgID uuid.UUID, id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	if b, err := s.bastions.GetBastion(ctx, orgID, *id); err == nil {
		return b.Name
	}
	return id.String()
}

// updateGroup is UpdateGroupBy's write; with moved set it reports a
// parent change there.
func (s *Service) updateGroup(ctx context.Context, orgID, id uuid.UUID, revision int64, in GroupPatch, moved **move) (GroupView, error) {
	if err := validateGroupMeta(in.Name, in.Description); err != nil {
		return GroupView{}, err
	}
	// The group's existence and the bastion reference are both checked
	// before WithTreeLock, in that order (group-not-found still wins over
	// an invalid bastion), and neither runs inside the lock: s.bastions
	// is a cross-package call through the ambient ctx, not through
	// txStore, so it would take a second connection from the pool while
	// the locked transaction already holds one — see WithTreeLock's doc
	// comment for why that can deadlock the pool under contention. The
	// lock's own GetGroup below re-checks existence (a delete racing in
	// between still surfaces as ErrNotFound there) and the composite FK
	// on bastion_id still guards a bastion deleted in between; neither
	// pre-check needs to be repeated inside the lock for correctness.
	if _, err := s.store.GetGroup(ctx, orgID, id); err != nil {
		return GroupView{}, err
	}
	if in.BastionID != nil {
		if _, err := s.bastions.GetBastion(ctx, orgID, *in.BastionID); errors.Is(err, sshconf.ErrNotFound) {
			return GroupView{}, fmt.Errorf("%w: jump host does not exist", ErrInvalidInput)
		} else if err != nil {
			return GroupView{}, err
		}
	}
	var result GroupView
	err := s.store.WithTreeLock(ctx, orgID, func(txStore Store) error {
		cur, err := txStore.GetGroup(ctx, orgID, id)
		if err != nil {
			return err
		}
		t, err := treeVia(ctx, txStore, orgID)
		if err != nil {
			return err
		}
		levels := 1 + t.Height(id)
		if in.ParentID != nil {
			p := *in.ParentID
			switch {
			case p == id || t.IsDescendant(p, id):
				return fmt.Errorf("%w: a group cannot move below itself", ErrInvalidInput)
			case !t.Has(p):
				return fmt.Errorf("%w: parent group does not exist", ErrInvalidInput)
			}
			levels += t.Depth(p) + 1
		}
		if levels > MaxGroupDepth {
			return errTooDeep()
		}
		next := cur
		next.Name, next.Description, next.ParentID, next.BastionID, next.Revision = in.Name, in.Description, in.ParentID, in.BastionID, revision
		updated, err := txStore.UpdateGroup(ctx, next)
		if err != nil {
			return err
		}
		result = groupView(updated)
		if moved != nil {
			*moved = &move{oldName: cur.Name, newName: in.Name, oldBastion: cur.BastionID, newBastion: in.BastionID}
			if !sameParent(cur.ParentID, in.ParentID) {
				(*moved).parentMoved = true
				(*moved).oldParent, (*moved).newParent, (*moved).former = nameIn(t, cur.ParentID), nameIn(t, in.ParentID), t.Ancestors(id)
			}
		}
		return nil
	})
	if err != nil {
		return GroupView{}, err
	}
	return result, nil
}

// DeleteGroup is DeleteGroupBy for a person.
func (s *Service) DeleteGroup(ctx context.Context, orgID, id uuid.UUID) error {
	return s.DeleteGroupBy(ctx, orgID, id, assistantchange.Actor{})
}

// DeleteGroupBy removes a group without children, and its memberships, on
// actor's behalf. It runs under the same per-organization tree lock as
// CreateGroup and UpdateGroup (2.15) so it never races a concurrent move
// or create that reads this group's existence into a now-stale tree
// snapshot. An assistant's delete of a group with members is handed to the
// watcher in the delete's transaction (decision D7): its hosts leave it.
func (s *Service) DeleteGroupBy(ctx context.Context, orgID, id uuid.UUID, actor assistantchange.Actor) error {
	if !s.watches(actor) {
		return s.store.WithTreeLock(ctx, orgID, func(txStore Store) error {
			return txStore.DeleteGroup(ctx, orgID, id)
		})
	}
	return s.store.InTx(ctx, orgID, func(ctx context.Context) error {
		var g Group
		var members []Membership
		var former []uuid.UUID
		err := s.store.WithTreeLock(ctx, orgID, func(txStore Store) error {
			var err error
			if g, err = txStore.GetGroup(ctx, orgID, id); err != nil {
				return err
			}
			if members, err = txStore.ListGroupMembers(ctx, orgID, id); err != nil {
				return err
			}
			t, err := treeVia(ctx, txStore, orgID)
			if err != nil {
				return err
			}
			former = append([]uuid.UUID{id}, t.Ancestors(id)...)
			return txStore.DeleteGroup(ctx, orgID, id)
		})
		if err != nil || len(members) == 0 {
			return err
		}
		hostIDs := make([]uuid.UUID, len(members))
		names := make([]string, len(members))
		for i, m := range members {
			hostIDs[i], names[i] = m.HostID, m.HostName
		}
		fields := []assistantchange.Field{{Name: "members", Old: names, New: nil}}
		return s.handOnGroup(ctx, orgID, actor, g, fields, &assistantchange.Reach{Hosts: hostIDs, FormerGroups: former})
	})
}

// nameIn is the name of group id in t; nil for none.
func nameIn(t grouptree.Tree, id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	if n, ok := t.Node(*id); ok {
		return n.Name
	}
	return id.String()
}

// sameParent reports whether two parent references name the same group
// (nil: a root).
func sameParent(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// SetSchema is SetSchemaBy for a person.
func (s *Service) SetSchema(ctx context.Context, orgID, id uuid.UUID, revision int64, sc schema.Schema) (GroupView, int, error) {
	return s.SetSchemaBy(ctx, orgID, id, revision, sc, assistantchange.Actor{})
}

// SetSchemaBy replaces a group's schema on actor's behalf. Stored values
// are never rewritten; the second result counts the members whose values
// no longer satisfy the new schema. A group holds manual fields only. An
// assistant's save marks the fields whose defaults it set or changed and
// may neither add a field under a name Ansible or Pulsix act on nor set a
// default under one; a person's clears the marks of the defaults it
// changes (decision D6, marks.go). An assistant's save that moves a
// default, or a value a run receives (schemaChanges), is handed to the
// watcher in the save's transaction.
func (s *Service) SetSchemaBy(ctx context.Context, orgID, id uuid.UUID, revision int64, sc schema.Schema, actor assistantchange.Actor) (GroupView, int, error) {
	if sc.Fields == nil {
		sc.Fields = []schema.Field{}
	}
	if err := sc.Check(); err != nil {
		return GroupView{}, 0, err
	}
	if err := sc.CheckManualOnly(); err != nil {
		return GroupView{}, 0, err
	}
	var result GroupView
	incomplete := 0
	err := s.store.InTx(ctx, orgID, func(ctx context.Context) error {
		cur, err := s.store.GetGroup(ctx, orgID, id)
		if err != nil {
			return err
		}
		changed := ChangedDefaults(cur.Schema, sc)
		if actor.Assistant {
			if err := joinProblems(checkAssistantFieldNames(cur.Schema, sc), checkAssistantDefaults(sc, changed)); err != nil {
				return err
			}
		}
		next := cur
		next.Schema, next.Revision = sc, revision
		next.AssistantDefaultKeys = defaultMarks(cur.AssistantDefaultKeys, sc, changed, actor)
		updated, err := s.store.UpdateGroup(ctx, next)
		if err != nil {
			return err
		}
		members, err := s.store.ListGroupMembers(ctx, orgID, id)
		if err != nil {
			return err
		}
		for _, m := range members {
			if schema.ValidateStored(sc.MembershipFields(), m.Vars) != nil {
				incomplete++
			}
		}
		result = groupView(updated)
		return s.handOnGroup(ctx, orgID, actor, updated, schemaChanges(cur.Schema, sc, cur.Vars, members), nil)
	})
	if err != nil {
		return GroupView{}, 0, err
	}
	return result, incomplete, nil
}

// SetVars is SetVarsBy for a person.
func (s *Service) SetVars(ctx context.Context, orgID, id uuid.UUID, revision int64, vars map[string]any) (GroupView, error) {
	return s.SetVarsBy(ctx, orgID, id, revision, vars, assistantchange.Actor{})
}

// SetVarsBy replaces a group's group-scoped values on actor's behalf after
// strict validation. Plaintext secrets are sealed; keep markers keep the
// stored secret. An assistant's save marks the keys it set or changed and
// may not set one Ansible or Pulsix act on; a person's clears the marks of
// the values it changes (decision D6, marks.go). An assistant's save that
// moves a value is handed to the watcher in the save's transaction.
func (s *Service) SetVarsBy(ctx context.Context, orgID, id uuid.UUID, revision int64, vars map[string]any, actor assistantchange.Actor) (GroupView, error) {
	var result GroupView
	err := s.store.InTx(ctx, orgID, func(ctx context.Context) error {
		cur, err := s.store.GetGroup(ctx, orgID, id)
		if err != nil {
			return err
		}
		sealed, err := s.prepareVars(GroupAAD(orgID, id), cur.Schema.GroupFields(), vars, cur.Vars)
		if err != nil {
			return err
		}
		changed := ChangedKeys(cur.Vars, sealed)
		if actor.Assistant {
			if err := checkAssistantValues(sealed, changed); err != nil {
				return err
			}
		}
		next := cur
		next.Vars, next.Revision = sealed, revision
		next.AssistantKeys = valueMarks(cur.AssistantKeys, sealed, changed, actor)
		updated, err := s.store.UpdateGroup(ctx, next)
		if err != nil {
			return err
		}
		result = groupView(updated)
		return s.handOnGroup(ctx, orgID, actor, updated, valueChanges(cur.Schema.GroupFields(), cur.Vars, sealed), nil)
	})
	if err != nil {
		return GroupView{}, err
	}
	return result, nil
}

// treeVia reads st's groups and builds the structural tree from them. It
// takes a Store parameter (rather than being a Service method on s.store)
// so a caller inside WithTreeLock can pass the lock-bound txStore and get a
// tree snapshot consistent with the write it is about to perform.
func treeVia(ctx context.Context, st Store, orgID uuid.UUID) (grouptree.Tree, error) {
	groups, err := st.ListGroups(ctx, orgID)
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
	v.Vars = schema.MaskDeclared(fields, g.Vars)
	if err := schema.ValidateStored(fields, g.Vars); err != nil {
		var ve *schema.ValidationError
		if errors.As(err, &ve) {
			v.Problems = ve.Problems
		}
	}
	return v
}
