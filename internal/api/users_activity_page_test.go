package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type usersPageBody struct {
	Users      []adminUserBody `json:"users"`
	NextCursor string          `json:"next_cursor"`
	Total      *int            `json:"total"`
}

func getUsersPage(t *testing.T, s *apitest.Server, c *http.Client, query string) usersPageBody {
	t.Helper()
	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+adminUsersPath+"?"+query, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET users?%s = %d %s", query, resp.StatusCode, mustReadBody(t, resp))
	}
	var out usersPageBody
	decodeInto(t, resp, &out)
	return out
}

func userEmails(us []adminUserBody) []string {
	out := make([]string, len(us))
	for i, u := range us {
		out[i] = u.Email
	}
	return out
}

// seedAccount creates an account with a name and last sign-in (nil: never).
func seedAccount(t *testing.T, s *apitest.Server, email, name string, lastSignIn *time.Time) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := s.Q.CreateUser(t.Context(), gen.CreateUserParams{ID: id, Email: email, DisplayName: name}); err != nil {
		t.Fatal(err)
	}
	setLastSignIn(t, s, id, lastSignIn)
	return id
}

func setLastSignIn(t *testing.T, s *apitest.Server, id uuid.UUID, at *time.Time) {
	t.Helper()
	if _, err := s.DB.Pool().Exec(t.Context(), `UPDATE users SET last_sign_in_at = $2 WHERE id = $1`, id, at); err != nil {
		t.Fatal(err)
	}
}

// TestAdminUsersPageMode: a page of 10 out of 30 accounts answers the total,
// the search narrows it, a page past the end is 200 with no rows, and without
// page the list stays in cursor mode.
func TestAdminUsersPageMode(t *testing.T) {
	s := apitest.New(t)
	admin, c := adminWithFactor(t, s, "root@page.test")
	setLastSignIn(t, s, admin, nil)
	if _, err := s.DB.Pool().Exec(t.Context(), `UPDATE users SET display_name = 'root' WHERE id = $1`, admin); err != nil {
		t.Fatal(err)
	}
	var want []string
	for i := range 29 {
		email := fmt.Sprintf("u%02d@page.test", i)
		seedAccount(t, s, email, fmt.Sprintf("user %02d", i), nil)
		want = append(want, email)
	}
	want = append(want, "root@page.test")
	slices.Sort(want)

	var got []string
	for p := 1; p <= 3; p++ {
		page := getUsersPage(t, s, c, fmt.Sprintf("page=%d&per_page=10", p))
		if page.Total == nil || *page.Total != 30 {
			t.Fatalf("page %d total = %v, want 30", p, page.Total)
		}
		if len(page.Users) != 10 {
			t.Fatalf("page %d has %d rows", p, len(page.Users))
		}
		got = append(got, userEmails(page.Users)...)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("default order: got %v, want e-mail ascending %v", got, want)
	}

	past := getUsersPage(t, s, c, "page=4&per_page=10")
	if len(past.Users) != 0 || past.Total == nil || *past.Total != 30 {
		t.Fatalf("page past the end = %d rows, total %v, want none and 30", len(past.Users), past.Total)
	}
	far := getUsersPage(t, s, c, "page=1000000&per_page=100")
	if len(far.Users) != 0 || far.Total == nil || *far.Total != 30 {
		t.Fatalf("far page = %d rows, total %v", len(far.Users), far.Total)
	}

	narrow := getUsersPage(t, s, c, "page=1&per_page=10&q=u1")
	if narrow.Total == nil || *narrow.Total != 10 || len(narrow.Users) != 10 {
		t.Fatalf("q=u1: %d rows, total %v, want 10 and 10", len(narrow.Users), narrow.Total)
	}
	narrow = getUsersPage(t, s, c, "page=2&per_page=10&q=u1")
	if narrow.Total == nil || *narrow.Total != 10 || len(narrow.Users) != 0 {
		t.Fatalf("q=u1 page 2: %d rows, total %v", len(narrow.Users), narrow.Total)
	}

	cursor := getUsersPage(t, s, c, "limit=5")
	if cursor.Total != nil || len(cursor.Users) != 5 || cursor.NextCursor == "" {
		t.Fatalf("cursor mode: total %v, %d rows, next %q", cursor.Total, len(cursor.Users), cursor.NextCursor)
	}

	for _, bad := range []string{"page=1&sort=state", "page=1&sort=org_count", "page=1&order=up", "page=1&per_page=7", "page=1000001", "page=1&cursor=x"} {
		resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+adminUsersPath+"?"+bad, nil)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("?%s = %d, want 422", bad, resp.StatusCode)
		}
	}
}

