package monitorsettings_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/monitorsettings"
)

var ctx = context.Background()

// fakeStore keeps one Settings per organization in memory.
type fakeStore struct {
	rows    map[uuid.UUID]monitorsettings.Settings
	getErr  error
	putErr  error
	putRuns int
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[uuid.UUID]monitorsettings.Settings{}}
}

func (f *fakeStore) Get(_ context.Context, orgID uuid.UUID) (monitorsettings.Settings, bool, error) {
	if f.getErr != nil {
		return monitorsettings.Settings{}, false, f.getErr
	}
	st, ok := f.rows[orgID]
	return st, ok, nil
}

func (f *fakeStore) Put(_ context.Context, orgID uuid.UUID, st monitorsettings.Settings) error {
	f.putRuns++
	if f.putErr != nil {
		return f.putErr
	}
	f.rows[orgID] = st
	return nil
}

func TestGetDefaultsWithoutARow(t *testing.T) {
	svc := monitorsettings.NewService(newFakeStore())
	st, err := svc.Get(ctx, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatal(err)
	}
	if st.ClientAbsentDays != monitorsettings.DefaultClientAbsentDays || monitorsettings.DefaultClientAbsentDays != 14 {
		t.Fatalf("default = %+v, want client_absent_days 14", st)
	}
}

func TestSetValidatesClientAbsentDays(t *testing.T) {
	cases := []struct {
		days int
		ok   bool
	}{
		{0, true}, {1, true}, {14, true}, {365, true},
		{-1, false}, {366, false}, {10000, false},
	}
	for _, c := range cases {
		st := newFakeStore()
		svc := monitorsettings.NewService(st)
		org := uuid.Must(uuid.NewV7())
		got, err := svc.Set(ctx, org, monitorsettings.Settings{ClientAbsentDays: c.days})
		if c.ok {
			if err != nil || got.ClientAbsentDays != c.days {
				t.Errorf("Set(%d) = %+v, %v; want stored", c.days, got, err)
			}
			if after, _ := svc.Get(ctx, org); after.ClientAbsentDays != c.days {
				t.Errorf("Get after Set(%d) = %+v", c.days, after)
			}
			continue
		}
		if !errors.Is(err, monitorsettings.ErrInvalidInput) {
			t.Errorf("Set(%d) = %v, want ErrInvalidInput", c.days, err)
		}
		if st.putRuns != 0 {
			t.Errorf("Set(%d) wrote to the store, want nothing written", c.days)
		}
	}
}

func TestStoreFailuresPropagate(t *testing.T) {
	boom := errors.New("boom")
	st := newFakeStore()
	st.getErr = boom
	svc := monitorsettings.NewService(st)
	if _, err := svc.Get(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, boom) {
		t.Fatalf("Get = %v, want the store error", err)
	}
	st = newFakeStore()
	st.putErr = boom
	svc = monitorsettings.NewService(st)
	if _, err := svc.Set(ctx, uuid.Must(uuid.NewV7()), monitorsettings.Settings{ClientAbsentDays: 3}); !errors.Is(err, boom) {
		t.Fatalf("Set = %v, want the store error", err)
	}
}
