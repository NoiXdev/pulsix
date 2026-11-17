package personaltokens_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/personaltokens"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// world is one user with memberships in a few organizations, on a fake
// store: A (admin), B (operator), C (viewer); D exists but the user is not
// a member there.
type world struct {
	st      *fakeStore
	members fakeMembers
	svc     *personaltokens.Service
	user    uuid.UUID
	a, b, c uuid.UUID
	d       uuid.UUID
}

func newWorld() world {
	w := world{st: newFakeStore(), members: fakeMembers{}, user: uuid.Must(uuid.NewV7())}
	for i, role := range []tenancy.Role{tenancy.RoleAdmin, tenancy.RoleOperator, tenancy.RoleViewer, ""} {
		org := uuid.Must(uuid.NewV7())
		slug := string(rune('a' + i))
		w.st.orgs[org] = orgInfo{slug: "org-" + slug, name: "Org " + strings.ToUpper(slug)}
		if role != "" {
			w.members[grantKey{w.user, org}] = role
		}
		switch i {
		case 0:
			w.a = org
		case 1:
			w.b = org
		case 2:
			w.c = org
		case 3:
			w.d = org
		}
	}
	w.svc = newService(w.st, w.members)
	return w
}

func (w world) create(t *testing.T, in personaltokens.NewToken) personaltokens.Token {
	t.Helper()
	_, tok, err := w.svc.Create(context.Background(), w.user, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return tok
}

func api(grants ...personaltokens.GrantInput) personaltokens.NewToken {
	return personaltokens.NewToken{Name: "deploy", Kind: auth.TokenKindAPI, Grants: grants}
}

func g(org uuid.UUID, role tenancy.Role) personaltokens.GrantInput {
	return personaltokens.GrantInput{OrgID: org, Role: role}
}

func TestCreateValidates(t *testing.T) {
	w := newWorld()
	past := fixedNow.Add(-time.Second)
	tooMany := make([]personaltokens.GrantInput, personaltokens.MaxGrantsPerToken+1)
	for i := range tooMany {
		tooMany[i] = g(uuid.Must(uuid.NewV7()), tenancy.RoleViewer)
	}
	assistant := func(areas []string, grants ...personaltokens.GrantInput) personaltokens.NewToken {
		return personaltokens.NewToken{Name: "bot", Kind: auth.TokenKindAssistant, Areas: areas, Grants: grants}
	}
	cases := []struct {
		name string
		in   personaltokens.NewToken
		err  error
	}{
		{"no grant", api(), personaltokens.ErrInvalid},
		{"51 grants", api(tooMany...), personaltokens.ErrInvalid},
		{"same organization twice", api(g(w.a, tenancy.RoleViewer), g(w.a, tenancy.RoleAdmin)), personaltokens.ErrInvalid},
		{"blank name", personaltokens.NewToken{Name: "  ", Grants: []personaltokens.GrantInput{g(w.a, tenancy.RoleViewer)}}, personaltokens.ErrInvalid},
		{"unknown kind", personaltokens.NewToken{Name: "x", Kind: "root", Grants: []personaltokens.GrantInput{g(w.a, tenancy.RoleViewer)}}, personaltokens.ErrInvalid},
		{"unknown role", api(g(w.a, tenancy.Role("root"))), personaltokens.ErrInvalid},
		{"expiry in the past", personaltokens.NewToken{Name: "x", ExpiresAt: &past, Grants: []personaltokens.GrantInput{g(w.a, tenancy.RoleViewer)}}, personaltokens.ErrInvalid},
		{"expiry now", personaltokens.NewToken{Name: "x", ExpiresAt: &fixedNow, Grants: []personaltokens.GrantInput{g(w.a, tenancy.RoleViewer)}}, personaltokens.ErrInvalid},
		{"cap above role", api(g(w.a, tenancy.RoleAdmin), g(w.b, tenancy.RoleAdmin)), personaltokens.ErrAboveRole},
		{"owner cap held by an admin", api(g(w.a, tenancy.RoleOwner)), personaltokens.ErrAboveRole},
		{"not a member", api(g(w.d, tenancy.RoleViewer)), personaltokens.ErrNotMember},
		{"areas on an api token", personaltokens.NewToken{Name: "x", Kind: auth.TokenKindAPI, Areas: []string{"jobs.read"}, Grants: []personaltokens.GrantInput{g(w.a, tenancy.RoleViewer)}}, personaltokens.ErrInvalid},
		{"assistant without areas", assistant(nil, g(w.a, tenancy.RoleViewer)), personaltokens.ErrInvalid},
		{"unknown area", assistant([]string{"root.everything"}, g(w.a, tenancy.RoleViewer)), personaltokens.ErrInvalid},
		{"area twice", assistant([]string{"jobs.read", "jobs.read"}, g(w.a, tenancy.RoleViewer)), personaltokens.ErrInvalid},
		{"area beyond the highest cap", assistant([]string{"inventory.write"}, g(w.a, tenancy.RoleViewer), g(w.c, tenancy.RoleViewer)), personaltokens.ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := w.svc.Create(context.Background(), w.user, tc.in)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if len(w.st.tokens) != 0 {
				t.Fatalf("a refused create stored %d tokens", len(w.st.tokens))
			}
		})
	}
}