// TestAdminUsersSortOrder asserts the exact order of every key and
// direction. Ties (equal names, equal sign-in times) fall back to the
// e-mail ascending, the accounts that never signed in come last both ways.
func TestAdminUsersSortOrder(t *testing.T) {
	s := apitest.New(t)
	admin, c := adminWithFactor(t, s, "root@sort.test")
	t1 := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Second)
	t2, t3 := t1.Add(24*time.Hour), t1.Add(48*time.Hour)
	if _, err := s.DB.Pool().Exec(t.Context(), `UPDATE users SET display_name = 'Zoe', last_sign_in_at = $2 WHERE id = $1`, admin, t2); err != nil {
		t.Fatal(err)
	}
	seedAccount(t, s, "a@sort.test", "carol", &t3)
	seedAccount(t, s, "b@sort.test", "Alice", nil)
	seedAccount(t, s, "c@sort.test", "carol", &t1)
	seedAccount(t, s, "d@sort.test", "bob", &t3)
	seedAccount(t, s, "e@sort.test", "alice", nil)

	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"sort=email", []string{"a", "b", "c", "d", "e", "root"}},
		{"sort=email&order=asc", []string{"a", "b", "c", "d", "e", "root"}},
		{"sort=email&order=desc", []string{"root", "e", "d", "c", "b", "a"}},
		{"", []string{"a", "b", "c", "d", "e", "root"}},
		{"order=desc", []string{"root", "e", "d", "c", "b", "a"}},
		{"sort=name", []string{"b", "e", "d", "a", "c", "root"}},
		{"sort=name&order=desc", []string{"root", "a", "c", "d", "b", "e"}},
		{"sort=last_sign_in", []string{"c", "root", "a", "d", "b", "e"}},
		{"sort=last_sign_in&order=asc", []string{"c", "root", "a", "d", "b", "e"}},
		{"sort=last_sign_in&order=desc", []string{"a", "d", "root", "c", "b", "e"}},
	} {
		page := getUsersPage(t, s, c, "page=1&per_page=25&"+tc.query)
		var got []string
		for _, u := range page.Users {
			got = append(got, u.Email[:len(u.Email)-len("@sort.test")])
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("?%s: got %v, want %v", tc.query, got, tc.want)
		}
		if page.Total == nil || *page.Total != 6 {
			t.Errorf("?%s: total %v, want 6", tc.query, page.Total)
		}
	}
}

