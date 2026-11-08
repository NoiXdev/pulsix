package repos_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/repos"
)

// TestCanonicalSubpath pins the one stored form of a repository subpath
// (backlog 7.94): the worker refuses a leading "/" and cannot open "./a"
// or "a//b", so every spelling of the same directory is stored as its
// plain clone-relative path, and the repository root as "".
func TestCanonicalSubpath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"ansible", "ansible"},
		{"deploy/ansible", "deploy/ansible"},
		{"/ansible", "ansible"},
		{"ansible/", "ansible"},
		{"/ansible/", "ansible"},
		{"//ansible//site//", "ansible/site"},
		{"./ansible", "ansible"},
		{"ansible/./site", "ansible/site"},
		{"/", ""},
		{".", ""},
		{"./", ""},
		{"/./", ""},
		{".hidden/x", ".hidden/x"},
		{"a.b/c-d_e", "a.b/c-d_e"},
	}
	for _, tc := range cases {
		got, err := repos.CanonicalSubpath(tc.in)
		if err != nil {
			t.Errorf("CanonicalSubpath(%q) = %v, want %q", tc.in, err, tc.want)
			continue
		}
		if got != tc.want {
			t.Errorf("CanonicalSubpath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCanonicalSubpathRefuses keeps the refusals the subpath always had:
// traversal, characters outside the set, and more than 200 characters
// (counted on what was sent, before any slash is dropped).
func TestCanonicalSubpathRefuses(t *testing.T) {
	for _, in := range []string{
		"..",
		"../x",
		"a/../b",
		"a/..",
		"a..b",
		"pl*ys",
		"a b",
		`a\b`,
		"/" + strings.Repeat("a", 200),
	} {
		got, err := repos.CanonicalSubpath(in)
		if !errors.Is(err, repos.ErrInvalidInput) || !strings.Contains(err.Error(), "invalid subpath") {
			t.Errorf("CanonicalSubpath(%q) = %q, %v; want ErrInvalidInput naming \"invalid subpath\"", in, got, err)
		}
	}
}

// TestCreateAndUpdateStoreTheCanonicalSubpath proves both writes store the
// canonical form, so a run's claim never carries "/ansible/site.yml".
func TestCreateAndUpdateStoreTheCanonicalSubpath(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := newService(store, newFakeCreds(nil), &fakeEnqueuer{})

	created, err := svc.Create(ctx, org, repos.CreateInput{Name: "infra", URL: "https://git.example.com/x.git", Subpath: "/ansible/"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Subpath != "ansible" {
		t.Fatalf("Create stored subpath %q, want %q", created.Subpath, "ansible")
	}
	stored, err := store.Get(ctx, org, created.ID)
	if err != nil || stored.Subpath != "ansible" {
		t.Fatalf("store holds subpath %q (%v), want %q", stored.Subpath, err, "ansible")
	}

	updated, err := svc.Update(ctx, org, created.ID, repos.UpdateInput{Name: "infra", URL: "https://git.example.com/x.git", Subpath: "./deploy//ansible"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Subpath != "deploy/ansible" {
		t.Fatalf("Update stored subpath %q, want %q", updated.Subpath, "deploy/ansible")
	}
}

// TestUpdateOfAProjectionComparesTheCanonicalSubpath proves a projection's
// own subpath sent back in another spelling is not taken for a change of
// the installation-owned field.
func TestUpdateOfAProjectionComparesTheCanonicalSubpath(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := newService(store, newFakeCreds(nil), &fakeEnqueuer{})
	sharedID := uuid.Must(uuid.NewV7())
	projected, err := store.Create(ctx, repos.Repo{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "infra",
		URL: "https://git.example.com/infra.git", Branch: "main", Subpath: "ansible",
		SharedRepoID: &sharedID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctx, org, projected.ID, repos.UpdateInput{
		Name: projected.Name, URL: projected.URL, Branch: projected.Branch, Subpath: "/ansible/",
	}); err != nil {
		t.Fatalf("Update with the projection's subpath spelled \"/ansible/\" = %v, want accepted", err)
	}
}
