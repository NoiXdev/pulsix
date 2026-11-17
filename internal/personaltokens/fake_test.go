package personaltokens_test

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/personaltokens"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type grantKey struct{ token, org uuid.UUID }

type grantRow struct {
	role     tenancy.Role
	blocked  bool
	lastUsed *time.Time
}

type orgInfo struct{ slug, name string }

type fakeToken struct {
	tok     auth.PersonalToken
	revoked bool
}

// fakeStore is the in-memory personaltokens.Store. The grant and switch
// maps are what the read side (Cap) uses; tokens, orgs and users back the
// management side. It keeps no transaction of its own: InTx only counts.
type fakeStore struct {
	grants   map[grantKey]grantRow
	off      map[uuid.UUID]bool
	touched  []grantKey
	capErr   error
	allowErr error
	touchErr error

	tokens  map[uuid.UUID]*fakeToken
	order   []uuid.UUID // token insertion order
	orgs    map[uuid.UUID]orgInfo
	inTx    int
	failErr error // returned by every management write when set
	hashes  [][]byte
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		grants: map[grantKey]grantRow{}, off: map[uuid.UUID]bool{},
		tokens: map[uuid.UUID]*fakeToken{}, orgs: map[uuid.UUID]orgInfo{},
	}
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

func (f *fakeStore) InTx(ctx context.Context, fn func(context.Context) error) error {
	f.inTx++
	return fn(ctx)
}