// TestAdminUsersSortLastSignInNullsLast pages through 30 accounts, half of
// which never signed in: in both directions the pages join up, the
// never-signed-in accounts are at the end and the others follow the order.
func TestAdminUsersSortLastSignInNullsLast(t *testing.T) {
	s := apitest.New(t)
	admin, c := adminWithFactor(t, s, "root@nulls.test")
	setLastSignIn(t, s, admin, nil)
	base := time.Now().UTC().Add(-100 * time.Hour).Truncate(time.Second)
	type row struct {
		email string
		at    *time.Time
	}
	rows := []row{{"root@nulls.test", nil}}
	for i := range 29 {
		var at *time.Time
		if i%2 == 0 {
			v := base.Add(time.Duration(i) * time.Hour)
			at = &v
		}
		email := fmt.Sprintf("n%02d@nulls.test", i)
		seedAccount(t, s, email, "x", at)
		rows = append(rows, row{email, at})
	}
	expect := func(desc bool) []string {
		slices.SortFunc(rows, func(a, b row) int {
			switch {
			case a.at == nil && b.at == nil:
				return compareStrings(a.email, b.email)
			case a.at == nil:
				return 1
			case b.at == nil:
				return -1
			case a.at.Equal(*b.at):
				return compareStrings(a.email, b.email)
			case a.at.Before(*b.at) != desc:
				return -1
			default:
				return 1
			}
		})
		out := make([]string, len(rows))
		for i, r := range rows {
			out[i] = r.email
		}
		return out
	}
	for _, order := range []string{"asc", "desc"} {
		want := expect(order == "desc")
		var got []string
		for p := 1; p <= 3; p++ {
			page := getUsersPage(t, s, c, fmt.Sprintf("page=%d&per_page=10&sort=last_sign_in&order=%s", p, order))
			got = append(got, userEmails(page.Users)...)
		}
		if !slices.Equal(got, want) {
			t.Errorf("order=%s: got %v\nwant %v", order, got, want)
		}
	}
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// TestAdminUsersSortName: names order case-insensitively, descending too.
func TestAdminUsersSortName(t *testing.T) {
	s := apitest.New(t)
	admin, c := adminWithFactor(t, s, "root@names.test")
	if _, err := s.DB.Pool().Exec(t.Context(), `UPDATE users SET display_name = 'mallory' WHERE id = $1`, admin); err != nil {
		t.Fatal(err)
	}
	seedAccount(t, s, "x1@names.test", "Zed", nil)
	seedAccount(t, s, "x2@names.test", "adam", nil)
	seedAccount(t, s, "x3@names.test", "Bea", nil)
	asc := userEmails(getUsersPage(t, s, c, "page=1&sort=name").Users)
	if want := []string{"x2@names.test", "x3@names.test", "root@names.test", "x1@names.test"}; !slices.Equal(asc, want) {
		t.Fatalf("name asc = %v, want %v", asc, want)
	}
	desc := userEmails(getUsersPage(t, s, c, "page=1&sort=name&order=desc").Users)
	if want := []string{"x1@names.test", "root@names.test", "x3@names.test", "x2@names.test"}; !slices.Equal(desc, want) {
		t.Fatalf("name desc = %v, want %v", desc, want)
	}
}

// --- activity ---

type activityPageBody struct {
	Items []struct {
		ID        string `json:"id"`
		At        string `json:"at"`
		TokenName string `json:"token_name"`
	} `json:"items"`
	NextCursor string `json:"next_cursor"`
	Total      *int   `json:"total"`
}

func (b activityPageBody) ids() []string {
	out := make([]string, len(b.Items))
	for i, it := range b.Items {
		out[i] = it.ID
	}
	return out
}

func getActivityPage(t *testing.T, s *apitest.Server, c *http.Client, path, query string) activityPageBody {
	t.Helper()
	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+path+"?"+query, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s?%s = %d %s", path, query, resp.StatusCode, mustReadBody(t, resp))
	}
	var out activityPageBody
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// seedEvent inserts an audit event; ordinal n gives it an id that sorts by n.
func seedEvent(t *testing.T, s *apitest.Server, n int, org, actor uuid.UUID, objectType string, at time.Time) string {
	t.Helper()
	id := uuid.MustParse(fmt.Sprintf("01900000-0000-7000-8000-%012d", n))
	if _, err := s.DB.Pool().Exec(t.Context(), `
INSERT INTO audit_events (id, org_id, actor_id, actor_kind, action, object_type, object_id, summary, meta, at)
VALUES ($1, $2, $3, 'user', 'POST seed', $4, 'o', 'seed', '{}', $5)`, id, org, actor, objectType, at); err != nil {
		t.Fatal(err)
	}
	return id.String()
}

// activityFixture seeds 25 events of one organization, two to a timestamp
// (ties on at, ordered by id): the owner made the even ones, a member the
// odd; object types alternate host / user.
func activityFixture(t *testing.T) (s *apitest.Server, c *http.Client, owner, member, org uuid.UUID, ids []string) {
	t.Helper()
	s = apitest.New(t)
	owner = s.SeedUser("act-owner@example.test", "correct horse battery")
	org = s.SeedOrg("act-org", owner)
	member = s.SeedUser("act-member@example.test", "correct horse battery")
	s.AddMember(org, member, tenancy.RoleAdmin)
	c = s.LoginAs(owner)
	base := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	for i := range 25 {
		actor, ot := owner, "host"
		if i%2 == 1 {
			actor, ot = member, "user"
		}
		ids = append(ids, seedEvent(t, s, i, org, actor, ot, base.Add(time.Duration(i/2)*time.Minute)))
	}
	return s, c, owner, member, org, ids
}

func reversed(in []string) []string {
	out := slices.Clone(in)
	slices.Reverse(out)
	return out
}

func TestOrgActivityPageMode(t *testing.T) {
	s, c, _, member, _, ids := activityFixture(t)
	const path = "/api/v1/orgs/act-org/activity"
	newest := reversed(ids)

	var got []string
	for p := 1; p <= 3; p++ {
		page := getActivityPage(t, s, c, path, fmt.Sprintf("page=%d&per_page=10", p))
		if page.Total == nil || *page.Total != 25 {
			t.Fatalf("page %d total = %v, want 25", p, page.Total)
		}
		got = append(got, page.ids()...)
	}
	if !slices.Equal(got, newest) {
		t.Fatalf("default order = %v, want at and id descending %v", got, newest)
	}

	for _, q := range []string{"sort=at&order=asc", "order=asc"} {
		got = nil
		for p := 1; p <= 3; p++ {
			got = append(got, getActivityPage(t, s, c, path, fmt.Sprintf("page=%d&per_page=10&%s", p, q)).ids()...)
		}
		if !slices.Equal(got, ids) {
			t.Fatalf("?%s = %v, want the oldest first %v", q, got, ids)
		}
	}
	if got := getActivityPage(t, s, c, path, "page=1&per_page=10&sort=at&order=desc").ids(); !slices.Equal(got, newest[:10]) {
		t.Fatalf("sort=at&order=desc = %v", got)
	}

	// Filters apply to the rows and to the total.
	host := getActivityPage(t, s, c, path, "page=2&per_page=10&object_type=host")
	if host.Total == nil || *host.Total != 13 || len(host.Items) != 3 {
		t.Fatalf("object_type=host page 2: %d rows, total %v, want 3 and 13", len(host.Items), host.Total)
	}
	by := getActivityPage(t, s, c, path, "page=1&per_page=25&order=asc&actor="+member.String())
	var wantOdd []string
	for i := 1; i < 25; i += 2 {
		wantOdd = append(wantOdd, ids[i])
	}
	if by.Total == nil || *by.Total != 12 || !slices.Equal(by.ids(), wantOdd) {
		t.Fatalf("actor filter: total %v rows %v, want 12 and %v", by.Total, by.ids(), wantOdd)
	}
	since := getActivityPage(t, s, c, path, "page=1&per_page=10&since=2099-01-01T00:00:00Z")
	if since.Total == nil || *since.Total != 0 || len(since.Items) != 0 {
		t.Fatalf("since far future: %d rows, total %v", len(since.Items), since.Total)
	}

	past := getActivityPage(t, s, c, path, "page=4&per_page=10")
	if len(past.Items) != 0 || past.Total == nil || *past.Total != 25 {
		t.Fatalf("page past the end: %d rows, total %v", len(past.Items), past.Total)
	}

	cursor := getActivityPage(t, s, c, path, "limit=5")
	if cursor.Total != nil || len(cursor.Items) != 5 || cursor.NextCursor == "" || !slices.Equal(cursor.ids(), newest[:5]) {
		t.Fatalf("cursor mode: total %v, rows %v, next %q", cursor.Total, cursor.ids(), cursor.NextCursor)
	}

	for _, bad := range []string{"page=1&sort=actor", "page=1&order=up", "page=1&per_page=7", "page=1&cursor=x"} {
		resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+path+"?"+bad, nil)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("?%s = %d, want 422", bad, resp.StatusCode)
		}
	}
}