func TestCreateGrantErrorNamesTheOrganization(t *testing.T) {
	w := newWorld()
	_, _, err := w.svc.Create(context.Background(), w.user, api(g(w.a, tenancy.RoleViewer), g(w.b, tenancy.RoleOwner)))
	var ge *personaltokens.GrantError
	if !errors.As(err, &ge) || ge.OrgID != w.b || !errors.Is(err, personaltokens.ErrAboveRole) {
		t.Fatalf("err = %#v, want a GrantError for B wrapping ErrAboveRole", err)
	}
}

func TestCreateRefusedWhileSwitchedOff(t *testing.T) {
	w := newWorld()
	w.st.off[w.b] = true
	_, _, err := w.svc.Create(context.Background(), w.user, api(g(w.a, tenancy.RoleViewer), g(w.b, tenancy.RoleViewer)))
	if !errors.Is(err, personaltokens.ErrSwitchedOff) {
		t.Fatalf("err = %v, want ErrSwitchedOff", err)
	}
}

func TestCreateAssistantWithinHighestCap(t *testing.T) {
	w := newWorld()
	tok := w.create(t, personaltokens.NewToken{Name: "bot", Kind: auth.TokenKindAssistant,
		Areas:  []string{"jobs.read", "inventory.write"},
		Grants: []personaltokens.GrantInput{g(w.c, tenancy.RoleViewer), g(w.b, tenancy.RoleOperator)}})
	// Stored in the canonical area order.
	if !slices.Equal(tok.Areas, []string{"inventory.write", "jobs.read"}) {
		t.Fatalf("areas = %v", tok.Areas)
	}
}