func (f *fakeStore) CountTokens(_ context.Context, userID uuid.UUID) (int, error) {
	if f.failErr != nil {
		return 0, f.failErr
	}
	n := 0
	for _, t := range f.tokens {
		if t.tok.UserID == userID && !t.revoked {
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) InsertToken(_ context.Context, t auth.PersonalToken, hash []byte) (auth.PersonalToken, error) {
	if f.failErr != nil {
		return auth.PersonalToken{}, f.failErr
	}
	t.CreatedAt = t.CreatedAt.Truncate(time.Microsecond)
	if t.ExpiresAt != nil {
		v := t.ExpiresAt.Truncate(time.Microsecond)
		t.ExpiresAt = &v
	}
	f.tokens[t.ID] = &fakeToken{tok: t}
	f.order = append(f.order, t.ID)
	f.hashes = append(f.hashes, hash)
	return t, nil
}

func (f *fakeStore) InsertGrant(_ context.Context, tokenID, _, orgID uuid.UUID, role tenancy.Role) error {
	if f.failErr != nil {
		return f.failErr
	}
	f.grants[grantKey{tokenID, orgID}] = grantRow{role: role}
	return nil
}

func (f *fakeStore) live(userID, id uuid.UUID) (*fakeToken, bool) {
	t, ok := f.tokens[id]
	if !ok || t.revoked || t.tok.UserID != userID {
		return nil, false
	}
	return t, true
}

func (f *fakeStore) Token(_ context.Context, userID, id uuid.UUID) (auth.PersonalToken, error) {
	t, ok := f.live(userID, id)
	if !ok {
		return auth.PersonalToken{}, personaltokens.ErrNotFound
	}
	return t.tok, nil
}

func (f *fakeStore) Tokens(_ context.Context, userID uuid.UUID) ([]auth.PersonalToken, error) {
	var out []auth.PersonalToken
	for _, id := range f.order {
		if t, ok := f.live(userID, id); ok {
			out = append(out, t.tok)
		}
	}
	return out, nil
}

func (f *fakeStore) stored(k grantKey, g grantRow) personaltokens.StoredGrant {
	o := f.orgs[k.org]
	return personaltokens.StoredGrant{TokenID: k.token, OrgID: k.org, OrgSlug: o.slug, OrgName: o.name,
		Role: g.role, Blocked: g.blocked, Allowed: !f.off[k.org], LastUsedAt: g.lastUsed}
}

func (f *fakeStore) sortedGrants(keep func(grantKey) bool) []personaltokens.StoredGrant {
	var out []personaltokens.StoredGrant
	for k, g := range f.grants {
		if keep(k) {
			out = append(out, f.stored(k, g))
		}
	}
	slices.SortFunc(out, func(a, b personaltokens.StoredGrant) int {
		if a.OrgSlug < b.OrgSlug {
			return -1
		}
		if a.OrgSlug > b.OrgSlug {
			return 1
		}
		return 0
	})
	return out
}

func (f *fakeStore) UserGrants(_ context.Context, userID uuid.UUID) ([]personaltokens.StoredGrant, error) {
	return f.sortedGrants(func(k grantKey) bool {
		t, ok := f.tokens[k.token]
		return ok && t.tok.UserID == userID
	}), nil
}

func (f *fakeStore) TokenGrants(_ context.Context, tokenID uuid.UUID) ([]personaltokens.StoredGrant, error) {
	return f.sortedGrants(func(k grantKey) bool { return k.token == tokenID }), nil
}

func (f *fakeStore) UpdateToken(_ context.Context, t auth.PersonalToken) (auth.PersonalToken, error) {
	if f.failErr != nil {
		return auth.PersonalToken{}, f.failErr
	}
	cur, ok := f.live(t.UserID, t.ID)
	if !ok {
		return auth.PersonalToken{}, personaltokens.ErrNotFound
	}
	cur.tok.Name, cur.tok.Areas = t.Name, t.Areas
	cur.tok.ExpiresAt = nil
	if t.ExpiresAt != nil {
		v := t.ExpiresAt.Truncate(time.Microsecond)
		cur.tok.ExpiresAt = &v
	}
	return cur.tok, nil
}

func (f *fakeStore) SetGrantRole(_ context.Context, tokenID, orgID uuid.UUID, role tenancy.Role) error {
	if f.failErr != nil {
		return f.failErr
	}
	k := grantKey{tokenID, orgID}
	g, ok := f.grants[k]
	if !ok || g.blocked {
		return personaltokens.ErrNotFound
	}
	g.role = role
	f.grants[k] = g
	return nil
}

func (f *fakeStore) DeleteGrant(_ context.Context, tokenID, orgID uuid.UUID) error {
	if f.failErr != nil {
		return f.failErr
	}
	k := grantKey{tokenID, orgID}
	if g, ok := f.grants[k]; !ok || g.blocked {
		return personaltokens.ErrNotFound
	}
	delete(f.grants, k)
	return nil
}

func (f *fakeStore) RevokeToken(_ context.Context, userID, id uuid.UUID) error {
	if f.failErr != nil {
		return f.failErr
	}
	t, ok := f.live(userID, id)
	if !ok {
		return personaltokens.ErrNotFound
	}
	t.revoked = true
	return nil
}

func (f *fakeStore) entry(orgID, tokenID uuid.UUID) (personaltokens.OrgEntry, bool) {
	g, ok := f.grants[grantKey{tokenID, orgID}]
	t, live := f.tokens[tokenID]
	if !ok || !live || t.revoked {
		return personaltokens.OrgEntry{}, false
	}
	return personaltokens.OrgEntry{TokenID: tokenID, Name: t.tok.Name, Kind: t.tok.Kind, OwnerID: t.tok.UserID,
		Role: g.role, Blocked: g.blocked, LastUsedAt: g.lastUsed, CreatedAt: t.tok.CreatedAt, ExpiresAt: t.tok.ExpiresAt}, true
}

func (f *fakeStore) OrgEntries(_ context.Context, orgID uuid.UUID) ([]personaltokens.OrgEntry, error) {
	var out []personaltokens.OrgEntry
	for _, id := range f.order {
		if e, ok := f.entry(orgID, id); ok {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeStore) OrgEntry(_ context.Context, orgID, tokenID uuid.UUID) (personaltokens.OrgEntry, error) {
	e, ok := f.entry(orgID, tokenID)
	if !ok {
		return personaltokens.OrgEntry{}, personaltokens.ErrNotFound
	}
	return e, nil
}

func (f *fakeStore) BlockGrant(_ context.Context, tokenID, orgID, _ uuid.UUID) error {
	if f.failErr != nil {
		return f.failErr
	}
	k := grantKey{tokenID, orgID}
	g, ok := f.grants[k]
	if !ok || g.blocked {
		return personaltokens.ErrNotFound
	}
	g.blocked = true
	f.grants[k] = g
	return nil
}

func (f *fakeStore) UnblockGrant(_ context.Context, tokenID, orgID uuid.UUID) error {
	if f.failErr != nil {
		return f.failErr
	}
	k := grantKey{tokenID, orgID}
	if g, ok := f.grants[k]; !ok || !g.blocked {
		return personaltokens.ErrNotFound
	}
	delete(f.grants, k)
	return nil
}

func (f *fakeStore) SetAllowed(_ context.Context, orgID uuid.UUID, on bool) error {
	if f.failErr != nil {
		return f.failErr
	}
	f.off[orgID] = !on
	return nil
}

func (f *fakeStore) LiveGrants(_ context.Context, tokenID uuid.UUID) (uuid.UUID, int, error) {
	if f.failErr != nil {
		return uuid.Nil, 0, f.failErr
	}
	var org uuid.UUID
	n := 0
	for k, g := range f.grants {
		if k.token == tokenID && !g.blocked {
			org = k.org
			n++
		}
	}
	if n != 1 {
		org = uuid.Nil
	}
	return org, n, nil
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

// fixedNow is the tests' clock: nanoseconds included, as on Linux.
var fixedNow = time.Date(2026, 11, 17, 9, 30, 0, 123456789, time.UTC)

func newService(st *fakeStore, members fakeMembers) *personaltokens.Service {
	return personaltokens.NewService(st, members, func() time.Time { return fixedNow })
}
