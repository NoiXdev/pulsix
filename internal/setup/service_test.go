package setup_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/mail"
	"github.com/noixdev/pulsix/internal/setup"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// fakeUser is the fake store's own internal shape — deliberately distinct
// from setup.AdminUser (the Store method's parameter type), the same way
// tenancy_test.fakeStore keeps its own user representation rather than
// reusing the service package's wire types.
type fakeUser struct {
	ID            uuid.UUID
	Email         string
	DisplayName   string
	PasswordHash  *string
	InstanceAdmin bool
}

// fakeSetupStore is an in-memory setup.Store good enough to drive Service
// without a database, mirroring tenancy_test.fakeStore's shape.
type fakeSetupStore struct {
	users       map[uuid.UUID]fakeUser
	orgs        map[uuid.UUID]tenancy.Org
	memberships map[uuid.UUID]map[uuid.UUID]tenancy.Role
	settings    map[string][]byte

	// failNext makes the named method's next call return this error
	// instead of doing its normal thing, mirroring tenancy_test.fakeStore.
	failNext map[string]error
}

func newFakeSetupStore() *fakeSetupStore {
	return &fakeSetupStore{
		users: map[uuid.UUID]fakeUser{}, orgs: map[uuid.UUID]tenancy.Org{},
		memberships: map[uuid.UUID]map[uuid.UUID]tenancy.Role{}, settings: map[string][]byte{},
		failNext: map[string]error{},
	}
}

func (f *fakeSetupStore) take(method string) error {
	if err, ok := f.failNext[method]; ok {
		delete(f.failNext, method)
		return err
	}
	return nil
}

// InTx snapshots every map before running fn and restores them if fn
// returns an error, simulating the atomicity the real SQL store gets from
// an actual database transaction: a half-finished setup never persists.
func (f *fakeSetupStore) InTx(ctx context.Context, fn func(setup.Store) error) error {
	backupUsers := cloneUsers(f.users)
	backupOrgs := cloneOrgs(f.orgs)
	backupMemberships := cloneMemberships(f.memberships)
	backupSettings := cloneSettings(f.settings)
	if err := fn(f); err != nil {
		f.users, f.orgs, f.memberships, f.settings = backupUsers, backupOrgs, backupMemberships, backupSettings
		return err
	}
	return nil
}