// TestMyActivityPageMode: only the caller's own events, across organizations.
func TestMyActivityPageMode(t *testing.T) {
	s, c, owner, _, orgA, ids := activityFixture(t)
	orgB := s.SeedOrg("act-org-b", owner)
	base := time.Now().UTC().Add(-12 * time.Hour).Truncate(time.Second)
	var mine []string
	for i := range ids { // the owner's events of org A: the even ordinals
		if i%2 == 0 {
			mine = append(mine, ids[i])
		}
	}
	for i := range 3 { // and three of org B, all at one instant
		mine = append(mine, seedEvent(t, s, 100+i, orgB, owner, "host", base))
	}
	_ = orgA
	const path = "/api/v1/me/activity"
	newest := reversed(mine)

	var got []string
	for p := 1; p <= 2; p++ {
		page := getActivityPage(t, s, c, path, fmt.Sprintf("page=%d&per_page=10", p))
		if page.Total == nil || *page.Total != 16 {
			t.Fatalf("page %d total = %v, want 16", p, page.Total)
		}
		got = append(got, page.ids()...)
	}
	if !slices.Equal(got, newest) {
		t.Fatalf("default order = %v, want %v", got, newest)
	}
	asc := getActivityPage(t, s, c, path, "page=1&per_page=25&order=asc")
	if !slices.Equal(asc.ids(), mine) {
		t.Fatalf("order=asc = %v, want %v", asc.ids(), mine)
	}
	ot := getActivityPage(t, s, c, path, "page=1&per_page=10&object_type=user")
	if ot.Total == nil || *ot.Total != 0 || len(ot.Items) != 0 {
		t.Fatalf("object_type=user: %d rows, total %v, want none (the member made those)", len(ot.Items), ot.Total)
	}
	past := getActivityPage(t, s, c, path, "page=3&per_page=10")
	if len(past.Items) != 0 || past.Total == nil || *past.Total != 16 {
		t.Fatalf("page past the end: %d rows, total %v", len(past.Items), past.Total)
	}
	cursor := getActivityPage(t, s, c, path, "limit=5")
	if cursor.Total != nil || len(cursor.Items) != 5 || cursor.NextCursor == "" {
		t.Fatalf("cursor mode: total %v, %d rows, next %q", cursor.Total, len(cursor.Items), cursor.NextCursor)
	}
	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+path+"?page=1&sort=actor", nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("sort=actor = %d, want 422", resp.StatusCode)
	}
}

// TestActivityPageTokenName: a page row made with an API token names it.
func TestActivityPageTokenName(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	base := "/api/v1/orgs/" + org.String()
	if resp := s.Post(t, tok, base+"/inventory/groups", map[string]any{"name": "web"}); resp.Code != http.StatusCreated && resp.Code != http.StatusOK {
		t.Fatalf("create group = %d %s", resp.Code, resp.Body)
	}
	resp := s.Get(t, tok, base+"/activity?page=1")
	if resp.Code != http.StatusOK {
		t.Fatalf("activity = %d %s", resp.Code, resp.Body)
	}
	var out activityPageBody
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Total == nil || *out.Total == 0 || len(out.Items) == 0 || out.Items[0].TokenName != "ci" {
		t.Fatalf("activity page = %+v, want the token's name", out)
	}
}
