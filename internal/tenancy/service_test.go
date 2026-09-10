package tenancy_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/tenancy"
)

// fakeStore is an in-memory tenancy.Store good enough to drive Service
// without a database. Every method is implemented literally against the
// maps below, mirroring the real SQL store's contract (ErrNotFound on a
// missing row, never swallowed).
type fakeStore struct {
	orgs         map[uuid.UUID]tenancy.Org
	orgsBySlug   map[string]uuid.UUID
	users        map[uuid.UUID]tenancy.User
	usersByEmail map[string]uuid.UUID
	memberships  map[[2]uuid.UUID]tenancy.Role
	invites      map[string]tenancy.Invite // keyed by hex-ish string of the hash
	disabledAt   map[uuid.UUID]time.Time
	revokedSess  map[uuid.UUID]bool
	revokedTok   map[uuid.UUID]bool

	// failNext, when set, makes the named method's next call return this
	// error instead of doing its normal thing — used to exercise a
	// propagated store failure the fakes can't otherwise produce.
	failNext map[string]error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		orgs: map[uuid.UUID]tenancy.Org{}, orgsBySlug: map[string]uuid.UUID{},
		users: map[uuid.UUID]tenancy.User{}, usersByEmail: map[string]uuid.UUID{},
		memberships: map[[2]uuid.UUID]tenancy.Role{}, invites: map[string]tenancy.Invite{},
		disabledAt: map[uuid.UUID]time.Time{}, revokedSess: map[uuid.UUID]bool{}, revokedTok: map[uuid.UUID]bool{},
		failNext: map[string]error{},
	}
}

func (f *fakeStore) addUser(email string, withPassword bool) uuid.UUID {
	id := uuid.Must(uuid.NewV7())
	u := tenancy.User{ID: id, Email: strings.ToLower(email), DisplayName: email}
	if withPassword {
		h := "$argon2id$fake"
		u.PasswordHash = &h
	}
	f.users[id] = u
	f.usersByEmail[u.Email] = id
	return id
}

func (f *fakeStore) addMember(orgID, userID uuid.UUID, role tenancy.Role) {
	f.memberships[[2]uuid.UUID{orgID, userID}] = role
}

func (f *fakeStore) take(method string) error {
	if err, ok := f.failNext[method]; ok {
		delete(f.failNext, method)
		return err
	}
	return nil
}

func (f *fakeStore) CreateOrg(_ context.Context, o tenancy.Org) error {
	if err := f.take("CreateOrg"); err != nil {
		return err
	}
	if _, taken := f.orgsBySlug[o.Slug]; taken {
		return tenancy.ErrSlugTaken
	}
	f.orgs[o.ID] = o
	f.orgsBySlug[o.Slug] = o.ID
	return nil
}

func (f *fakeStore) GetOrgBySlug(_ context.Context, slug string) (tenancy.Org, error) {
	id, ok := f.orgsBySlug[slug]
	if !ok {
		return tenancy.Org{}, tenancy.ErrNotFound
	}
	return f.orgs[id], nil
}

func (f *fakeStore) GetOrgByID(_ context.Context, id uuid.UUID) (tenancy.Org, error) {
	o, ok := f.orgs[id]
	if !ok {
		return tenancy.Org{}, tenancy.ErrNotFound
	}
	return o, nil
}

func (f *fakeStore) UpdateOrg(_ context.Context, o tenancy.Org) error {
	if err := f.take("UpdateOrg"); err != nil {
		return err
	}
	if _, ok := f.orgs[o.ID]; !ok {
		return tenancy.ErrNotFound
	}
	f.orgs[o.ID] = o
	return nil
}