func (f *fakeSetupStore) AdminExists(context.Context) (bool, error) {
	if err := f.take("AdminExists"); err != nil {
		return false, err
	}
	for _, u := range f.users {
		if u.InstanceAdmin {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeSetupStore) AdminMFAEnrolled(context.Context) (bool, error) {
	return false, f.take("AdminMFAEnrolled")
}

func (f *fakeSetupStore) CreateAdmin(_ context.Context, u setup.AdminUser) error {
	if err := f.take("CreateAdmin"); err != nil {
		return err
	}
	hash := u.PasswordHash
	f.users[u.ID] = fakeUser{ID: u.ID, Email: u.Email, DisplayName: u.DisplayName, PasswordHash: &hash, InstanceAdmin: true}
	return nil
}

func (f *fakeSetupStore) CreateOrg(_ context.Context, o tenancy.Org) error {
	if err := f.take("CreateOrg"); err != nil {
		return err
	}
	f.orgs[o.ID] = o
	return nil
}

func (f *fakeSetupStore) AddMembership(_ context.Context, orgID, userID uuid.UUID, role tenancy.Role) error {
	if err := f.take("AddMembership"); err != nil {
		return err
	}
	if f.memberships[orgID] == nil {
		f.memberships[orgID] = map[uuid.UUID]tenancy.Role{}
	}
	f.memberships[orgID][userID] = role
	return nil
}

func (f *fakeSetupStore) SaveSetting(_ context.Context, key string, value []byte) error {
	if err := f.take("SaveSetting"); err != nil {
		return err
	}
	f.settings[key] = value
	return nil
}

func (f *fakeSetupStore) LoadSetting(_ context.Context, key string) ([]byte, error) {
	if err := f.take("LoadSetting"); err != nil {
		return nil, err
	}
	v, ok := f.settings[key]
	if !ok {
		return nil, setup.ErrNotFound
	}
	return v, nil
}

func cloneUsers(m map[uuid.UUID]fakeUser) map[uuid.UUID]fakeUser {
	out := make(map[uuid.UUID]fakeUser, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneOrgs(m map[uuid.UUID]tenancy.Org) map[uuid.UUID]tenancy.Org {
	out := make(map[uuid.UUID]tenancy.Org, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneMemberships(m map[uuid.UUID]map[uuid.UUID]tenancy.Role) map[uuid.UUID]map[uuid.UUID]tenancy.Role {
	out := make(map[uuid.UUID]map[uuid.UUID]tenancy.Role, len(m))
	for k, v := range m {
		inner := make(map[uuid.UUID]tenancy.Role, len(v))
		for k2, v2 := range v {
			inner[k2] = v2
		}
		out[k] = inner
	}
	return out
}

func cloneSettings(m map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(m))
	for k, v := range m {
		cp := make([]byte, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

// fakeSessions is a setup.SessionStore that always succeeds. It panics if
// asked for an MFA-complete session: Complete must always open one with
// mfaComplete = false, since the freshly created administrator has no
// second factor enrolled yet (see Result.Cookie's own doc comment) — a
// mistake here would otherwise only surface as a real session silently
// skipping required MFA enrolment.
type fakeSessions struct{}

func (f *fakeSessions) Create(_ context.Context, userID uuid.UUID, mfaComplete bool) (uuid.UUID, string, error) {
	if mfaComplete {
		panic("setup.Complete must never open an MFA-complete session: the admin has no second factor enrolled yet")
	}
	return uuid.Must(uuid.NewV7()), "cookie-for-" + userID.String(), nil
}

var ctx = context.Background()

func TestCompleteCreatesAdminOrgAndSession(t *testing.T) {
	st := newFakeSetupStore()
	svc := setup.NewService(st, &fakeSessions{}, crypto.NewBox([32]byte{}), time.Now)
	res, err := svc.Complete(ctx, setup.Input{AdminEmail: "root@x.test", AdminPassword: "correct horse battery", AdminName: "Root", OrgSlug: "acme", OrgName: "Acme"})
	if err != nil || res.Cookie == "" {
		t.Fatal(err)
	}
	u := st.users[res.UserID]
	if !u.InstanceAdmin || u.PasswordHash == nil {
		t.Fatalf("admin %+v", u)
	}
	if st.memberships[res.OrgID][res.UserID] != tenancy.RoleOwner {
		t.Fatal("admin is not owner")
	}
	if _, err := svc.Complete(ctx, setup.Input{AdminEmail: "again@x.test", AdminPassword: "correct horse battery", AdminName: "A", OrgSlug: "b", OrgName: "B"}); !errors.Is(err, setup.ErrAlreadyCompleted) {
		t.Fatalf("second complete: %v", err)
	}
}

func TestCompleteValidatesInput(t *testing.T) {
	svc := setup.NewService(newFakeSetupStore(), &fakeSessions{}, crypto.NewBox([32]byte{}), time.Now)
	for name, in := range map[string]setup.Input{
		"short password": {AdminEmail: "a@x.test", AdminPassword: "short", AdminName: "A", OrgSlug: "acme", OrgName: "Acme"},
		"bad slug":       {AdminEmail: "a@x.test", AdminPassword: "correct horse battery", AdminName: "A", OrgSlug: "Acme Inc", OrgName: "Acme"},
		"no email":       {AdminPassword: "correct horse battery", AdminName: "A", OrgSlug: "acme", OrgName: "Acme"},
		"no name":        {AdminEmail: "a@x.test", AdminPassword: "correct horse battery", OrgSlug: "acme", OrgName: "Acme"},
		"no org name":    {AdminEmail: "a@x.test", AdminPassword: "correct horse battery", AdminName: "A", OrgSlug: "acme"},
		"slug too short": {AdminEmail: "a@x.test", AdminPassword: "correct horse battery", AdminName: "A", OrgSlug: "a-", OrgName: "Acme"},
	} {
		if _, err := svc.Complete(ctx, in); !errors.Is(err, setup.ErrInvalidInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestCompleteSlugBoundaries checks the slug pattern's edges directly,
// rather than relying only on the "bad slug" case above: a single
// character must be accepted (the pattern's optional middle group), and a
// leading or trailing hyphen must not be.
func TestCompleteSlugBoundaries(t *testing.T) {
	for slug, wantValid := range map[string]bool{
		"a": true,
		// "ac" is deliberately false: the brief's pattern
		// ^[a-z0-9](?:[a-z0-9-]{1,38}[a-z0-9])?$ requires the optional
		// group, when present, to itself be at least 3 characters (1 body
		// + 1 tail, with {1,38} demanding at least one of the former) —
		// so exactly 1 or 3+ characters validate, never exactly 2. This is
		// a property of the literal regex the brief specifies, not a
		// mistake in this implementation.
		"ac":                    false,
		"acm":                   true,
		"acme-corp":             true,
		"acme123":               true,
		"-acme":                 false,
		"acme-":                 false,
		"Acme":                  false,
		"acme_corp":             false,
		"":                      false,
		strings.Repeat("a", 40): true,
		strings.Repeat("a", 41): false,
	} {
		st := newFakeSetupStore()
		svc := setup.NewService(st, &fakeSessions{}, crypto.NewBox([32]byte{}), time.Now)
		_, err := svc.Complete(ctx, setup.Input{AdminEmail: "a@x.test", AdminPassword: "correct horse battery", AdminName: "A", OrgSlug: slug, OrgName: "Acme"})
		gotValid := !errors.Is(err, setup.ErrInvalidInput)
		if gotValid != wantValid {
			t.Errorf("slug %q: err = %v, want valid=%v", slug, err, wantValid)
		}
	}
}

func TestCompleteWithSMTPStoresSealedSettings(t *testing.T) {
	st := newFakeSetupStore()
	svc := setup.NewService(st, &fakeSessions{}, crypto.NewBox([32]byte{}), time.Now)
	smtp := mail.SMTPSettings{Host: "smtp.x.test", Port: 587, Username: "u", Password: "p", From: "pulsix@x.test", StartTLS: true}
	if _, err := svc.Complete(ctx, setup.Input{
		AdminEmail: "root@x.test", AdminPassword: "correct horse battery", AdminName: "Root", OrgSlug: "acme", OrgName: "Acme", SMTP: &smtp,
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.settings["smtp"]; !ok {
		t.Fatal("Complete with SMTP settings did not persist them")
	}
	out, err := svc.LoadSMTP(ctx)
	if err != nil || out != smtp {
		t.Fatalf("LoadSMTP after Complete: %+v %v", out, err)
	}
}

// TestCompleteRollsBackOnFailure exercises the "runs in one transaction"
// guarantee directly: a failure partway through (AddMembership, the last
// write before SMTP) must leave no admin and no org behind, not a
// half-finished setup a caller could never recover from except by manually
// fixing the database.
func TestCompleteRollsBackOnFailure(t *testing.T) {
	st := newFakeSetupStore()
	st.failNext["AddMembership"] = errors.New("simulated failure")
	svc := setup.NewService(st, &fakeSessions{}, crypto.NewBox([32]byte{}), time.Now)
	if _, err := svc.Complete(ctx, setup.Input{AdminEmail: "root@x.test", AdminPassword: "correct horse battery", AdminName: "Root", OrgSlug: "acme", OrgName: "Acme"}); err == nil {
		t.Fatal("expected the simulated failure to propagate")
	}
	if len(st.users) != 0 || len(st.orgs) != 0 {
		t.Fatalf("a failed Complete left state behind: users=%v orgs=%v", st.users, st.orgs)
	}
	// A retry after the rollback must succeed — nothing was left reserved.
	if _, err := svc.Complete(ctx, setup.Input{AdminEmail: "root@x.test", AdminPassword: "correct horse battery", AdminName: "Root", OrgSlug: "acme", OrgName: "Acme"}); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
}

func TestSMTPSettingsRoundTripSealsPassword(t *testing.T) {
	st := newFakeSetupStore()
	svc := setup.NewService(st, &fakeSessions{}, crypto.NewBox([32]byte{}), time.Now)
	in := mail.SMTPSettings{Host: "smtp.x", Port: 587, Username: "u", Password: "p", From: "pulsix@x", StartTLS: true}
	if err := svc.SaveSMTP(ctx, in); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(st.settings["smtp"]), `"p"`) {
		t.Fatal("password stored in clear")
	}
	out, err := svc.LoadSMTP(ctx)
	if err != nil || out != in {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestLoadSMTPBeforeAnySaveReportsNotFound(t *testing.T) {
	svc := setup.NewService(newFakeSetupStore(), &fakeSessions{}, crypto.NewBox([32]byte{}), time.Now)
	if _, err := svc.LoadSMTP(ctx); !errors.Is(err, setup.ErrNotFound) {
		t.Fatalf("LoadSMTP before any save: %v, want ErrNotFound", err)
	}
}

func TestStatusReportsCompletionAndMFAEnrolment(t *testing.T) {
	st := newFakeSetupStore()
	svc := setup.NewService(st, &fakeSessions{}, crypto.NewBox([32]byte{}), time.Now)

	st0, err := svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st0.Completed {
		t.Fatal("a fresh instance must report Completed = false")
	}

	if _, err := svc.Complete(ctx, setup.Input{AdminEmail: "root@x.test", AdminPassword: "correct horse battery", AdminName: "Root", OrgSlug: "acme", OrgName: "Acme"}); err != nil {
		t.Fatal(err)
	}
	st1, err := svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st1.Completed {
		t.Fatal("after Complete, Status must report Completed = true")
	}
	if st1.Version == "" {
		t.Fatal("Status must report a non-empty Version")
	}
}
