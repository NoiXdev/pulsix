package personaltokens_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/personaltokens"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type grantKey struct{ token, org uuid.UUID }

type grantRow struct {
	role    tenancy.Role
	blocked bool
}

// fakeStore is the in-memory personaltokens.Store.
type fakeStore struct {
	grants   map[grantKey]grantRow
	off      map[uuid.UUID]bool
	touched  []grantKey
	capErr   error
	allowErr error
	touchErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{grants: map[grantKey]grantRow{}, off: map[uuid.UUID]bool{}}
}

func (f *fakeStore) Cap(_ context.Context, tokenID, orgID uuid.UUID) (tenancy.Role, bool, error) {
	if f.capErr != nil {
		return "", false, f.capErr
	}
	g, ok := f.grants[grantKey{tokenID, orgID}]
	if !ok {
		return "", false, personaltokens.ErrNotFound
	}
	return g.role, g.blocked, nil
}

func (f *fakeStore) Allowed(_ context.Context, orgID uuid.UUID) (bool, error) {
	if f.allowErr != nil {
		return false, f.allowErr
	}
	return !f.off[orgID], nil
}

func (f *fakeStore) TouchGrant(_ context.Context, tokenID, orgID uuid.UUID) error {
	f.touched = append(f.touched, grantKey{tokenID, orgID})
	return f.touchErr
}

// fakeMembers answers MembershipOf from a map.
type fakeMembers map[grantKey]tenancy.Role // keyed by (user, org)

func (m fakeMembers) MembershipOf(_ context.Context, orgID, userID uuid.UUID) (tenancy.Role, error) {
	r, ok := m[grantKey{userID, orgID}]
	if !ok {
		return "", tenancy.ErrNotMember
	}
	return r, nil
}

func ids() (token, org uuid.UUID) { return uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()) }

func TestCapActiveGrant(t *testing.T) {
	st := newFakeStore()
	tok, org := ids()
	st.grants[grantKey{tok, org}] = grantRow{role: tenancy.RoleOperator}
	got, err := personaltokens.NewService(st, fakeMembers{}).Cap(context.Background(), tok, org)
	if err != nil || got != tenancy.RoleOperator {
		t.Fatalf("Cap = %q, %v; want operator", got, err)
	}
}

func TestCapNoGrant(t *testing.T) {
	st := newFakeStore()
	tok, org := ids()
	// A grant for another organization does not count here.
	st.grants[grantKey{tok, uuid.Must(uuid.NewV7())}] = grantRow{role: tenancy.RoleOwner}
	if _, err := personaltokens.NewService(st, fakeMembers{}).Cap(context.Background(), tok, org); !errors.Is(err, personaltokens.ErrNoGrant) {
		t.Fatalf("Cap err = %v, want ErrNoGrant", err)
	}
}

func TestCapBlocked(t *testing.T) {
	st := newFakeStore()
	tok, org := ids()
	st.grants[grantKey{tok, org}] = grantRow{role: tenancy.RoleAdmin, blocked: true}
	if _, err := personaltokens.NewService(st, fakeMembers{}).Cap(context.Background(), tok, org); !errors.Is(err, personaltokens.ErrNoGrant) {
		t.Fatalf("Cap err = %v, want ErrNoGrant", err)
	}
}

func TestCapSwitchedOff(t *testing.T) {
	st := newFakeStore()
	tok, org := ids()
	st.grants[grantKey{tok, org}] = grantRow{role: tenancy.RoleAdmin}
	st.off[org] = true
	svc := personaltokens.NewService(st, fakeMembers{})
	if _, err := svc.Cap(context.Background(), tok, org); !errors.Is(err, personaltokens.ErrSwitchedOff) {
		t.Fatalf("Cap err = %v, want ErrSwitchedOff", err)
	}
	// On again: the grant works again.
	st.off[org] = false
	if got, err := svc.Cap(context.Background(), tok, org); err != nil || got != tenancy.RoleAdmin {
		t.Fatalf("Cap after switching on = %q, %v", got, err)
	}
}

// A store failure is never folded into a refusal: it propagates, so the
// caller fails closed with a server error instead of a 403.
func TestCapStoreFailuresPropagate(t *testing.T) {
	boom := errors.New("connection reset")
	for name, spoil := range map[string]func(*fakeStore){
		"grant":  func(f *fakeStore) { f.capErr = boom },
		"switch": func(f *fakeStore) { f.allowErr = boom },
	} {
		t.Run(name, func(t *testing.T) {
			st := newFakeStore()
			tok, org := ids()
			st.grants[grantKey{tok, org}] = grantRow{role: tenancy.RoleViewer}
			spoil(st)
			_, err := personaltokens.NewService(st, fakeMembers{}).Cap(context.Background(), tok, org)
			if !errors.Is(err, boom) || errors.Is(err, personaltokens.ErrNoGrant) || errors.Is(err, personaltokens.ErrSwitchedOff) {
				t.Fatalf("Cap err = %v, want the store's error", err)
			}
		})
	}
}

func TestTouchGrantIsBestEffort(t *testing.T) {
	st := newFakeStore()
	tok, org := ids()
	svc := personaltokens.NewService(st, fakeMembers{})
	svc.TouchGrant(context.Background(), tok, org)
	st.touchErr = errors.New("write failed")
	svc.TouchGrant(context.Background(), tok, org) // logged, not returned
	if len(st.touched) != 2 || st.touched[0] != (grantKey{tok, org}) {
		t.Fatalf("touches = %v", st.touched)
	}
}

func TestEffectiveRole(t *testing.T) {
	user := uuid.Must(uuid.NewV7())
	cases := []struct {
		name   string
		member tenancy.Role // "" = not a member
		grant  *grantRow
		off    bool
		want   tenancy.Role
		err    error
	}{
		{name: "cap below membership", member: tenancy.RoleAdmin, grant: &grantRow{role: tenancy.RoleViewer}, want: tenancy.RoleViewer},
		{name: "membership below cap", member: tenancy.RoleOperator, grant: &grantRow{role: tenancy.RoleOwner}, want: tenancy.RoleOperator},
		{name: "not a member", grant: &grantRow{role: tenancy.RoleOwner}, err: tenancy.ErrNotMember},
		{name: "no grant", member: tenancy.RoleOwner, err: personaltokens.ErrNoGrant},
		{name: "blocked", member: tenancy.RoleOwner, grant: &grantRow{role: tenancy.RoleOwner, blocked: true}, err: personaltokens.ErrNoGrant},
		{name: "switched off", member: tenancy.RoleOwner, grant: &grantRow{role: tenancy.RoleOwner}, off: true, err: personaltokens.ErrSwitchedOff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newFakeStore()
			tok, org := ids()
			if tc.grant != nil {
				st.grants[grantKey{tok, org}] = *tc.grant
			}
			st.off[org] = tc.off
			members := fakeMembers{}
			if tc.member != "" {
				members[grantKey{user, org}] = tc.member
			}
			got, err := personaltokens.NewService(st, members).EffectiveRole(context.Background(), tok, org, user)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("err = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("EffectiveRole = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