func TestCreateAnswersTheStoredToken(t *testing.T) {
	w := newWorld()
	w.st.off[w.c] = false
	exp := fixedNow.Add(48*time.Hour + 987)
	plain, tok, err := w.svc.Create(context.Background(), w.user, personaltokens.NewToken{
		Name: " deploy ", ExpiresAt: &exp, Grants: []personaltokens.GrantInput{g(w.b, tenancy.RoleViewer), g(w.a, tenancy.RoleAdmin)}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, "pxp_") || len(plain) != 47 {
		t.Fatalf("plain = %q", plain)
	}
	if w.st.inTx != 1 {
		t.Fatalf("InTx calls = %d, want 1", w.st.inTx)
	}
	if tok.Name != "deploy" || tok.Kind != auth.TokenKindAPI || tok.UserID != w.user {
		t.Fatalf("token = %+v", tok.PersonalToken)
	}
	if !tok.CreatedAt.Equal(fixedNow.Truncate(time.Microsecond)) || !tok.ExpiresAt.Equal(exp.Truncate(time.Microsecond)) {
		t.Fatalf("created %v, expires %v: want microseconds", tok.CreatedAt, tok.ExpiresAt)
	}
	want := []personaltokens.Grant{
		{OrgID: w.a, OrgSlug: "org-a", OrgName: "Org A", Role: tenancy.RoleAdmin, State: personaltokens.GrantActive},
		{OrgID: w.b, OrgSlug: "org-b", OrgName: "Org B", Role: tenancy.RoleViewer, State: personaltokens.GrantActive},
	}
	if !slices.EqualFunc(tok.Grants, want, sameGrant) {
		t.Fatalf("grants = %+v, want %+v", tok.Grants, want)
	}
}

func sameGrant(a, b personaltokens.Grant) bool {
	return a.OrgID == b.OrgID && a.OrgSlug == b.OrgSlug && a.OrgName == b.OrgName && a.Role == b.Role && a.State == b.State
}

func TestCreateLimit(t *testing.T) {
	w := newWorld()
	for range personaltokens.MaxTokensPerUser {
		w.create(t, api(g(w.c, tenancy.RoleViewer)))
	}
	if _, _, err := w.svc.Create(context.Background(), w.user, api(g(w.c, tenancy.RoleViewer))); !errors.Is(err, personaltokens.ErrLimit) {
		t.Fatalf("51st token: err = %v, want ErrLimit", err)
	}
	// A revoked token frees its place.
	if _, err := w.svc.Revoke(context.Background(), w.user, w.st.order[0]); err != nil {
		t.Fatal(err)
	}
	w.create(t, api(g(w.c, tenancy.RoleViewer)))
	// Another user's tokens do not count.
	other := uuid.Must(uuid.NewV7())
	w.members[grantKey{other, w.c}] = tenancy.RoleViewer
	if _, _, err := w.svc.Create(context.Background(), other, api(g(w.c, tenancy.RoleViewer))); err != nil {
		t.Fatalf("another user's first token: %v", err)
	}
}

func TestCreateStoreFailurePropagates(t *testing.T) {
	w := newWorld()
	boom := errors.New("connection reset")
	w.st.failErr = boom
	if _, _, err := w.svc.Create(context.Background(), w.user, api(g(w.a, tenancy.RoleViewer))); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store's", err)
	}
	w.st.failErr = nil
	w.st.allowErr = boom
	if _, _, err := w.svc.Create(context.Background(), w.user, api(g(w.a, tenancy.RoleViewer))); !errors.Is(err, boom) {
		t.Fatalf("switch read failure: err = %v, want the store's", err)
	}
}

func TestListShowsGrantStates(t *testing.T) {
	w := newWorld()
	tok := w.create(t, api(g(w.a, tenancy.RoleAdmin), g(w.b, tenancy.RoleOperator), g(w.c, tenancy.RoleViewer)))
	w.st.grants[grantKey{tok.ID, w.a}] = grantRow{role: tenancy.RoleAdmin, blocked: true}
	w.st.off[w.a] = true // blocked wins over off
	w.st.off[w.b] = true
	second := w.create(t, api(g(w.c, tenancy.RoleViewer)))
	// Another user's token is not listed.
	other := uuid.Must(uuid.NewV7())
	w.members[grantKey{other, w.c}] = tenancy.RoleViewer
	if _, _, err := w.svc.Create(context.Background(), other, api(g(w.c, tenancy.RoleViewer))); err != nil {
		t.Fatal(err)
	}

	list, err := w.svc.List(context.Background(), w.user)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != tok.ID || list[1].ID != second.ID {
		t.Fatalf("list = %+v", list)
	}
	states := map[uuid.UUID]personaltokens.GrantState{}
	for _, gr := range list[0].Grants {
		states[gr.OrgID] = gr.State
	}
	want := map[uuid.UUID]personaltokens.GrantState{w.a: personaltokens.GrantBlocked, w.b: personaltokens.GrantOff, w.c: personaltokens.GrantActive}
	for org, s := range want {
		if states[org] != s {
			t.Fatalf("states = %v, want %v", states, want)
		}
	}
	if len(list[1].Grants) != 1 {
		t.Fatalf("second token grants = %+v", list[1].Grants)
	}
}

