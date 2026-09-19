package app

import (
	"context"
	"errors"
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

func TestGroupHosts(t *testing.T) {
	a, b, group := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	r := &fakeScopeResolver{targets: []jobs.ZoneTargets{{HostIDs: []uuid.UUID{a}}, {HostIDs: []uuid.UUID{b}}}}
	got, err := NewGroupHosts(r).HostsInGroup(context.Background(), uuid.Must(uuid.NewV7()), group)
	if err != nil || len(got) != 2 || r.scope.GroupID == nil || *r.scope.GroupID != group {
		t.Fatalf("HostsInGroup = %v, %v (scope %+v)", got, err, r.scope)
	}
	r.err = jobs.ErrNotFound
	if got, err := NewGroupHosts(r).HostsInGroup(context.Background(), uuid.Must(uuid.NewV7()), group); err != nil || got != nil {
		t.Fatalf("deleted group = %v, %v; want nothing and no error", got, err)
	}
	r.err = errors.New("db down")
	if _, err := NewGroupHosts(r).HostsInGroup(context.Background(), uuid.Must(uuid.NewV7()), group); err == nil {
		t.Fatal("a resolver failure must surface")
	}
}
