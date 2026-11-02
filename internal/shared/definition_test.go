package shared_test

import (
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/shared"
)

func TestCollidingOrgs(t *testing.T) {
	orgA, orgB, orgC := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	tests := []struct {
		name     string
		existing []shared.OrgName
		want     string
		wantOrgs []uuid.UUID
	}{
		{
			name: "no organization uses the name",
			existing: []shared.OrgName{
				{OrgID: orgA, Name: "other-repo"},
				{OrgID: orgB, Name: "another-repo"},
			},
			want: "ansible-base",
		},
		{
			name: "one organization already has the name",
			existing: []shared.OrgName{
				{OrgID: orgA, Name: "ansible-base"},
			},
			want:     "ansible-base",
			wantOrgs: []uuid.UUID{orgA},
		},
		{
			name: "several organizations already have the name, one of them twice",
			existing: []shared.OrgName{
				{OrgID: orgA, Name: "ansible-base"},
				{OrgID: orgA, Name: "other-repo"}, // orgA's second, unrelated repo
				{OrgID: orgB, Name: "ansible-base"},
				{OrgID: orgC, Name: "totally-different"},
			},
			want:     "ansible-base",
			wantOrgs: []uuid.UUID{orgA, orgB},
		},
		{
			name:     "no existing rows at all",
			existing: nil,
			want:     "ansible-base",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shared.CollidingOrgs(tt.existing, tt.want)
			want := tt.wantOrgs
			sort.Slice(want, func(i, j int) bool { return want[i].String() < want[j].String() })
			// A nil and an empty slice both mean "no collisions"; only the
			// elements matter here, not which of the two CollidingOrgs
			// happens to return for the empty case.
			if len(got) != len(want) {
				t.Fatalf("CollidingOrgs(%q) = %v, want %v", tt.want, got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("CollidingOrgs(%q) = %v, want %v", tt.want, got, want)
				}
			}
		})
	}
}

func TestCollidingOrgsIsSortedForAStableResult(t *testing.T) {
	// Deliberately inserted out of id order, so a correct implementation
	// cannot pass by coincidentally preserving input order.
	low, high := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if low.String() > high.String() {
		low, high = high, low
	}
	existing := []shared.OrgName{
		{OrgID: high, Name: "ansible-base"},
		{OrgID: low, Name: "ansible-base"},
	}
	got := shared.CollidingOrgs(existing, "ansible-base")
	want := []uuid.UUID{low, high}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CollidingOrgs() = %v, want sorted %v", got, want)
	}
}

func TestProjectedRepoColumns(t *testing.T) {
	tests := []struct {
		name string
		def  shared.RepoDefinition
	}{
		{
			name: "every field populated",
			def: shared.RepoDefinition{
				ID: uuid.Must(uuid.NewV7()), Name: "infra", URL: "https://git.example.com/infra.git",
				Branch: "main", Subpath: "ansible",
			},
		},
		{
			name: "empty subpath, the common case",
			def: shared.RepoDefinition{
				ID: uuid.Must(uuid.NewV7()), Name: "infra", URL: "ssh://git@example.com/infra.git",
				Branch: "develop", Subpath: "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, url, branch, subpath := shared.ProjectedRepoColumns(tt.def)
			if name != tt.def.Name || url != tt.def.URL || branch != tt.def.Branch || subpath != tt.def.Subpath {
				t.Fatalf("ProjectedRepoColumns(%+v) = (%q,%q,%q,%q), want the definition's own values",
					tt.def, name, url, branch, subpath)
			}
		})
	}
}

func TestNameCollisionErrorMatchesErrNameTaken(t *testing.T) {
	err := &shared.NameCollisionError{Name: "ansible-base", Orgs: []uuid.UUID{uuid.Must(uuid.NewV7())}}
	if !errors.Is(err, shared.ErrNameTaken) {
		t.Fatal("errors.Is(NameCollisionError, ErrNameTaken) = false, want true")
	}
}

// TestNameCollisionErrorMessage pins the exact Error() text — never
// composed in a test until now, even though mapSharedErr
// (internal/api/shared_handlers.go) never calls it directly either (it
// re-derives a sentence from Name/Orgs instead); this exists purely so a
// future reader changing the format sees a failing test, not a silent
// drift from what this comment documents.
func TestNameCollisionErrorMessage(t *testing.T) {
	err := &shared.NameCollisionError{Name: "ansible-base", Orgs: []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}}
	want := `name "ansible-base" is already used by 2 organization(s)`
	if got := err.Error(); got != want {
		t.Fatalf("NameCollisionError.Error() = %q, want %q", got, want)
	}
}

// TestNameCollisionErrorMessageNamesTheScope: a check definition's
// collision can sit at a group's or a host's binding, and the message says
// where (see NameCollisionError's own comment).
func TestNameCollisionErrorMessageNamesTheScope(t *testing.T) {
	err := &shared.NameCollisionError{Name: "disk", Orgs: []uuid.UUID{uuid.Must(uuid.NewV7())}, Scope: "a host's own checks"}
	want := `name "disk" is already used by 1 organization(s), at a host's own checks`
	if got := err.Error(); got != want {
		t.Fatalf("NameCollisionError.Error() = %q, want %q", got, want)
	}
}

// TestInUseErrorMessageAndUnwrap is NameCollisionError's own pair of tests
// for the other richer sentinel (spec decision 5): both of Error()'s
// branches (a named reason, and the backstop with none at all — a
// foreign-key violation caught with nothing explicit to name, InUseError's
// own doc comment) and that errors.Is still matches ErrInUse through
// Unwrap.
func TestInUseErrorMessageAndUnwrap(t *testing.T) {
	org := uuid.Must(uuid.NewV7())

	named := &shared.InUseError{OrgID: org, Reasons: []string{"a job trigger", "a playbook"}}
	wantNamed := "organization " + org.String() + ": a job trigger, a playbook still reference this definition's projection"
	if got := named.Error(); got != wantNamed {
		t.Fatalf("InUseError.Error() with reasons = %q, want %q", got, wantNamed)
	}
	if !errors.Is(named, shared.ErrInUse) {
		t.Fatal("errors.Is(InUseError, ErrInUse) = false, want true")
	}

	bare := &shared.InUseError{OrgID: org}
	wantBare := "organization " + org.String() + " still has a dependant referencing this definition's projection"
	if got := bare.Error(); got != wantBare {
		t.Fatalf("InUseError.Error() with no reasons = %q, want %q", got, wantBare)
	}
}