func TestUpdateReportsGrantChanges(t *testing.T) {
	w := newWorld()
	tok := w.create(t, api(g(w.a, tenancy.RoleOperator), g(w.b, tenancy.RoleViewer), g(w.c, tenancy.RoleViewer)))
	// An admin of C blocked the grant there.
	w.st.grants[grantKey{tok.ID, w.c}] = grantRow{role: tenancy.RoleViewer, blocked: true}
	extra := uuid.Must(uuid.NewV7())
	w.st.orgs[extra] = orgInfo{slug: "org-e", name: "Org E"}
	w.members[grantKey{w.user, extra}] = tenancy.RoleOwner

	grants := []personaltokens.GrantInput{g(w.a, tenancy.RoleViewer), g(extra, tenancy.RoleOwner)}
	before, after, changes, err := w.svc.Update(context.Background(), w.user, tok.ID, personaltokens.Change{Grants: &grants})
	if err != nil {
		t.Fatal(err)
	}
	wantChanges := []personaltokens.GrantChange{
		{OrgID: w.a, Kind: personaltokens.GrantChanged, From: tenancy.RoleOperator, To: tenancy.RoleViewer},
		{OrgID: w.b, Kind: personaltokens.GrantRemoved, From: tenancy.RoleViewer},
		{OrgID: extra, Kind: personaltokens.GrantAdded, To: tenancy.RoleOwner},
	}
	sortChanges := func(cs []personaltokens.GrantChange) {
		slices.SortFunc(cs, func(x, y personaltokens.GrantChange) int { return strings.Compare(x.OrgID.String(), y.OrgID.String()) })
	}
	sortChanges(changes)
	sortChanges(wantChanges)
	if !slices.Equal(changes, wantChanges) {
		t.Fatalf("changes = %+v, want %+v", changes, wantChanges)
	}
	if len(before.Grants) != 3 {
		t.Fatalf("before = %+v", before.Grants)
	}
	got := map[uuid.UUID]personaltokens.Grant{}
	for _, gr := range after.Grants {
		got[gr.OrgID] = gr
	}
	if len(got) != 3 || got[w.a].Role != tenancy.RoleViewer || got[extra].Role != tenancy.RoleOwner {
		t.Fatalf("after = %+v", after.Grants)
	}
	// The blocked grant survives the edit unchanged.
	if gc, ok := got[w.c]; !ok || gc.State != personaltokens.GrantBlocked || gc.Role != tenancy.RoleViewer {
		t.Fatalf("blocked grant after the edit = %+v (present %v)", gc, ok)
	}
}

