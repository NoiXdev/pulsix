package assistant_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistant"
)

// fakeStore keeps one organization's settings and records the calls, so the
// tests see the update run inside the store's transaction.
type fakeStore struct {
	row     assistant.Settings
	calls   []string
	inTx    bool
	lockErr error
	putErr  error
}

func (f *fakeStore) Enabled(context.Context, uuid.UUID) (bool, error) { return f.row.Enabled, nil }

func (f *fakeStore) StartsFollowUps(context.Context, uuid.UUID) (bool, error) {
	return f.row.StartsFollowUps, nil
}

func (f *fakeStore) InTx(ctx context.Context, _ uuid.UUID, fn func(ctx context.Context) error) error {
	f.inTx = true
	defer func() { f.inTx = false }()
	f.calls = append(f.calls, "begin")
	return fn(ctx)
}

func (f *fakeStore) Lock(context.Context, uuid.UUID) (assistant.Settings, error) {
	f.calls = append(f.calls, "lock")
	if !f.inTx {
		return assistant.Settings{}, errors.New("lock outside the transaction")
	}
	return f.row, f.lockErr
}

func (f *fakeStore) Put(_ context.Context, _ uuid.UUID, s assistant.Settings) error {
	f.calls = append(f.calls, "put")
	if !f.inTx {
		return errors.New("put outside the transaction")
	}
	if f.putErr != nil {
		return f.putErr
	}
	f.row = s
	return nil
}

func ptr(b bool) *bool { return &b }

func TestUpdateKeepsWhatItIsNotGiven(t *testing.T) {
	cases := []struct {
		name   string
		stored assistant.Settings
		change assistant.Change
		want   assistant.Settings
	}{
		{"access only", assistant.Settings{StartsFollowUps: true}, assistant.Change{Enabled: ptr(true)}, assistant.Settings{Enabled: true, StartsFollowUps: true}},
		{"follow-ups only", assistant.Settings{Enabled: true}, assistant.Change{StartsFollowUps: ptr(true)}, assistant.Settings{Enabled: true, StartsFollowUps: true}},
		{"both", assistant.Settings{Enabled: true, StartsFollowUps: true}, assistant.Change{Enabled: ptr(false), StartsFollowUps: ptr(false)}, assistant.Settings{}},
		{"nothing actually changed", assistant.Settings{Enabled: true}, assistant.Change{Enabled: ptr(true)}, assistant.Settings{Enabled: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &fakeStore{row: tc.stored}
			before, after, err := assistant.NewService(st).Update(context.Background(), uuid.Nil, tc.change)
			if err != nil {
				t.Fatal(err)
			}
			if before != tc.stored || after != tc.want || st.row != tc.want {
				t.Fatalf("before %+v, after %+v, stored %+v; want before %+v, after and stored %+v", before, after, st.row, tc.stored, tc.want)
			}
			if len(st.calls) != 3 || st.calls[0] != "begin" || st.calls[1] != "lock" || st.calls[2] != "put" {
				t.Fatalf("calls = %v, want begin, lock, put", st.calls)
			}
		})
	}
}

func TestUpdateRefusesAnEmptyChange(t *testing.T) {
	st := &fakeStore{}
	if _, _, err := assistant.NewService(st).Update(context.Background(), uuid.Nil, assistant.Change{}); !errors.Is(err, assistant.ErrNothingToChange) {
		t.Fatalf("err = %v, want ErrNothingToChange", err)
	}
	if len(st.calls) != 0 {
		t.Fatalf("calls = %v, want none", st.calls)
	}
}

func TestUpdatePropagatesStoreErrors(t *testing.T) {
	boom := errors.New("boom")
	for _, st := range []*fakeStore{{lockErr: boom}, {putErr: boom}} {
		if _, _, err := assistant.NewService(st).Update(context.Background(), uuid.Nil, assistant.Change{Enabled: ptr(true)}); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
	}
}