func (f *fakeStore) ListOrgsForUser(_ context.Context, userID uuid.UUID) ([]tenancy.OrgMembership, error) {
	if err := f.take("ListOrgsForUser"); err != nil {
		return nil, err
	}
	var out []tenancy.OrgMembership
	for key, role := range f.memberships {
		if key[1] != userID {
			continue
		}
		org := f.orgs[key[0]]
		out = append(out, tenancy.OrgMembership{OrgID: org.ID, Slug: org.Slug, Name: org.Name, Role: role, CreatedAt: org.CreatedAt})
	}
	return out, nil
}

func (f *fakeStore) GetMembership(_ context.Context, orgID, userID uuid.UUID) (tenancy.Role, error) {
	if err := f.take("GetMembership"); err != nil {
		return "", err
	}
	role, ok := f.memberships[[2]uuid.UUID{orgID, userID}]
	if !ok {
		return "", tenancy.ErrNotFound
	}
	return role, nil
}

func (f *fakeStore) AddMembership(_ context.Context, orgID, userID uuid.UUID, role tenancy.Role) error {
	if err := f.take("AddMembership"); err != nil {
		return err
	}
	f.memberships[[2]uuid.UUID{orgID, userID}] = role
	return nil
}

func (f *fakeStore) UpdateMembershipRole(_ context.Context, orgID, userID uuid.UUID, role tenancy.Role) error {
	if err := f.take("UpdateMembershipRole"); err != nil {
		return err
	}
	f.memberships[[2]uuid.UUID{orgID, userID}] = role
	return nil
}

func (f *fakeStore) DeleteMembership(_ context.Context, orgID, userID uuid.UUID) error {
	if err := f.take("DeleteMembership"); err != nil {
		return err
	}
	delete(f.memberships, [2]uuid.UUID{orgID, userID})
	return nil
}