func TestUpdateRefusals(t *testing.T) {
	future := fixedNow.Add(time.Hour)
	past := fixedNow.Add(-time.Hour)
	name := "renamed"
	blank := " "
	areas := []string{"jobs.read"}
	cases := []struct {
		name  string
		setup func(w world, tok personaltokens.Token)
		ch    func(w world) personaltokens.Change
		err   error
	}{
		{name: "nothing to change", ch: func(world) personaltokens.Change { return personaltokens.Change{} }, err: personaltokens.ErrInvalid},
		{name: "blank name", ch: func(world) personaltokens.Change { return personaltokens.Change{Name: &blank} }, err: personaltokens.ErrInvalid},
		{name: "areas on an api token", ch: func(world) personaltokens.Change { return personaltokens.Change{Areas: &areas} }, err: personaltokens.ErrInvalid},
		{name: "expiry in the past", ch: func(world) personaltokens.Change {
			return personaltokens.Change{Expiry: auth.ExpirySet, ExpiresAt: &past}
		}, err: personaltokens.ErrInvalid},
		{name: "expiry set without a time", ch: func(world) personaltokens.Change {
			return personaltokens.Change{Expiry: auth.ExpirySet}
		}, err: personaltokens.ErrInvalid},
		{name: "unknown expiry change", ch: func(world) personaltokens.Change {
			return personaltokens.Change{Name: &name, Expiry: auth.ExpiryChange(9)}
		}, err: personaltokens.ErrInvalid},
		{name: "empty grant set", ch: func(world) personaltokens.Change {
			return personaltokens.Change{Grants: &[]personaltokens.GrantInput{}}
		}, err: personaltokens.ErrInvalid},
		{name: "same organization twice", ch: func(w world) personaltokens.Change {
			return personaltokens.Change{Grants: &[]personaltokens.GrantInput{g(w.a, tenancy.RoleViewer), g(w.a, tenancy.RoleViewer)}}
		}, err: personaltokens.ErrInvalid},
		{name: "listing a blocked organization", setup: func(w world, tok personaltokens.Token) {
			w.st.grants[grantKey{tok.ID, w.b}] = grantRow{role: tenancy.RoleViewer, blocked: true}
		}, ch: func(w world) personaltokens.Change {
			return personaltokens.Change{Grants: &[]personaltokens.GrantInput{g(w.a, tenancy.RoleOperator), g(w.b, tenancy.RoleViewer)}}
		}, err: personaltokens.ErrBlocked},
		{name: "a new organization while switched off", setup: func(w world, _ personaltokens.Token) {
			w.st.off[w.c] = true
		}, ch: func(w world) personaltokens.Change {
			return personaltokens.Change{Grants: &[]personaltokens.GrantInput{g(w.a, tenancy.RoleOperator), g(w.b, tenancy.RoleViewer), g(w.c, tenancy.RoleViewer)}}
		}, err: personaltokens.ErrSwitchedOff},
		{name: "raising a cap while switched off", setup: func(w world, _ personaltokens.Token) {
			w.st.off[w.b] = true
		}, ch: func(w world) personaltokens.Change {
			return personaltokens.Change{Grants: &[]personaltokens.GrantInput{g(w.a, tenancy.RoleOperator), g(w.b, tenancy.RoleOperator)}}
		}, err: personaltokens.ErrSwitchedOff},
		{name: "cap above role", ch: func(w world) personaltokens.Change {
			return personaltokens.Change{Grants: &[]personaltokens.GrantInput{g(w.a, tenancy.RoleOwner), g(w.b, tenancy.RoleViewer)}}
		}, err: personaltokens.ErrAboveRole},
		{name: "not a member", ch: func(w world) personaltokens.Change {
			return personaltokens.Change{Grants: &[]personaltokens.GrantInput{g(w.d, tenancy.RoleViewer)}}
		}, err: personaltokens.ErrNotMember},
		{name: "expired token", setup: func(w world, tok personaltokens.Token) {
			w.st.tokens[tok.ID].tok.ExpiresAt = &past
		}, ch: func(world) personaltokens.Change { return personaltokens.Change{Name: &name} }, err: auth.ErrTokenExpired},
		{name: "revoked token", setup: func(w world, tok personaltokens.Token) {
			w.st.tokens[tok.ID].revoked = true
		}, ch: func(world) personaltokens.Change { return personaltokens.Change{Name: &name} }, err: personaltokens.ErrNotFound},
		{name: "store failure", setup: func(w world, _ personaltokens.Token) {
			w.st.failErr = errors.New("boom")
		}, ch: func(world) personaltokens.Change {
			return personaltokens.Change{Name: &name, Expiry: auth.ExpirySet, ExpiresAt: &future}
		}, err: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld()
			tok := w.create(t, api(g(w.a, tenancy.RoleOperator), g(w.b, tenancy.RoleViewer)))
			if tc.setup != nil {
				tc.setup(w, tok)
			}
			_, _, _, err := w.svc.Update(context.Background(), w.user, tok.ID, tc.ch(w))
			if tc.err == nil {
				if err == nil {
					t.Fatal("err = nil, want the store's error")
				}
				return
			}
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
		})
	}
}

