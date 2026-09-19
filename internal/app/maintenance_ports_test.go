package app

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/jobs"
)

type fakeScopeResolver struct {
	targets []jobs.ZoneTargets
	err     error
	scope   render.Scope
}

func (f *fakeScopeResolver) Resolve(_ context.Context, _ uuid.UUID, scope render.Scope) ([]jobs.ZoneTargets, error) {
	f.scope = scope
	return f.targets, f.err
}

// TestGroupHosts covers HostsInGroup's translation of the resolver's three
// outcomes for a group scope: hosts, a normal empty selection (an empty
// group, or one whose only members are unmanaged — jobs.ErrEmptyScope), a
// group that no longer exists (jobs.ErrNotFound), and any other resolver
// failure, which must still surface rather than being swallowed like the
// two expected-empty cases.
func TestGroupHosts(t *testing.T) {
	a, b, group := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	dbErr := errors.New("db down")

	cases := []struct {
		name    string
		targets []jobs.ZoneTargets
		err     error
		want    []uuid.UUID
		wantErr error
	}{
		{
			name:    "group with hosts",
			targets: []jobs.ZoneTargets{{HostIDs: []uuid.UUID{a}}, {HostIDs: []uuid.UUID{b}}},
			want:    []uuid.UUID{a, b},
		},
		{name: "empty group", err: jobs.ErrEmptyScope, want: nil},
		{name: "missing group", err: jobs.ErrNotFound, want: nil},
		{name: "other error propagates", err: dbErr, wantErr: dbErr},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeScopeResolver{targets: tc.targets, err: tc.err}
			got, err := NewGroupHosts(r).HostsInGroup(context.Background(), uuid.Must(uuid.NewV7()), group)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got = %v, want %v", got, tc.want)
			}
			if r.scope.GroupID == nil || *r.scope.GroupID != group {
				t.Fatalf("scope = %+v, want group %v", r.scope, group)
			}
		})
	}
}