func (f *fakeStore) CountOwners(_ context.Context, orgID uuid.UUID) (int, error) {
	if err := f.take("CountOwners"); err != nil {
		return 0, err
	}
	n := 0
	for key, role := range f.memberships {
		if key[0] == orgID && role == tenancy.RoleOwner {
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) ListMembers(_ context.Context, orgID uuid.UUID) ([]tenancy.Member, error) {
	if err := f.take("ListMembers"); err != nil {
		return nil, err
	}
	var out []tenancy.Member
	for key, role := range f.memberships {
		if key[0] != orgID {
			continue
		}
		u := f.users[key[1]]
		m := tenancy.Member{UserID: u.ID, Email: u.Email, DisplayName: u.DisplayName, Role: role}
		if at, ok := f.disabledAt[u.ID]; ok {
			m.DisabledAt = &at
		}
		out = append(out, m)
	}
	return out, nil
}

func (f *fakeStore) GetUserByEmail(_ context.Context, email string) (tenancy.User, error) {
	if err := f.take("GetUserByEmail"); err != nil {
		return tenancy.User{}, err
	}
	id, ok := f.usersByEmail[strings.ToLower(email)]
	if !ok {
		return tenancy.User{}, tenancy.ErrNotFound
	}
	return f.users[id], nil
}

func (f *fakeStore) CreateUser(_ context.Context, u tenancy.User) error {
	if err := f.take("CreateUser"); err != nil {
		return err
	}
	f.users[u.ID] = u
	f.usersByEmail[u.Email] = u.ID
	return nil
}

func (f *fakeStore) SetUserPasswordAndName(_ context.Context, id uuid.UUID, hash, name string) error {
	if err := f.take("SetUserPasswordAndName"); err != nil {
		return err
	}
	u := f.users[id]
	u.PasswordHash = &hash
	u.DisplayName = name
	f.users[id] = u
	return nil
}

func (f *fakeStore) DisableUser(_ context.Context, id uuid.UUID, at time.Time) error {
	if err := f.take("DisableUser"); err != nil {
		return err
	}
	f.disabledAt[id] = at
	return nil
}

func (f *fakeStore) RevokeSessionsForUser(_ context.Context, id uuid.UUID) error {
	if err := f.take("RevokeSessionsForUser"); err != nil {
		return err
	}
	f.revokedSess[id] = true
	return nil
}

func (f *fakeStore) RevokeTokensForUser(_ context.Context, id uuid.UUID) error {
	if err := f.take("RevokeTokensForUser"); err != nil {
		return err
	}
	f.revokedTok[id] = true
	return nil
}

func (f *fakeStore) InsertInvite(_ context.Context, inv tenancy.Invite, tokenHash []byte) error {
	if err := f.take("InsertInvite"); err != nil {
		return err
	}
	f.invites[string(tokenHash)] = inv
	return nil
}

func (f *fakeStore) TakeInvite(_ context.Context, tokenHash []byte, now time.Time) (tenancy.Invite, error) {
	if err := f.take("TakeInvite"); err != nil {
		return tenancy.Invite{}, err
	}
	inv, ok := f.invites[string(tokenHash)]
	if !ok || now.After(inv.ExpiresAt) {
		return tenancy.Invite{}, tenancy.ErrNotFound
	}
	delete(f.invites, string(tokenHash)) // single-use: mark accepted by removing it
	return inv, nil
}

// fakeMailer records every Send call.
type fakeMailer struct {
	sent    []sentMail
	failErr error
}

type sentMail struct{ to, subject, body string }

func (f *fakeMailer) Send(_ context.Context, to, subject, body string) error {
	if f.failErr != nil {
		return f.failErr
	}
	f.sent = append(f.sent, sentMail{to, subject, body})
	return nil
}

const testPublicURL = "https://pulsix.test"

func TestInviteCreatesUserSendsMailAndAcceptJoins(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	mail := &fakeMailer{}
	now := time.Now()
	svc := tenancy.NewService(st, mail, testPublicURL, func() time.Time { return now })
	owner := st.addUser("owner@x", true)
	org, err := svc.CreateOrg(ctx, owner, "acme", "Acme")
	if err != nil {
		t.Fatal(err)
	}

	inv, err := svc.Invite(ctx, org.ID, owner, "new@x", tenancy.RoleOperator)
	if err != nil {
		t.Fatal(err)
	}
	if len(mail.sent) != 1 || !strings.Contains(mail.sent[0].body, inv.Token) {
		t.Fatalf("mail %+v", mail.sent)
	}
	uid, gotOrg, err := svc.AcceptInvite(ctx, inv.Token, "New User", "correct horse battery")
	if err != nil || gotOrg != org.ID {
		t.Fatalf("accept: %v", err)
	}
	role, err := svc.MembershipOf(ctx, org.ID, uid)
	if err != nil || role != tenancy.RoleOperator {
		t.Fatalf("role %s %v", role, err)
	}
	if _, _, err := svc.AcceptInvite(ctx, inv.Token, "x", "correct horse battery"); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("invite reused: %v", err)
	}
}

func TestAcceptInviteExistingAccountKeepsPassword(t *testing.T) {
	// Safety property: an invite token proves control of a mailbox, not
	// knowledge of an existing account's password. Accepting an invite sent
	// to an address that already belongs to a registered user must not let
	// whoever holds the token overwrite that account's credentials or
	// create a second, duplicate account.
	ctx := context.Background()
	st := newFakeStore()
	mail := &fakeMailer{}
	svc := tenancy.NewService(st, mail, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	org, err := svc.CreateOrg(ctx, owner, "acme", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	existing := st.addUser("existing@x", true)
	originalHash := *st.users[existing].PasswordHash
	originalName := st.users[existing].DisplayName

	inv, err := svc.Invite(ctx, org.ID, owner, "existing@x", tenancy.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}

	uid, gotOrg, err := svc.AcceptInvite(ctx, inv.Token, "Attacker-Chosen Name", "attacker-chosen-password")
	if err != nil {
		t.Fatal(err)
	}
	if uid != existing || gotOrg != org.ID {
		t.Fatalf("accept created/joined the wrong user: got %s, want %s", uid, existing)
	}
	if len(st.users) != 2 { // owner + existing, no third user created
		t.Fatalf("accept created a duplicate user: %d users", len(st.users))
	}
	if *st.users[existing].PasswordHash != originalHash {
		t.Fatal("accept overwrote an existing account's password")
	}
	if st.users[existing].DisplayName != originalName {
		t.Fatal("accept overwrote an existing account's display name")
	}
	role, err := svc.MembershipOf(ctx, org.ID, existing)
	if err != nil || role != tenancy.RoleViewer {
		t.Fatalf("membership not added: role=%s err=%v", role, err)
	}
}

func TestAcceptInviteUnknownTokenNotFound(t *testing.T) {
	ctx := context.Background()
	svc := tenancy.NewService(newFakeStore(), &fakeMailer{}, testPublicURL, time.Now)
	if _, _, err := svc.AcceptInvite(ctx, "not-a-real-token", "x", "correct horse battery"); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("unknown token: %v, want ErrNotFound", err)
	}
}

func TestInviteExpires(t *testing.T) {
	now := time.Now()
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, func() time.Time { return now })
	owner := st.addUser("owner@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	inv, _ := svc.Invite(ctx, org.ID, owner, "late@x", tenancy.RoleViewer)
	now = now.Add(8 * 24 * time.Hour)
	if _, _, err := svc.AcceptInvite(ctx, inv.Token, "Late", "correct horse battery"); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("expired invite: %v", err)
	}
}

func TestInviteRejectsOwnerRole(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	if _, err := svc.Invite(ctx, org.ID, owner, "new@x", tenancy.RoleOwner); !errors.Is(err, tenancy.ErrForbidden) {
		t.Fatalf("invite owner: %v, want ErrForbidden", err)
	}
}

func TestInviteRejectsUnknownRole(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	if _, err := svc.Invite(ctx, org.ID, owner, "new@x", tenancy.Role("superuser")); !errors.Is(err, tenancy.ErrForbidden) {
		t.Fatalf("invite bogus role: %v, want ErrForbidden", err)
	}
}

func TestInvitePropagatesMailFailure(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	mail := &fakeMailer{failErr: errors.New("smtp down")}
	svc := tenancy.NewService(st, mail, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	if _, err := svc.Invite(ctx, org.ID, owner, "new@x", tenancy.RoleViewer); err == nil {
		t.Fatal("expected mail failure to propagate")
	}
}

func TestLastOwnerCannotBeDemotedOrRemoved(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	if err := svc.ChangeRole(ctx, org.ID, owner, owner, tenancy.RoleAdmin); !errors.Is(err, tenancy.ErrLastOwner) {
		t.Fatalf("demote: %v", err)
	}
	if err := svc.RemoveMember(ctx, org.ID, owner, owner); !errors.Is(err, tenancy.ErrLastOwner) {
		t.Fatalf("remove: %v", err)
	}
}

func TestOnlyOwnerCanGrantOwner(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	admin := st.addUser("admin@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	st.addMember(org.ID, admin, tenancy.RoleAdmin)
	if err := svc.ChangeRole(ctx, org.ID, admin, admin, tenancy.RoleOwner); !errors.Is(err, tenancy.ErrForbidden) {
		t.Fatalf("admin self-promote: %v", err)
	}
	if err := svc.ChangeRole(ctx, org.ID, owner, admin, tenancy.RoleOwner); err != nil {
		t.Fatalf("owner promote: %v", err)
	}
}

func TestOnlyOwnerCanRemoveOwner(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	secondOwner := st.addUser("owner2@x", true)
	admin := st.addUser("admin@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	st.addMember(org.ID, secondOwner, tenancy.RoleOwner)
	st.addMember(org.ID, admin, tenancy.RoleAdmin)
	if err := svc.RemoveMember(ctx, org.ID, admin, secondOwner); !errors.Is(err, tenancy.ErrForbidden) {
		t.Fatalf("admin removes owner: %v, want ErrForbidden", err)
	}
	if err := svc.RemoveMember(ctx, org.ID, owner, secondOwner); err != nil {
		t.Fatalf("owner removes owner: %v", err)
	}
}

func TestRemoveNonOwnerMember(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	viewer := st.addUser("viewer@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	st.addMember(org.ID, viewer, tenancy.RoleViewer)
	if err := svc.RemoveMember(ctx, org.ID, owner, viewer); err != nil {
		t.Fatalf("remove viewer: %v", err)
	}
	if _, err := svc.MembershipOf(ctx, org.ID, viewer); !errors.Is(err, tenancy.ErrNotMember) {
		t.Fatalf("membership after remove: %v, want ErrNotMember", err)
	}
}

func TestChangeRoleActorNotMember(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	outsider := st.addUser("outsider@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	if err := svc.ChangeRole(ctx, org.ID, outsider, owner, tenancy.RoleAdmin); !errors.Is(err, tenancy.ErrNotMember) {
		t.Fatalf("non-member actor: %v, want ErrNotMember", err)
	}
}

func TestChangeRoleTargetNotFound(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	stranger := st.addUser("stranger@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	if err := svc.ChangeRole(ctx, org.ID, owner, stranger, tenancy.RoleAdmin); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("unknown target: %v, want ErrNotFound", err)
	}
}

func TestChangeRoleRejectsUnknownRole(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	if err := svc.ChangeRole(ctx, org.ID, owner, owner, tenancy.Role("superuser")); !errors.Is(err, tenancy.ErrForbidden) {
		t.Fatalf("bogus role: %v, want ErrForbidden", err)
	}
}

func TestChangeRoleNonOwnerDemotion(t *testing.T) {
	// A regular role change between two non-owner roles by an admin actor
	// must succeed without touching the last-owner guard.
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	admin := st.addUser("admin@x", true)
	operator := st.addUser("operator@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	st.addMember(org.ID, admin, tenancy.RoleAdmin)
	st.addMember(org.ID, operator, tenancy.RoleOperator)
	if err := svc.ChangeRole(ctx, org.ID, admin, operator, tenancy.RoleViewer); err != nil {
		t.Fatalf("admin demotes operator: %v", err)
	}
	role, err := svc.MembershipOf(ctx, org.ID, operator)
	if err != nil || role != tenancy.RoleViewer {
		t.Fatalf("role after demotion = %s, %v", role, err)
	}
}

func TestCreateOrgSlugTaken(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	if _, err := svc.CreateOrg(ctx, owner, "acme", "Acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateOrg(ctx, owner, "acme", "Acme Again"); !errors.Is(err, tenancy.ErrSlugTaken) {
		t.Fatalf("duplicate slug: %v, want ErrSlugTaken", err)
	}
}

func TestGetOrgBySlugNotFound(t *testing.T) {
	ctx := context.Background()
	svc := tenancy.NewService(newFakeStore(), &fakeMailer{}, testPublicURL, time.Now)
	if _, err := svc.GetOrgBySlug(ctx, "nope"); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("unknown slug: %v, want ErrNotFound", err)
	}
}

func TestMembershipOfNotMember(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	outsider := st.addUser("outsider@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	if _, err := svc.MembershipOf(ctx, org.ID, outsider); !errors.Is(err, tenancy.ErrNotMember) {
		t.Fatalf("outsider: %v, want ErrNotMember", err)
	}
}

func TestListOrgsForUser(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	if _, err := svc.CreateOrg(ctx, owner, "acme", "Acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateOrg(ctx, owner, "globex", "Globex"); err != nil {
		t.Fatal(err)
	}
	memberships, err := svc.ListOrgsForUser(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(memberships) != 2 {
		t.Fatalf("memberships = %d, want 2", len(memberships))
	}
}

func TestListMembersReturnsMembers(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	viewer := st.addUser("viewer@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	st.addMember(org.ID, viewer, tenancy.RoleViewer)
	members, err := svc.ListMembers(ctx, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 {
		t.Fatalf("members = %d, want 2", len(members))
	}
}

func TestUpdateOrgRenames(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	updated, err := svc.UpdateOrg(ctx, org.ID, "Acme Corp")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Acme Corp" || updated.Slug != "acme" {
		t.Fatalf("updated org = %+v", updated)
	}
}

func TestUpdateOrgUnknown(t *testing.T) {
	ctx := context.Background()
	svc := tenancy.NewService(newFakeStore(), &fakeMailer{}, testPublicURL, time.Now)
	if _, err := svc.UpdateOrg(ctx, uuid.Must(uuid.NewV7()), "x"); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("unknown org: %v, want ErrNotFound", err)
	}
}

func TestDeactivateUserRequiresInstanceAdmin(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	target := st.addUser("target@x", true)
	if err := svc.DeactivateUser(ctx, false, target); !errors.Is(err, tenancy.ErrForbidden) {
		t.Fatalf("non-admin: %v, want ErrForbidden", err)
	}
	if _, disabled := st.disabledAt[target]; disabled {
		t.Fatal("non-admin call must not have disabled the user")
	}
}

func TestDeactivateUserRevokesSessionsAndTokens(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	target := st.addUser("target@x", true)
	if err := svc.DeactivateUser(ctx, true, target); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.disabledAt[target]; !ok {
		t.Fatal("user not disabled")
	}
	if !st.revokedSess[target] {
		t.Fatal("sessions not revoked")
	}
	if !st.revokedTok[target] {
		t.Fatal("tokens not revoked")
	}
}

// TestStoreFailuresPropagate exercises the "must never swallow a genuine
// store error" branches that the happy-path tests above don't reach: each
// case forces one store method to fail and asserts the service surfaces
// that failure rather than mapping it to a domain sentinel or silently
// succeeding.
func TestStoreFailuresPropagate(t *testing.T) {
	boom := errors.New("boom")
	ctx := context.Background()

	t.Run("GetMembership actor lookup", func(t *testing.T) {
		// A genuine store failure looking up the actor's membership (not a
		// clean "no such row") must propagate as-is, the same fail-closed
		// contract MembershipOf gives: only ErrNotFound becomes the domain
		// sentinel (ErrNotMember here), never any other error.
		st := newFakeStore()
		svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
		owner := st.addUser("owner@x", true)
		org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
		st.failNext["GetMembership"] = boom
		if err := svc.ChangeRole(ctx, org.ID, owner, owner, tenancy.RoleAdmin); !errors.Is(err, boom) {
			t.Fatalf("ChangeRole actor lookup failure: %v, want %v", err, boom)
		}
	})

	t.Run("ChangeRole actor not member (ErrNotFound folds to ErrNotMember)", func(t *testing.T) {
		st := newFakeStore()
		svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
		owner := st.addUser("owner@x", true)
		outsider := st.addUser("outsider@x", true)
		org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
		if err := svc.ChangeRole(ctx, org.ID, outsider, owner, tenancy.RoleAdmin); !errors.Is(err, tenancy.ErrNotMember) {
			t.Fatalf("ChangeRole non-member actor: %v, want ErrNotMember", err)
		}
	})

	t.Run("RemoveMember actor lookup", func(t *testing.T) {
		st := newFakeStore()
		svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
		owner := st.addUser("owner@x", true)
		org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
		st.failNext["GetMembership"] = boom
		if err := svc.RemoveMember(ctx, org.ID, owner, owner); !errors.Is(err, boom) {
			t.Fatalf("RemoveMember actor lookup failure: %v, want %v", err, boom)
		}
	})

	t.Run("CountOwners", func(t *testing.T) {
		st := newFakeStore()
		svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
		owner := st.addUser("owner@x", true)
		org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
		st.failNext["CountOwners"] = boom
		if err := svc.ChangeRole(ctx, org.ID, owner, owner, tenancy.RoleAdmin); !errors.Is(err, boom) {
			t.Fatalf("ChangeRole CountOwners failure: %v, want %v", err, boom)
		}
	})

	t.Run("CountOwners via RemoveMember", func(t *testing.T) {
		st := newFakeStore()
		svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
		owner := st.addUser("owner@x", true)
		org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
		st.failNext["CountOwners"] = boom
		if err := svc.RemoveMember(ctx, org.ID, owner, owner); !errors.Is(err, boom) {
			t.Fatalf("RemoveMember CountOwners failure: %v, want %v", err, boom)
		}
	})

	t.Run("UpdateMembershipRole", func(t *testing.T) {
		st := newFakeStore()
		svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
		owner := st.addUser("owner@x", true)
		admin := st.addUser("admin@x", true)
		org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
		st.addMember(org.ID, admin, tenancy.RoleAdmin)
		st.failNext["UpdateMembershipRole"] = boom
		if err := svc.ChangeRole(ctx, org.ID, owner, admin, tenancy.RoleViewer); !errors.Is(err, boom) {
			t.Fatalf("UpdateMembershipRole failure: %v, want %v", err, boom)
		}
	})

	t.Run("DeleteMembership", func(t *testing.T) {
		st := newFakeStore()
		svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
		owner := st.addUser("owner@x", true)
		admin := st.addUser("admin@x", true)
		org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
		st.addMember(org.ID, admin, tenancy.RoleAdmin)
		st.failNext["DeleteMembership"] = boom
		if err := svc.RemoveMember(ctx, org.ID, owner, admin); !errors.Is(err, boom) {
			t.Fatalf("DeleteMembership failure: %v, want %v", err, boom)
		}
	})

	t.Run("MembershipOf raw error", func(t *testing.T) {
		st := newFakeStore()
		svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
		owner := st.addUser("owner@x", true)
		org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
		st.failNext["GetMembership"] = boom
		if _, err := svc.MembershipOf(ctx, org.ID, owner); !errors.Is(err, boom) {
			t.Fatalf("MembershipOf: %v, want %v", err, boom)
		}
	})

	t.Run("Invite InsertInvite", func(t *testing.T) {
		st := newFakeStore()
		svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
		owner := st.addUser("owner@x", true)
		org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
		st.failNext["InsertInvite"] = boom
		if _, err := svc.Invite(ctx, org.ID, owner, "new@x", tenancy.RoleViewer); !errors.Is(err, boom) {
			t.Fatalf("Invite InsertInvite failure: %v, want %v", err, boom)
		}
	})

	t.Run("AcceptInvite CreateUser", func(t *testing.T) {
		st := newFakeStore()
		mail := &fakeMailer{}
		svc := tenancy.NewService(st, mail, testPublicURL, time.Now)
		owner := st.addUser("owner@x", true)
		org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
		inv, err := svc.Invite(ctx, org.ID, owner, "new@x", tenancy.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		st.failNext["CreateUser"] = boom
		if _, _, err := svc.AcceptInvite(ctx, inv.Token, "New", "correct horse battery"); !errors.Is(err, boom) {
			t.Fatalf("AcceptInvite CreateUser failure: %v, want %v", err, boom)
		}
	})

	t.Run("AcceptInvite GetUserByEmail generic failure", func(t *testing.T) {
		st := newFakeStore()
		mail := &fakeMailer{}
		svc := tenancy.NewService(st, mail, testPublicURL, time.Now)
		owner := st.addUser("owner@x", true)
		org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
		inv, err := svc.Invite(ctx, org.ID, owner, "new@x", tenancy.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		st.failNext["GetUserByEmail"] = boom
		if _, _, err := svc.AcceptInvite(ctx, inv.Token, "New", "correct horse battery"); !errors.Is(err, boom) {
			t.Fatalf("AcceptInvite GetUserByEmail failure: %v, want %v", err, boom)
		}
	})

	t.Run("AcceptInvite SetUserPasswordAndName", func(t *testing.T) {
		// The invited user row already exists (a previous, never-accepted
		// invite created it with no password) and this accept must set its
		// credentials — exercise that store call's own failure path.
		st := newFakeStore()
		mail := &fakeMailer{}
		svc := tenancy.NewService(st, mail, testPublicURL, time.Now)
		owner := st.addUser("owner@x", true)
		org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
		st.addUser("passwordless@x", false)
		inv, err := svc.Invite(ctx, org.ID, owner, "passwordless@x", tenancy.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		st.failNext["SetUserPasswordAndName"] = boom
		if _, _, err := svc.AcceptInvite(ctx, inv.Token, "New", "correct horse battery"); !errors.Is(err, boom) {
			t.Fatalf("AcceptInvite SetUserPasswordAndName failure: %v, want %v", err, boom)
		}
	})

	t.Run("AcceptInvite AddMembership", func(t *testing.T) {
		st := newFakeStore()
		mail := &fakeMailer{}
		svc := tenancy.NewService(st, mail, testPublicURL, time.Now)
		owner := st.addUser("owner@x", true)
		org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
		inv, err := svc.Invite(ctx, org.ID, owner, "new@x", tenancy.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		st.failNext["AddMembership"] = boom
		if _, _, err := svc.AcceptInvite(ctx, inv.Token, "New", "correct horse battery"); !errors.Is(err, boom) {
			t.Fatalf("AcceptInvite AddMembership failure: %v, want %v", err, boom)
		}
	})

	t.Run("DeactivateUser DisableUser", func(t *testing.T) {
		st := newFakeStore()
		svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
		target := st.addUser("target@x", true)
		st.failNext["DisableUser"] = boom
		if err := svc.DeactivateUser(ctx, true, target); !errors.Is(err, boom) {
			t.Fatalf("DeactivateUser DisableUser failure: %v, want %v", err, boom)
		}
	})

	t.Run("DeactivateUser RevokeSessionsForUser", func(t *testing.T) {
		st := newFakeStore()
		svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
		target := st.addUser("target@x", true)
		st.failNext["RevokeSessionsForUser"] = boom
		if err := svc.DeactivateUser(ctx, true, target); !errors.Is(err, boom) {
			t.Fatalf("DeactivateUser RevokeSessionsForUser failure: %v, want %v", err, boom)
		}
	})

	t.Run("DeactivateUser RevokeTokensForUser", func(t *testing.T) {
		st := newFakeStore()
		svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
		target := st.addUser("target@x", true)
		st.failNext["RevokeTokensForUser"] = boom
		if err := svc.DeactivateUser(ctx, true, target); !errors.Is(err, boom) {
			t.Fatalf("DeactivateUser RevokeTokensForUser failure: %v, want %v", err, boom)
		}
	})

	t.Run("CreateOrg AddMembership", func(t *testing.T) {
		st := newFakeStore()
		svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
		owner := st.addUser("owner@x", true)
		st.failNext["AddMembership"] = boom
		if _, err := svc.CreateOrg(ctx, owner, "acme", "Acme"); !errors.Is(err, boom) {
			t.Fatalf("CreateOrg AddMembership failure: %v, want %v", err, boom)
		}
	})

	t.Run("UpdateOrg store failure", func(t *testing.T) {
		st := newFakeStore()
		svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
		owner := st.addUser("owner@x", true)
		org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
		st.failNext["UpdateOrg"] = boom
		if _, err := svc.UpdateOrg(ctx, org.ID, "new name"); !errors.Is(err, boom) {
			t.Fatalf("UpdateOrg failure: %v, want %v", err, boom)
		}
	})
}