func TestUpdateOtherUsersTokenNotFound(t *testing.T) {
	w := newWorld()
	tok := w.create(t, api(g(w.a, tenancy.RoleViewer)))
	name := "mine now"
	if _, _, _, err := w.svc.Update(context.Background(), uuid.Must(uuid.NewV7()), tok.ID, personaltokens.Change{Name: &name}); !errors.Is(err, personaltokens.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := w.svc.Revoke(context.Background(), uuid.Must(uuid.NewV7()), tok.ID); !errors.Is(err, personaltokens.ErrNotFound) {
		t.Fatalf("revoke: err = %v, want ErrNotFound", err)
	}
}

// An unchanged grant is not re-validated: a cap above a membership that was
// lowered since does not block renaming the token (the effective role is
// the lower of the two at every use anyway). Lowering is always allowed,
// even while the organization's switch is off.
func TestUpdateKeepsUnchangedGrantAndLowersWhileOff(t *testing.T) {
	w := newWorld()
	tok := w.create(t, api(g(w.a, tenancy.RoleAdmin), g(w.b, tenancy.RoleOperator)))
	w.members[grantKey{w.user, w.a}] = tenancy.RoleViewer // demoted
	w.st.off[w.b] = true
	name := "renamed"
	grants := []personaltokens.GrantInput{g(w.a, tenancy.RoleAdmin), g(w.b, tenancy.RoleViewer)}
	_, after, changes, err := w.svc.Update(context.Background(), w.user, tok.ID, personaltokens.Change{Name: &name, Grants: &grants})
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != "renamed" || len(changes) != 1 || changes[0].OrgID != w.b || changes[0].Kind != personaltokens.GrantChanged {
		t.Fatalf("after %q, changes %+v", after.Name, changes)
	}
}

func TestUpdateNameAndExpiry(t *testing.T) {
	w := newWorld()
	tok := w.create(t, api(g(w.a, tenancy.RoleViewer)))
	future := fixedNow.Add(24*time.Hour + 999)
	name := "  renamed "
	before, after, changes, err := w.svc.Update(context.Background(), w.user, tok.ID, personaltokens.Change{Name: &name, Expiry: auth.ExpirySet, ExpiresAt: &future})
	if err != nil {
		t.Fatal(err)
	}
	if before.Name != "deploy" || after.Name != "renamed" || after.ExpiresAt == nil || !after.ExpiresAt.Equal(future.Truncate(time.Microsecond)) || len(changes) != 0 {
		t.Fatalf("before %+v, after %+v, changes %v", before.PersonalToken, after.PersonalToken, changes)
	}
	_, after, _, err = w.svc.Update(context.Background(), w.user, tok.ID, personaltokens.Change{Expiry: auth.ExpiryClear})
	if err != nil || after.ExpiresAt != nil {
		t.Fatalf("clear expiry: %+v, %v", after.PersonalToken, err)
	}
}

// Changing the grants re-validates an assistant token's areas against the
// new highest cap.
func TestUpdateGrantsRevalidateAreas(t *testing.T) {
	w := newWorld()
	tok := w.create(t, personaltokens.NewToken{Name: "bot", Kind: auth.TokenKindAssistant, Areas: []string{"inventory.write"},
		Grants: []personaltokens.GrantInput{g(w.b, tenancy.RoleOperator)}})
	lower := []personaltokens.GrantInput{g(w.b, tenancy.RoleViewer)}
	if _, _, _, err := w.svc.Update(context.Background(), w.user, tok.ID, personaltokens.Change{Grants: &lower}); !errors.Is(err, personaltokens.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	areas := []string{"jobs.read", "inventory.read"}
	_, after, _, err := w.svc.Update(context.Background(), w.user, tok.ID, personaltokens.Change{Areas: &areas, Grants: &lower})
	if err != nil || !slices.Equal(after.Areas, []string{"inventory.read", "jobs.read"}) {
		t.Fatalf("areas %v, err %v", after.Areas, err)
	}
}

func TestRevokeReturnsTheTokenAsItWas(t *testing.T) {
	w := newWorld()
	tok := w.create(t, api(g(w.a, tenancy.RoleOperator), g(w.b, tenancy.RoleViewer)))
	got, err := w.svc.Revoke(context.Background(), w.user, tok.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != tok.ID || len(got.Grants) != 2 {
		t.Fatalf("revoked = %+v", got)
	}
	if _, err := w.svc.Revoke(context.Background(), w.user, tok.ID); !errors.Is(err, personaltokens.ErrNotFound) {
		t.Fatalf("second revoke: err = %v", err)
	}
	if list, err := w.svc.List(context.Background(), w.user); err != nil || len(list) != 0 {
		t.Fatalf("list after revoke = %+v, %v", list, err)
	}
}

func TestOrgRevokeAndUnblockGrant(t *testing.T) {
	w := newWorld()
	tok := w.create(t, api(g(w.a, tenancy.RoleOperator), g(w.b, tenancy.RoleViewer)))
	admin := uuid.Must(uuid.NewV7())
	ctx := context.Background()

	entries, err := w.svc.ListForOrg(ctx, w.a)
	if err != nil || len(entries) != 1 || entries[0].TokenID != tok.ID || entries[0].Role != tenancy.RoleOperator {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
	e, err := w.svc.RevokeGrant(ctx, w.a, tok.ID, admin)
	if err != nil || !e.Blocked || e.Name != "deploy" {
		t.Fatalf("RevokeGrant = %+v, %v", e, err)
	}
	if _, err := w.svc.Cap(ctx, tok.ID, w.a); !errors.Is(err, personaltokens.ErrNoGrant) {
		t.Fatalf("Cap after revoke: %v", err)
	}
	if _, err := w.svc.RevokeGrant(ctx, w.a, tok.ID, admin); !errors.Is(err, personaltokens.ErrBlocked) {
		t.Fatalf("second revoke: %v, want ErrBlocked", err)
	}
	if _, err := w.svc.RevokeGrant(ctx, w.c, tok.ID, admin); !errors.Is(err, personaltokens.ErrNotFound) {
		t.Fatalf("revoke without a grant: %v", err)
	}
	if _, err := w.svc.UnblockGrant(ctx, w.b, tok.ID); !errors.Is(err, personaltokens.ErrNotFound) {
		t.Fatalf("unblock an unblocked grant: %v", err)
	}
	e, err = w.svc.UnblockGrant(ctx, w.a, tok.ID)
	if err != nil || e.TokenID != tok.ID || !e.Blocked {
		t.Fatalf("UnblockGrant = %+v, %v", e, err)
	}
	// Unblocking removes the grant: the owner may add it again.
	if _, err := w.svc.Cap(ctx, tok.ID, w.a); !errors.Is(err, personaltokens.ErrNoGrant) {
		t.Fatalf("Cap after unblock: %v", err)
	}
	grants := []personaltokens.GrantInput{g(w.a, tenancy.RoleViewer), g(w.b, tenancy.RoleViewer)}
	if _, _, _, err := w.svc.Update(ctx, w.user, tok.ID, personaltokens.Change{Grants: &grants}); err != nil {
		t.Fatalf("re-adding A after the unblock: %v", err)
	}
}

func TestOrgSwitchAndGrantCount(t *testing.T) {
	w := newWorld()
	ctx := context.Background()
	tok := w.create(t, api(g(w.a, tenancy.RoleOperator), g(w.b, tenancy.RoleViewer)))
	if on, err := w.svc.Allowed(ctx, w.a); err != nil || !on {
		t.Fatalf("Allowed = %v, %v", on, err)
	}
	if err := w.svc.SetAllowed(ctx, w.a, false); err != nil {
		t.Fatal(err)
	}
	if on, _ := w.svc.Allowed(ctx, w.a); on {
		t.Fatal("still allowed after switching off")
	}
	if org, n, err := w.svc.GrantCount(ctx, tok.ID); err != nil || n != 2 || org != uuid.Nil {
		t.Fatalf("GrantCount = %v, %d, %v", org, n, err)
	}
	if _, err := w.svc.RevokeGrant(ctx, w.b, tok.ID, uuid.Nil); err != nil {
		t.Fatal(err)
	}
	if org, n, err := w.svc.GrantCount(ctx, tok.ID); err != nil || n != 1 || org != w.a {
		t.Fatalf("GrantCount after the block = %v, %d, %v", org, n, err)
	}
}

// A grant that vanished between the read and the write (an admin blocked
// it meanwhile) is a conflict on that organization, not a missing token.
func TestUpdateGrantGoneMeanwhileIsBlocked(t *testing.T) {
	w := newWorld()
	tok := w.create(t, api(g(w.a, tenancy.RoleOperator), g(w.b, tenancy.RoleViewer)))
	racing := &vanishingStore{fakeStore: w.st, gone: grantKey{tok.ID, w.b}}
	svc := personaltokens.NewService(racing, w.members, func() time.Time { return fixedNow })
	grants := []personaltokens.GrantInput{g(w.a, tenancy.RoleOperator)}
	_, _, _, err := svc.Update(context.Background(), w.user, tok.ID, personaltokens.Change{Grants: &grants})
	var ge *personaltokens.GrantError
	if !errors.As(err, &ge) || ge.OrgID != w.b || !errors.Is(err, personaltokens.ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked for B", err)
	}
}

// vanishingStore answers ErrNotFound for writes to one grant, as if an
// admin had blocked it after the service read it.
type vanishingStore struct {
	*fakeStore
	gone grantKey
}

func (v *vanishingStore) DeleteGrant(ctx context.Context, tokenID, orgID uuid.UUID) error {
	if (grantKey{tokenID, orgID}) == v.gone {
		return personaltokens.ErrNotFound
	}
	return v.fakeStore.DeleteGrant(ctx, tokenID, orgID)
}

// TestBlockIsPerPersonAndOrganization (fix round 1, the owner's decision
// "an admin's revocation sticks"): once an admin revoked one of a user's
// grants in an organization, no token of that user — a new one, another
// existing one, one made after revoking the blocked token — gets a grant
// there until an admin lifts the block. Lifting it lifts every block of
// that user there.
func TestBlockIsPerPersonAndOrganization(t *testing.T) {
	w := newWorld()
	ctx := context.Background()
	blocked := w.create(t, api(g(w.a, tenancy.RoleOperator), g(w.b, tenancy.RoleViewer)))
	other := w.create(t, api(g(w.b, tenancy.RoleViewer)))
	twice := w.create(t, api(g(w.a, tenancy.RoleViewer)))
	if _, err := w.svc.RevokeGrant(ctx, w.a, blocked.ID, uuid.Nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.RevokeGrant(ctx, w.a, twice.ID, uuid.Nil); err != nil {
		t.Fatal(err)
	}
	wantBlocked := func(what string, err error) {
		t.Helper()
		var ge *personaltokens.GrantError
		if !errors.As(err, &ge) || ge.OrgID != w.a || !errors.Is(err, personaltokens.ErrBlocked) {
			t.Fatalf("%s: err = %v, want ErrBlocked for A", what, err)
		}
	}
	_, _, err := w.svc.Create(ctx, w.user, api(g(w.a, tenancy.RoleViewer)))
	wantBlocked("a new token", err)
	grants := []personaltokens.GrantInput{g(w.b, tenancy.RoleViewer), g(w.a, tenancy.RoleViewer)}
	_, _, _, err = w.svc.Update(ctx, w.user, other.ID, personaltokens.Change{Grants: &grants})
	wantBlocked("another token", err)
	if _, err := w.svc.Revoke(ctx, w.user, blocked.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Revoke(ctx, w.user, twice.ID); err != nil {
		t.Fatal(err)
	}
	_, _, err = w.svc.Create(ctx, w.user, api(g(w.a, tenancy.RoleViewer)))
	wantBlocked("a new token after revoking the blocked one", err)

	// The admin still sees the blocks, revoked tokens included.
	entries, err := w.svc.ListForOrg(ctx, w.a)
	if err != nil || len(entries) != 2 || !entries[0].Blocked || !entries[0].TokenRevoked {
		t.Fatalf("A entries = %+v, %v", entries, err)
	}
	// Another user is not affected.
	stranger := uuid.Must(uuid.NewV7())
	w.members[grantKey{stranger, w.a}] = tenancy.RoleViewer
	if _, _, err := w.svc.Create(ctx, stranger, api(g(w.a, tenancy.RoleViewer))); err != nil {
		t.Fatalf("another user's grant in A: %v", err)
	}

	if _, err := w.svc.UnblockGrant(ctx, w.a, blocked.ID); err != nil {
		t.Fatal(err)
	}
	if entries, _ := w.svc.ListForOrg(ctx, w.a); len(entries) != 1 || entries[0].OwnerID != stranger {
		t.Fatalf("A entries after the unblock = %+v", entries)
	}
	if _, _, err := w.svc.Create(ctx, w.user, api(g(w.a, tenancy.RoleViewer))); err != nil {
		t.Fatalf("a new token after the unblock: %v", err)
	}
}

// A blocked grant still counts toward a token's 50 grants: the owner
// cannot list 50 more organizations beside it.
func TestUpdateCountsBlockedGrantsTowardLimit(t *testing.T) {
	w := newWorld()
	tok := w.create(t, api(g(w.a, tenancy.RoleViewer), g(w.b, tenancy.RoleViewer)))
	if _, err := w.svc.RevokeGrant(context.Background(), w.a, tok.ID, uuid.Nil); err != nil {
		t.Fatal(err)
	}
	set := []personaltokens.GrantInput{g(w.b, tenancy.RoleViewer)}
	for range personaltokens.MaxGrantsPerToken - 1 {
		org := uuid.Must(uuid.NewV7())
		w.members[grantKey{w.user, org}] = tenancy.RoleViewer
		set = append(set, g(org, tenancy.RoleViewer))
	}
	if _, _, _, err := w.svc.Update(context.Background(), w.user, tok.ID, personaltokens.Change{Grants: &set}); !errors.Is(err, personaltokens.ErrInvalid) {
		t.Fatalf("50 listed + 1 blocked: err = %v, want ErrInvalid", err)
	}
	set = set[:len(set)-1]
	if _, _, _, err := w.svc.Update(context.Background(), w.user, tok.ID, personaltokens.Change{Grants: &set}); err != nil {
		t.Fatalf("49 listed + 1 blocked: %v", err)
	}
}
