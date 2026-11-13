package checks_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/checks"
)

// fakePolicy is one hypertable's retention policy in fakeRetentionStore.
type fakePolicy struct {
	dropAfter time.Duration
}

// fakeRetentionStore keeps the policies in a map and records every set.
// committed is what a transaction that returned nil left behind; a failing
// fn leaves the policies as they were, like a rollback.
type fakeRetentionStore struct {
	policies map[string]fakePolicy
	sets     []string
	txs      int
	txErr    error
	readErr  error
	setErr   error
}

func (s *fakeRetentionStore) InRetentionTx(ctx context.Context, fn func(checks.RetentionPolicyTx) error) error {
	s.txs++
	if s.txErr != nil {
		return s.txErr
	}
	work := &fakeRetentionTx{s: s, policies: map[string]fakePolicy{}}
	for k, v := range s.policies {
		work.policies[k] = v
	}
	if err := fn(work); err != nil {
		return err
	}
	s.policies = work.policies
	s.sets = append(s.sets, work.sets...)
	return nil
}

type fakeRetentionTx struct {
	s        *fakeRetentionStore
	policies map[string]fakePolicy
	sets     []string
}

func (t *fakeRetentionTx) RetentionPolicy(_ context.Context, table string) (time.Duration, bool, error) {
	if t.s.readErr != nil {
		return 0, false, t.s.readErr
	}
	p, ok := t.policies[table]
	return p.dropAfter, ok, nil
}

func (t *fakeRetentionTx) SetRetentionPolicy(_ context.Context, table string, days int) error {
	if t.s.setErr != nil {
		return t.s.setErr
	}
	t.policies[table] = fakePolicy{dropAfter: time.Duration(days) * 24 * time.Hour}
	t.sets = append(t.sets, table)
	return nil
}

func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

func TestRetentionSyncSetsBothTablesWhenDifferent(t *testing.T) {
	store := &fakeRetentionStore{policies: map[string]fakePolicy{
		"check_samples":       {dropAfter: days(90)},
		"check_state_changes": {dropAfter: days(90)},
	}}
	var logs bytes.Buffer
	syncer := checks.NewRetentionSync(store, slog.New(slog.NewTextHandler(&logs, nil)))

	if err := syncer.Sync(context.Background(), 30); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"check_samples", "check_state_changes"} {
		if got := store.policies[table].dropAfter; got != days(30) {
			t.Errorf("%s drop_after = %v, want 30 days", table, got)
		}
		if !strings.Contains(logs.String(), "table="+table) {
			t.Errorf("no info log for %s: %s", table, logs.String())
		}
	}
	if len(store.sets) != 2 || store.txs != 1 {
		t.Errorf("sets = %v in %d transactions, want both tables in one", store.sets, store.txs)
	}
	if !strings.Contains(logs.String(), "level=INFO") || !strings.Contains(logs.String(), "from_days=90") || !strings.Contains(logs.String(), "to_days=30") {
		t.Errorf("log does not say the change at info: %s", logs.String())
	}
}

func TestRetentionSyncLeavesEqualPoliciesAlone(t *testing.T) {
	store := &fakeRetentionStore{policies: map[string]fakePolicy{
		"check_samples":       {dropAfter: days(90)},
		"check_state_changes": {dropAfter: days(90)},
	}}
	var logs bytes.Buffer
	syncer := checks.NewRetentionSync(store, slog.New(slog.NewTextHandler(&logs, nil)))

	if err := syncer.Sync(context.Background(), 90); err != nil {
		t.Fatal(err)
	}
	if len(store.sets) != 0 {
		t.Errorf("sets = %v, want none for an unchanged value", store.sets)
	}
	if logs.Len() != 0 {
		t.Errorf("logged on an unchanged value: %s", logs.String())
	}
}

func TestRetentionSyncSetsOnlyTheDifferingOrMissingTable(t *testing.T) {
	store := &fakeRetentionStore{policies: map[string]fakePolicy{
		"check_samples": {dropAfter: days(45)},
	}}
	var logs bytes.Buffer
	syncer := checks.NewRetentionSync(store, slog.New(slog.NewTextHandler(&logs, nil)))

	if err := syncer.Sync(context.Background(), 45); err != nil {
		t.Fatal(err)
	}
	if len(store.sets) != 1 || store.sets[0] != "check_state_changes" {
		t.Fatalf("sets = %v, want only the missing check_state_changes", store.sets)
	}
	if got := store.policies["check_state_changes"].dropAfter; got != days(45) {
		t.Errorf("check_state_changes drop_after = %v, want 45 days", got)
	}
	if !strings.Contains(logs.String(), "from_days=none") {
		t.Errorf("a missing policy should log from_days=none: %s", logs.String())
	}
}

// A policy that is not a whole number of days (set by hand) differs from
// every configured value and is replaced.
func TestRetentionSyncReplacesAPartialDayPolicy(t *testing.T) {
	store := &fakeRetentionStore{policies: map[string]fakePolicy{
		"check_samples":       {dropAfter: days(90) + time.Hour},
		"check_state_changes": {dropAfter: days(90)},
	}}
	syncer := checks.NewRetentionSync(store, slog.New(slog.DiscardHandler))
	if err := syncer.Sync(context.Background(), 90); err != nil {
		t.Fatal(err)
	}
	if len(store.sets) != 1 || store.sets[0] != "check_samples" {
		t.Fatalf("sets = %v, want only check_samples", store.sets)
	}
}

func TestRetentionSyncErrors(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name  string
		store *fakeRetentionStore
		days  int
	}{
		{name: "transaction", store: &fakeRetentionStore{txErr: boom}, days: 30},
		{name: "read", store: &fakeRetentionStore{readErr: boom}, days: 30},
		{name: "set", store: &fakeRetentionStore{setErr: boom}, days: 30},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checks.NewRetentionSync(c.store, nil).Sync(context.Background(), c.days)
			if !errors.Is(err, boom) {
				t.Fatalf("Sync = %v, want it to wrap %v", err, boom)
			}
			if len(c.store.sets) != 0 {
				t.Errorf("a failed sync left sets %v behind", c.store.sets)
			}
		})
	}
}

// A non-positive maximum would make the policy drop every chunk; the sync
// refuses it before touching the database.
func TestRetentionSyncRefusesANonPositiveMaximum(t *testing.T) {
	for _, n := range []int{0, -1} {
		store := &fakeRetentionStore{}
		err := checks.NewRetentionSync(store, nil).Sync(context.Background(), n)
		if err == nil {
			t.Fatalf("Sync(%d) = nil, want an error", n)
		}
		if store.txs != 0 {
			t.Errorf("Sync(%d) opened a transaction", n)
		}
	}
}
