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
