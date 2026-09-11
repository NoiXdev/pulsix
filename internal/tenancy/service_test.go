package tenancy_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/mail"
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
	// instanceAdmins marks users as instance administrators, for
	// DisableUser's last-active-administrator guard.
	instanceAdmins map[uuid.UUID]bool

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
		instanceAdmins: map[uuid.UUID]bool{}, failNext: map[string]error{},
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

// AddMembership mirrors the real table's "INSERT ... ON CONFLICT (org_id,
// user_id) DO NOTHING" (Finding 5, Task 10 fix round 1): a membership that
// already exists is left exactly as it was, not silently upserted to the
// role just passed in. Before this fix the fake upserted unconditionally,
// which is exactly why the real duplicate-membership bug (accepting an
// invite for an address that is already a member burning the invite before
// failing with an unmapped unique-violation 500) had no test coverage —
// every fake-store-backed test that exercised the path would have quietly
// "succeeded" by overwriting the role.
func (f *fakeStore) AddMembership(_ context.Context, orgID, userID uuid.UUID, role tenancy.Role) error {
	if err := f.take("AddMembership"); err != nil {
		return err
	}
	key := [2]uuid.UUID{orgID, userID}
	if _, exists := f.memberships[key]; exists {
		return nil
	}
	f.memberships[key] = role
	return nil
}

// UpdateMembershipRole mirrors the real, atomic guarded UPDATE
// (tenancy_users.sql, Finding 3 / Task 10 fix round 1): it refuses — with
// ErrNotFound, the same sentinel a 0-rows-affected UPDATE maps to — to move
// the org's last owner away from the owner role. Unlike the real store, a
// Go map cannot race with itself, so this cannot exercise the concurrency
// half of the fix (see store_sql_test.go's
// TestSQLStoreConcurrentDemotionOfBothOwnersLeavesOneOwner for that); it
// exists so ChangeRole's own reinterpretation of a guarded ErrNotFound as
// ErrLastOwner has something to observe against a fake store at all.
func (f *fakeStore) UpdateMembershipRole(_ context.Context, orgID, userID uuid.UUID, role tenancy.Role) error {
	if err := f.take("UpdateMembershipRole"); err != nil {
		return err
	}
	key := [2]uuid.UUID{orgID, userID}
	current, ok := f.memberships[key]
	if !ok {
		return tenancy.ErrNotFound
	}
	if current == tenancy.RoleOwner && role != tenancy.RoleOwner && f.countOwners(orgID) <= 1 {
		return tenancy.ErrNotFound
	}
	f.memberships[key] = role
	return nil
}

// DeleteMembership mirrors the real store the same way UpdateMembershipRole
// does: ErrNotFound both for a membership that never existed and for one
// the atomic last-owner guard refused to remove.
func (f *fakeStore) DeleteMembership(_ context.Context, orgID, userID uuid.UUID) error {
	if err := f.take("DeleteMembership"); err != nil {
		return err
	}
	key := [2]uuid.UUID{orgID, userID}
	current, ok := f.memberships[key]
	if !ok {
		return tenancy.ErrNotFound
	}
	if current == tenancy.RoleOwner && f.countOwners(orgID) <= 1 {
		return tenancy.ErrNotFound
	}
	delete(f.memberships, key)
	return nil
}

