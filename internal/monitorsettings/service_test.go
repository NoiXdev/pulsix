package monitorsettings_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/monitorsettings"
)

var ctx = context.Background()

// fakeStore keeps one Settings per organization in memory and applies an
// Update the way the SQL upsert does: client_absent_days always, the
// retention choice only when the update carries one.
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

func (f *fakeStore) Put(_ context.Context, orgID uuid.UUID, u monitorsettings.Update) (monitorsettings.Stored, error) {
	f.putRuns++
	if f.putErr != nil {
		return monitorsettings.Stored{}, f.putErr
	}
	st := f.rows[orgID]
	prev := st.RetentionDays
	st.ClientAbsentDays = u.ClientAbsentDays
	if u.RetentionDays != nil {
		st.RetentionDays = *u.RetentionDays
	}
	f.rows[orgID] = st
	return monitorsettings.Stored{Settings: st, PreviousRetentionDays: prev}, nil
}

func intp(n int) *int { return &n }

func TestGetDefaultsWithoutARow(t *testing.T) {
	svc := monitorsettings.NewService(newFakeStore(), 90)
	st, err := svc.Get(ctx, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatal(err)
	}
	if st.ClientAbsentDays != monitorsettings.DefaultClientAbsentDays || monitorsettings.DefaultClientAbsentDays != 14 {
		t.Fatalf("default = %+v, want client_absent_days 14", st)
	}
	if st.RetentionDays != 0 || st.RetentionMaxDays != 90 || st.RetentionEffectiveDays != 90 {
		t.Fatalf("default retention = %+v, want no choice, max 90, effective 90", st)
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
		svc := monitorsettings.NewService(st, 90)
		org := uuid.Must(uuid.NewV7())
		got, err := svc.Set(ctx, org, monitorsettings.Update{ClientAbsentDays: c.days})
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
	svc := monitorsettings.NewService(st, 90)
	if _, err := svc.Get(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, boom) {
		t.Fatalf("Get = %v, want the store error", err)
	}
	st = newFakeStore()
	st.putErr = boom
	svc = monitorsettings.NewService(st, 90)
	if _, err := svc.Set(ctx, uuid.Must(uuid.NewV7()), monitorsettings.Update{ClientAbsentDays: 3}); !errors.Is(err, boom) {
		t.Fatalf("Set = %v, want the store error", err)
	}
}

func TestEffectiveRetentionDays(t *testing.T) {
	cases := []struct{ choice, max, want int }{
		{0, 90, 90},   // no choice: the instance maximum
		{0, 7, 7},     // no choice under a low maximum
		{30, 90, 30},  // a shorter choice wins
		{90, 90, 90},  // equal
		{365, 90, 90}, // never above the maximum
		{90, 30, 30},  // a lowered maximum caps a stored choice
		{30, 365, 30}, // a long maximum, a short choice
		{365, 365, 365},
	}
	for _, c := range cases {
		if got := monitorsettings.EffectiveRetentionDays(c.choice, c.max); got != c.want {
			t.Errorf("EffectiveRetentionDays(%d, %d) = %d, want %d", c.choice, c.max, got, c.want)
		}
	}
}

func TestSetRetentionChoice(t *testing.T) {
	st := newFakeStore()
	svc := monitorsettings.NewService(st, 90)
	org := uuid.Must(uuid.NewV7())

	// A choice below the maximum is effective.
	got, err := svc.Set(ctx, org, monitorsettings.Update{ClientAbsentDays: 14, RetentionDays: intp(30)})
	if err != nil {
		t.Fatal(err)
	}
	if got.RetentionDays != 30 || got.RetentionMaxDays != 90 || got.RetentionEffectiveDays != 30 {
		t.Fatalf("Set(30) = %+v, want choice 30, max 90, effective 30", got)
	}
	// Omitting the choice keeps it while client_absent_days changes.
	got, err = svc.Set(ctx, org, monitorsettings.Update{ClientAbsentDays: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got.ClientAbsentDays != 3 || got.RetentionDays != 30 || got.RetentionEffectiveDays != 30 {
		t.Fatalf("Set without a retention choice = %+v, want 3 and the kept 30", got)
	}
	// A choice above the maximum is stored but capped.
	got, err = svc.Set(ctx, org, monitorsettings.Update{ClientAbsentDays: 3, RetentionDays: intp(365)})
	if err != nil {
		t.Fatal(err)
	}
	if got.RetentionDays != 365 || got.RetentionEffectiveDays != 90 || got.PreviousRetentionDays != 30 {
		t.Fatalf("Set(365) under max 90 = %+v, want choice 365, effective 90", got)
	}
	// 0 clears the choice back to the instance maximum.
	got, err = svc.Set(ctx, org, monitorsettings.Update{ClientAbsentDays: 3, RetentionDays: intp(0)})
	if err != nil {
		t.Fatal(err)
	}
	if got.RetentionDays != 0 || got.RetentionEffectiveDays != 90 {
		t.Fatalf("Set(clear) = %+v, want no choice, effective 90", got)
	}
	if after, _ := svc.Get(ctx, org); after.RetentionDays != 0 || after.ClientAbsentDays != 3 || after.RetentionEffectiveDays != 90 {
		t.Fatalf("Get after clear = %+v", after)
	}
}

func TestGetAppliesALowerInstanceMaximum(t *testing.T) {
	st := newFakeStore()
	org := uuid.Must(uuid.NewV7())
	st.rows[org] = monitorsettings.Settings{ClientAbsentDays: 14, RetentionDays: 90}
	got, err := monitorsettings.NewService(st, 30).Get(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if got.RetentionDays != 90 || got.RetentionMaxDays != 30 || got.RetentionEffectiveDays != 30 {
		t.Fatalf("Get under max 30 = %+v, want choice 90, max 30, effective 30", got)
	}
}

func TestSetRefusesOtherRetentionValues(t *testing.T) {
	for _, days := range []int{-1, 1, 7, 29, 31, 60, 180, 364, 366, 1000} {
		st := newFakeStore()
		svc := monitorsettings.NewService(st, 365)
		_, err := svc.Set(ctx, uuid.Must(uuid.NewV7()), monitorsettings.Update{ClientAbsentDays: 14, RetentionDays: intp(days)})
		if !errors.Is(err, monitorsettings.ErrInvalidInput) {
			t.Errorf("Set(retention %d) = %v, want ErrInvalidInput", days, err)
		}
		if st.putRuns != 0 {
			t.Errorf("Set(retention %d) wrote to the store", days)
		}
	}
}