// countOwners is the guard logic UpdateMembershipRole/DeleteMembership
// share, factored out from the public CountOwners method below (which also
// goes through f.take and so cannot be called internally without
// consuming an injected failure meant for the caller's own CountOwners
// test, if any).
func (f *fakeStore) countOwners(orgID uuid.UUID) int {
	n := 0
	for key, role := range f.memberships {
		if key[0] == orgID && role == tenancy.RoleOwner {
			n++
		}
	}
	return n
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

// DisableUser mirrors the SQL guard: an unknown user is ErrNotFound, and
// the last active instance administrator cannot be disabled.
func (f *fakeStore) DisableUser(_ context.Context, id uuid.UUID, at time.Time) error {
	if err := f.take("DisableUser"); err != nil {
		return err
	}
	if _, ok := f.users[id]; !ok {
		return tenancy.ErrNotFound
	}
	if _, alreadyDisabled := f.disabledAt[id]; f.instanceAdmins[id] && !alreadyDisabled {
		active := 0
		for admin := range f.instanceAdmins {
			if _, disabled := f.disabledAt[admin]; !disabled {
				active++
			}
		}
		if active <= 1 {
			return tenancy.ErrLastInstanceAdmin
		}
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

func (f *fakeStore) DeleteInvite(_ context.Context, orgID, id uuid.UUID) error {
	if err := f.take("DeleteInvite"); err != nil {
		return err
	}
	for k, inv := range f.invites {
		if inv.ID == id && inv.OrgID == orgID {
			delete(f.invites, k)
		}
	}
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
	uid, gotOrg, newAccount, err := svc.AcceptInvite(ctx, inv.Token, "New User", "correct horse battery")
	if err != nil || gotOrg != org.ID {
		t.Fatalf("accept: %v", err)
	}
	if !newAccount {
		t.Fatal("accepting an invite for a brand-new address should report newAccount = true")
	}
	role, err := svc.MembershipOf(ctx, org.ID, uid)
	if err != nil || role != tenancy.RoleOperator {
		t.Fatalf("role %s %v", role, err)
	}
	if _, _, _, err := svc.AcceptInvite(ctx, inv.Token, "x", "correct horse battery"); !errors.Is(err, tenancy.ErrNotFound) {
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

	uid, gotOrg, newAccount, err := svc.AcceptInvite(ctx, inv.Token, "Attacker-Chosen Name", "attacker-chosen-password")
	if err != nil {
		t.Fatal(err)
	}
	if uid != existing || gotOrg != org.ID {
		t.Fatalf("accept created/joined the wrong user: got %s, want %s", uid, existing)
	}
	if newAccount {
		t.Fatal("accepting an invite for an address with its own password should report newAccount = false")
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

// TestAcceptInviteAsExistingMember is Finding 5's regression test (Task 10
// fix round 1): the real org_memberships table has no ON CONFLICT-free
// insert path — a plain INSERT on the (org_id, user_id) primary key — so
// accepting an invitation for an address that already belongs to a member
// of that same org must not burn the invite (TakeInvite already marks it
// accepted before AddMembership ever runs) and then blow up as an unmapped
// 500. Acceptance must succeed, add no duplicate row, and leave the
// existing membership's role exactly as it was — the invited role is not
// silently applied, since accepting an invite is not the same operation as
// ChangeRole. fakeStore.AddMembership's ON-CONFLICT-DO-NOTHING semantics
// (mirroring the real table) are what make this test meaningful: before
// that fix the fake upserted and this scenario always "passed" by luck.
func TestAcceptInviteAsExistingMember(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	mail := &fakeMailer{}
	svc := tenancy.NewService(st, mail, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	existing := st.addUser("already-a-member@x", true)
	st.addMember(org.ID, existing, tenancy.RoleViewer)

	inv, err := svc.Invite(ctx, org.ID, owner, "already-a-member@x", tenancy.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	uid, gotOrg, newAccount, err := svc.AcceptInvite(ctx, inv.Token, "Ignored Name", "correct horse battery")
	if err != nil {
		t.Fatalf("accept as existing member: %v", err)
	}
	if uid != existing || gotOrg != org.ID {
		t.Fatalf("accept resolved the wrong user/org: got %s/%s, want %s/%s", uid, gotOrg, existing, org.ID)
	}
	if newAccount {
		t.Fatal("accepting an invite for an existing member with a password should report newAccount = false")
	}
	role, err := svc.MembershipOf(ctx, org.ID, existing)
	if err != nil || role != tenancy.RoleViewer {
		t.Fatalf("existing member's role changed: role=%s err=%v, want unchanged viewer", role, err)
	}
	members, err := svc.ListMembers(ctx, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 { // owner + existing, no duplicate row
		t.Fatalf("members after accept = %d, want 2 (no duplicate membership)", len(members))
	}
}

// TestAcceptInviteReinvitedPasswordlessAccountReportsNewAccount covers
// AcceptInvite's third branch: a user row that already exists but has
// never had a password set (PasswordHash == nil — left over from an
// earlier, never-accepted invite for the same address). This accept call
// is what actually gives the address its first password, so it must
// report newAccount = true just like a genuinely brand-new row — the
// handler layer opens a session on that signal (see
// api.registerInviteAcceptRoute), and this address has never had
// credentials to sign in with before now, so it needs one exactly as much
// as a first-time invitee does.
func TestAcceptInviteReinvitedPasswordlessAccountReportsNewAccount(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	mail := &fakeMailer{}
	svc := tenancy.NewService(st, mail, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	passwordless := st.addUser("never-accepted@x", false)

	inv, err := svc.Invite(ctx, org.ID, owner, "never-accepted@x", tenancy.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	uid, gotOrg, newAccount, err := svc.AcceptInvite(ctx, inv.Token, "First Real Name", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if uid != passwordless || gotOrg != org.ID {
		t.Fatalf("accept resolved the wrong user/org: got %s/%s, want %s/%s", uid, gotOrg, passwordless, org.ID)
	}
	if !newAccount {
		t.Fatal("giving a passwordless row its first password should report newAccount = true")
	}
	if st.users[passwordless].PasswordHash == nil {
		t.Fatal("accept did not set a password for the passwordless row")
	}
	if st.users[passwordless].DisplayName != "First Real Name" {
		t.Fatalf("display name = %q, want the name given at acceptance", st.users[passwordless].DisplayName)
	}
}

func TestAcceptInviteUnknownTokenNotFound(t *testing.T) {
	ctx := context.Background()
	svc := tenancy.NewService(newFakeStore(), &fakeMailer{}, testPublicURL, time.Now)
	if _, _, _, err := svc.AcceptInvite(ctx, "not-a-real-token", "x", "correct horse battery"); !errors.Is(err, tenancy.ErrNotFound) {
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
	if _, _, _, err := svc.AcceptInvite(ctx, inv.Token, "Late", "correct horse battery"); !errors.Is(err, tenancy.ErrNotFound) {
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

// TestInviteDeliveryFailureLeavesNoInvitation: with mail configured but
// delivery failing, Invite fails with ErrInviteDelivery (wrapping the
// mailer's own error) and removes the invitation it had stored — nobody was
// told about it, so it must not linger as an orphaned row.
func TestInviteDeliveryFailureLeavesNoInvitation(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	smtpDown := errors.New("smtp down")
	svc := tenancy.NewService(st, &fakeMailer{failErr: smtpDown}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	_, err := svc.Invite(ctx, org.ID, owner, "new@x", tenancy.RoleViewer)
	if !errors.Is(err, tenancy.ErrInviteDelivery) || !errors.Is(err, smtpDown) {
		t.Fatalf("err = %v, want ErrInviteDelivery wrapping the mailer's error", err)
	}
	if len(st.invites) != 0 {
		t.Fatalf("%d invitation(s) left behind after a failed delivery", len(st.invites))
	}
}

// TestInviteDeliveryFailureReportsAFailedCleanup: if removing the
// undelivered invitation fails too, both failures are reported.
func TestInviteDeliveryFailureReportsAFailedCleanup(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{failErr: errors.New("smtp down")}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	cleanup := errors.New("delete failed")
	st.failNext["DeleteInvite"] = cleanup
	if _, err := svc.Invite(ctx, org.ID, owner, "new@x", tenancy.RoleViewer); !errors.Is(err, tenancy.ErrInviteDelivery) || !errors.Is(err, cleanup) {
		t.Fatalf("err = %v, want ErrInviteDelivery and the cleanup failure", err)
	}
}

// TestInviteWithoutMailConfiguredReturnsTheLink: inviting must not depend on
// outbound mail. With none configured, the invitation is kept and comes back
// with its acceptance link for the administrator to hand over; with mail
// working, the link is mailed and not returned.
func TestInviteWithoutMailConfiguredReturnsTheLink(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{failErr: fmt.Errorf("load smtp settings: %w", mail.ErrNotConfigured)}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")

	inv, err := svc.Invite(ctx, org.ID, owner, "new@x", tenancy.RoleViewer)
	if err != nil {
		t.Fatalf("Invite without mail configured: %v", err)
	}
	if inv.Mailed || inv.AcceptURL != testPublicURL+"/invite/"+inv.Token {
		t.Fatalf("invite = mailed %v, accept url %q; want an unmailed invite carrying its link", inv.Mailed, inv.AcceptURL)
	}
	if len(st.invites) != 1 {
		t.Fatalf("stored invitations = %d, want 1", len(st.invites))
	}
	if _, _, _, err := svc.AcceptInvite(ctx, inv.Token, "New", "long enough password"); err != nil {
		t.Fatalf("accepting the returned link: %v", err)
	}

	mailed, err := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now).Invite(ctx, org.ID, owner, "other@x", tenancy.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	if !mailed.Mailed || mailed.AcceptURL != "" {
		t.Fatalf("mailed invite = mailed %v, accept url %q; want mailed and no link returned", mailed.Mailed, mailed.AcceptURL)
	}
}

func TestValidateOrgName(t *testing.T) {
	for name, ok := range map[string]bool{
		"Acme Hosting":                    true,
		"Müller GmbH":                     true,
		"":                                false,
		"   ":                             false,
		"Acme\r\nBcc: attacker@evil.test": false,
		"Acme\nX-Injected: yes":           false,
		"Tab\there":                       false,
		strings.Repeat("a", 201):          false,
		strings.Repeat("ä", 200):          true,
	} {
		err := tenancy.ValidateOrgName(name)
		if ok && err != nil {
			t.Errorf("ValidateOrgName(%q) = %v, want nil", name, err)
		}
		if !ok && !errors.Is(err, tenancy.ErrInvalidInput) {
			t.Errorf("ValidateOrgName(%q) = %v, want ErrInvalidInput", name, err)
		}
	}
}

// TestOrgNamesWithControlCharactersAreRejected covers M4 at the service: an
// organization name ends up in the Subject header of every invitation, so
// neither creating nor renaming may store a CR or LF in it.
func TestOrgNamesWithControlCharactersAreRejected(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	if _, err := svc.CreateOrg(ctx, owner, "evil", "Evil\r\nBcc: attacker@evil.test"); !errors.Is(err, tenancy.ErrInvalidInput) {
		t.Fatalf("CreateOrg with CRLF: err = %v, want ErrInvalidInput", err)
	}
	org, err := svc.CreateOrg(ctx, owner, "acme", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateOrg(ctx, org.ID, "Acme\nX-Injected: yes"); !errors.Is(err, tenancy.ErrInvalidInput) {
		t.Fatalf("UpdateOrg with LF: err = %v, want ErrInvalidInput", err)
	}
}

func TestLastOwnerCannotBeDemotedOrRemoved(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	if err := svc.ChangeRole(ctx, org.ID, tenancy.RoleOwner, owner, owner, tenancy.RoleAdmin); !errors.Is(err, tenancy.ErrLastOwner) {
		t.Fatalf("demote: %v", err)
	}
	if err := svc.RemoveMember(ctx, org.ID, tenancy.RoleOwner, owner, owner); !errors.Is(err, tenancy.ErrLastOwner) {
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
	if err := svc.ChangeRole(ctx, org.ID, tenancy.RoleAdmin, admin, admin, tenancy.RoleOwner); !errors.Is(err, tenancy.ErrForbidden) {
		t.Fatalf("admin self-promote: %v", err)
	}
	if err := svc.ChangeRole(ctx, org.ID, tenancy.RoleOwner, owner, admin, tenancy.RoleOwner); err != nil {
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
	if err := svc.RemoveMember(ctx, org.ID, tenancy.RoleAdmin, admin, secondOwner); !errors.Is(err, tenancy.ErrForbidden) {
		t.Fatalf("admin removes owner: %v, want ErrForbidden", err)
	}
	if err := svc.RemoveMember(ctx, org.ID, tenancy.RoleOwner, owner, secondOwner); err != nil {
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
	if err := svc.RemoveMember(ctx, org.ID, tenancy.RoleOwner, owner, viewer); err != nil {
		t.Fatalf("remove viewer: %v", err)
	}
	if _, err := svc.MembershipOf(ctx, org.ID, viewer); !errors.Is(err, tenancy.ErrNotMember) {
		t.Fatalf("membership after remove: %v, want ErrNotMember", err)
	}
}

// TestChangeRoleUsesSuppliedActorRoleNotStoreMembership is Finding 1's core
// regression test (Task 10 fix round 1): ChangeRole must decide the
// owner-only rules from the actorRole the caller supplies — the effective
// role api.requireOrg already computed as the lower of the caller's real
// membership and any bearer token's own scope — never by re-reading the
// actor's membership from the store itself. owner here really is an owner
// in the store (as a real admin-scoped bearer token minted by an owner
// would resolve if re-looked-up), so a store-derived decision would
// incorrectly allow this; only trusting the supplied actorRole refuses it.
func TestChangeRoleUsesSuppliedActorRoleNotStoreMembership(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	target := st.addUser("target@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	st.addMember(org.ID, target, tenancy.RoleViewer)

	if err := svc.ChangeRole(ctx, org.ID, tenancy.RoleAdmin, owner, target, tenancy.RoleOwner); !errors.Is(err, tenancy.ErrForbidden) {
		t.Fatalf("admin-capped actorRole granting owner: %v, want ErrForbidden", err)
	}
	if err := svc.ChangeRole(ctx, org.ID, tenancy.RoleOwner, owner, target, tenancy.RoleOwner); err != nil {
		t.Fatalf("uncapped owner actorRole granting owner: %v", err)
	}
}

// TestRemoveMemberUsesSuppliedActorRoleNotStoreMembership is
// TestChangeRoleUsesSuppliedActorRoleNotStoreMembership's RemoveMember
// counterpart: removing an existing owner must be refused when the
// supplied actorRole is capped below owner, even though owner's real store
// membership is owner.
func TestRemoveMemberUsesSuppliedActorRoleNotStoreMembership(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	secondOwner := st.addUser("owner2@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	st.addMember(org.ID, secondOwner, tenancy.RoleOwner)

	if err := svc.RemoveMember(ctx, org.ID, tenancy.RoleAdmin, owner, secondOwner); !errors.Is(err, tenancy.ErrForbidden) {
		t.Fatalf("admin-capped actorRole removing an owner: %v, want ErrForbidden", err)
	}
	if err := svc.RemoveMember(ctx, org.ID, tenancy.RoleOwner, owner, secondOwner); err != nil {
		t.Fatalf("uncapped owner actorRole removing an owner: %v", err)
	}
}

func TestChangeRoleTargetNotFound(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	stranger := st.addUser("stranger@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	if err := svc.ChangeRole(ctx, org.ID, tenancy.RoleOwner, owner, stranger, tenancy.RoleAdmin); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("unknown target: %v, want ErrNotFound", err)
	}
}

func TestChangeRoleRejectsUnknownRole(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
	if err := svc.ChangeRole(ctx, org.ID, tenancy.RoleOwner, owner, owner, tenancy.Role("superuser")); !errors.Is(err, tenancy.ErrForbidden) {
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
	if err := svc.ChangeRole(ctx, org.ID, tenancy.RoleAdmin, admin, operator, tenancy.RoleViewer); err != nil {
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

// TestCreateOrgRunsOrgCreatedHook proves the hook Task 13 adds
// (SetOrgCreatedHook) is actually invoked, with the newly created org's id,
// once CreateOrg's own steps (the org row and the owner membership) have
// already succeeded.
func TestCreateOrgRunsOrgCreatedHook(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)

	var gotOrgID uuid.UUID
	calls := 0
	svc.SetOrgCreatedHook(func(_ context.Context, orgID uuid.UUID) error {
		calls++
		gotOrgID = orgID
		// The org and its owner membership must already be visible to the
		// hook — it runs after both steps, never before or concurrently.
		if _, err := st.GetOrgByID(ctx, orgID); err != nil {
			t.Errorf("hook ran before the org was committed: %v", err)
		}
		if role, err := st.GetMembership(ctx, orgID, owner); err != nil || role != tenancy.RoleOwner {
			t.Errorf("hook ran before the owner membership was committed: role=%s err=%v", role, err)
		}
		return nil
	})

	org, err := svc.CreateOrg(ctx, owner, "acme", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("hook called %d times, want 1", calls)
	}
	if gotOrgID != org.ID {
		t.Fatalf("hook got orgID %s, want %s", gotOrgID, org.ID)
	}
}

// TestCreateOrgPropagatesOrgCreatedHookError proves a failing hook fails
// the whole CreateOrg call — the caller learns provisioning did not fully
// succeed rather than getting back an Org that silently lacks whatever the
// hook was supposed to set up (see OrgCreatedHook's own doc comment on why
// this is "fail loud", not "fail atomically": the org and membership rows
// are not rolled back, but the caller is never told creation succeeded
// when it did not finish).
func TestCreateOrgPropagatesOrgCreatedHookError(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	boom := errors.New("boom")
	svc.SetOrgCreatedHook(func(context.Context, uuid.UUID) error { return boom })

	if _, err := svc.CreateOrg(ctx, owner, "acme", "Acme"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped %v", err, boom)
	}
}

// TestCreateOrgWithNoHookInstalledSucceeds proves CreateOrg behaves exactly
// as it did before OrgCreatedHook existed when no hook is installed — the
// default for every test above this one in the file, and for any other
// package's tests that build a bare tenancy.Service.
func TestCreateOrgWithNoHookInstalledSucceeds(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	owner := st.addUser("owner@x", true)
	if _, err := svc.CreateOrg(ctx, owner, "acme", "Acme"); err != nil {
		t.Fatal(err)
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

// TestDeactivateUserRefusesTheLastInstanceAdmin covers M6: setup is gated on
// "no instance administrator exists", so disabling the last active one —
// the caller themselves included — would lock the instance for good. It is
// refused before anything is revoked; with a second active administrator
// it goes through.
func TestDeactivateUserRefusesTheLastInstanceAdmin(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
	admin := st.addUser("admin@x", true)
	st.instanceAdmins[admin] = true

	if err := svc.DeactivateUser(ctx, true, admin); !errors.Is(err, tenancy.ErrLastInstanceAdmin) {
		t.Fatalf("deactivating the only instance admin: %v, want ErrLastInstanceAdmin", err)
	}
	if st.revokedSess[admin] || st.revokedTok[admin] {
		t.Fatal("the refused deactivation still revoked sessions or tokens")
	}

	second := st.addUser("second@x", true)
	st.instanceAdmins[second] = true
	if err := svc.DeactivateUser(ctx, true, admin); err != nil {
		t.Fatalf("deactivating one of two instance admins: %v", err)
	}
	if err := svc.DeactivateUser(ctx, true, second); !errors.Is(err, tenancy.ErrLastInstanceAdmin) {
		t.Fatalf("deactivating the remaining instance admin: %v, want ErrLastInstanceAdmin", err)
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

	t.Run("GetMembership target lookup", func(t *testing.T) {
		// A genuine store failure looking up the target's membership (not a
		// clean "no such row") must propagate as-is. Finding 1's fix (Task
		// 10 fix round 1) removed the actor's own GetMembership lookup
		// entirely — actorRole is supplied by the caller now — so this is
		// the sole remaining GetMembership call in ChangeRole.
		st := newFakeStore()
		svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
		owner := st.addUser("owner@x", true)
		org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
		st.failNext["GetMembership"] = boom
		if err := svc.ChangeRole(ctx, org.ID, tenancy.RoleOwner, owner, owner, tenancy.RoleAdmin); !errors.Is(err, boom) {
			t.Fatalf("ChangeRole target lookup failure: %v, want %v", err, boom)
		}
	})

	t.Run("RemoveMember target lookup", func(t *testing.T) {
		st := newFakeStore()
		svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
		owner := st.addUser("owner@x", true)
		org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
		st.failNext["GetMembership"] = boom
		if err := svc.RemoveMember(ctx, org.ID, tenancy.RoleOwner, owner, owner); !errors.Is(err, boom) {
			t.Fatalf("RemoveMember target lookup failure: %v, want %v", err, boom)
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
		if err := svc.ChangeRole(ctx, org.ID, tenancy.RoleOwner, owner, admin, tenancy.RoleViewer); !errors.Is(err, boom) {
			t.Fatalf("UpdateMembershipRole failure: %v, want %v", err, boom)
		}
	})

	t.Run("UpdateMembershipRole ErrNotFound for a non-owner target is not reinterpreted as ErrLastOwner", func(t *testing.T) {
		// The atomic-guard reinterpretation in ChangeRole must be scoped
		// exactly to "was demoting the org's only owner" — an ErrNotFound
		// from a genuinely vanished, never-an-owner target (e.g. removed by
		// a concurrent request) must surface as ErrNotFound, not get
		// mislabeled ErrLastOwner.
		st := newFakeStore()
		svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
		owner := st.addUser("owner@x", true)
		admin := st.addUser("admin@x", true)
		org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
		st.addMember(org.ID, admin, tenancy.RoleAdmin)
		st.failNext["UpdateMembershipRole"] = tenancy.ErrNotFound
		err := svc.ChangeRole(ctx, org.ID, tenancy.RoleOwner, owner, admin, tenancy.RoleViewer)
		if !errors.Is(err, tenancy.ErrNotFound) {
			t.Fatalf("non-owner target racing to ErrNotFound: %v, want ErrNotFound", err)
		}
		if errors.Is(err, tenancy.ErrLastOwner) {
			t.Fatalf("non-owner target's ErrNotFound was reinterpreted as ErrLastOwner: %v", err)
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
		if err := svc.RemoveMember(ctx, org.ID, tenancy.RoleOwner, owner, admin); !errors.Is(err, boom) {
			t.Fatalf("DeleteMembership failure: %v, want %v", err, boom)
		}
	})

	t.Run("DeleteMembership ErrNotFound for a non-owner target is not reinterpreted as ErrLastOwner", func(t *testing.T) {
		st := newFakeStore()
		svc := tenancy.NewService(st, &fakeMailer{}, testPublicURL, time.Now)
		owner := st.addUser("owner@x", true)
		admin := st.addUser("admin@x", true)
		org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
		st.addMember(org.ID, admin, tenancy.RoleAdmin)
		st.failNext["DeleteMembership"] = tenancy.ErrNotFound
		err := svc.RemoveMember(ctx, org.ID, tenancy.RoleOwner, owner, admin)
		if !errors.Is(err, tenancy.ErrNotFound) {
			t.Fatalf("non-owner target racing to ErrNotFound: %v, want ErrNotFound", err)
		}
		if errors.Is(err, tenancy.ErrLastOwner) {
			t.Fatalf("non-owner target's ErrNotFound was reinterpreted as ErrLastOwner: %v", err)
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

	t.Run("AcceptInvite TakeInvite generic failure", func(t *testing.T) {
		// Adjacent fix, Task 10 fix round 1: AcceptInvite used to fold every
		// TakeInvite error into ErrNotFound, turning a database outage into
		// a silent 404 indistinguishable from "no such invite". Only a
		// genuine ErrNotFound (unknown/expired/already-used token) may
		// become the domain sentinel; anything else must propagate.
		st := newFakeStore()
		mail := &fakeMailer{}
		svc := tenancy.NewService(st, mail, testPublicURL, time.Now)
		owner := st.addUser("owner@x", true)
		org, _ := svc.CreateOrg(ctx, owner, "acme", "Acme")
		inv, err := svc.Invite(ctx, org.ID, owner, "new@x", tenancy.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		st.failNext["TakeInvite"] = boom
		if _, _, _, err := svc.AcceptInvite(ctx, inv.Token, "New", "correct horse battery"); !errors.Is(err, boom) {
			t.Fatalf("AcceptInvite TakeInvite failure: %v, want %v", err, boom)
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
		if _, _, _, err := svc.AcceptInvite(ctx, inv.Token, "New", "correct horse battery"); !errors.Is(err, boom) {
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
		if _, _, _, err := svc.AcceptInvite(ctx, inv.Token, "New", "correct horse battery"); !errors.Is(err, boom) {
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
		if _, _, _, err := svc.AcceptInvite(ctx, inv.Token, "New", "correct horse battery"); !errors.Is(err, boom) {
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
		if _, _, _, err := svc.AcceptInvite(ctx, inv.Token, "New", "correct horse battery"); !errors.Is(err, boom) {
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
