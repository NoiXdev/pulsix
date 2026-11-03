package backup_test

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/backup"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

const fixtureUser = "00000000-0000-4000-8000-0000000000c1"

// sqlStore opens the store through pulsix_app, the way the manager does, and
// returns a superuser handle for fixtures next to it.
func sqlStore(t *testing.T) (*backup.SQLStore, *db.DB, *db.DB) {
	t.Helper()
	app := testdb.Open(t)
	return backup.NewSQLStore(app), app, sourceDB(t)
}

func TestSQLStoreAttempts(t *testing.T) {
	store, _, admin := sqlStore(t)
	ctx := context.Background()
	exec(t, admin.Pool(), `INSERT INTO users (id, email, display_name, password_hash) VALUES ('`+fixtureUser+`', 'ada@example.com', 'Ada', 'x')`)
	actor := uuid.MustParse(fixtureUser)
	t0 := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)

	manual, ok, err := store.InsertAttempt(ctx, backup.NewAttempt{Kind: backup.KindManual, StartedAt: t0.Add(-time.Hour), ObjectName: "m.pxbak", Actor: &actor})
	if err != nil || !ok {
		t.Fatalf("insert manual: %v %v", ok, err)
	}
	if _, ok, err := store.InsertAttempt(ctx, backup.NewAttempt{Kind: backup.KindManual, StartedAt: t0.Add(-2 * time.Hour), ObjectName: "m2.pxbak"}); err != nil || !ok {
		t.Fatalf("a second manual attempt: %v %v", ok, err)
	}
	slot := t0
	sched, ok, err := store.InsertAttempt(ctx, backup.NewAttempt{Kind: backup.KindScheduled, Slot: &slot, StartedAt: t0, ObjectName: "s.pxbak"})
	if err != nil || !ok {
		t.Fatalf("insert scheduled: %v %v", ok, err)
	}
	if _, ok, err := store.InsertAttempt(ctx, backup.NewAttempt{Kind: backup.KindScheduled, Slot: &slot, StartedAt: t0.Add(time.Second), ObjectName: "s2.pxbak"}); err != nil || ok {
		t.Fatalf("a second claim of the same slot: inserted=%v err=%v, want not inserted", ok, err)
	}
	if claimed, err := store.SlotClaimed(ctx, slot); err != nil || !claimed {
		t.Errorf("SlotClaimed = %v %v", claimed, err)
	}
	if claimed, _ := store.SlotClaimed(ctx, slot.Add(24*time.Hour)); claimed {
		t.Error("tomorrow's slot claimed")
	}
	if running, err := store.AnyRunning(ctx); err != nil || !running {
		t.Errorf("AnyRunning = %v %v", running, err)
	}

	if err := store.FinishAttempt(ctx, sched, backup.Finish{At: t0.Add(time.Minute), State: backup.StateSucceeded, Size: 1234}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAttempt(ctx, manual, backup.Finish{At: t0.Add(time.Minute), State: backup.StateFailed, Error: "disk full"}); err != nil {
		t.Fatal(err)
	}
	n, err := store.AbandonRunning(ctx, t0.Add(2*time.Minute), "gone")
	if err != nil || n != 1 {
		t.Fatalf("AbandonRunning = %d %v, want the one left running", n, err)
	}
	if running, _ := store.AnyRunning(ctx); running {
		t.Error("AnyRunning after abandoning")
	}

	got, err := store.RecentAttempts(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("attempts = %+v", got)
	}
	s, m, abandoned := got[0], got[1], got[2]
	if s.ID != sched || s.State != backup.StateSucceeded || s.Size != 1234 || s.Slot == nil || !s.Slot.Equal(slot) || s.Kind != backup.KindScheduled ||
		s.FinishedAt == nil || !s.FinishedAt.Equal(t0.Add(time.Minute)) || s.ObjectName != "s.pxbak" || s.Actor != nil || s.Error != "" {
		t.Errorf("scheduled = %+v", s)
	}
	if abandoned.State != backup.StateAbandoned || abandoned.Error != "gone" || abandoned.FinishedAt == nil {
		t.Errorf("abandoned = %+v", abandoned)
	}
	if m.ID != manual || m.State != backup.StateFailed || m.Error != "disk full" || m.Size != 0 || m.Actor == nil || *m.Actor != actor || m.ActorName != "Ada" || m.Slot != nil {
		t.Errorf("manual = %+v", m)
	}
	if limited, _ := store.RecentAttempts(ctx, 1); len(limited) != 1 || limited[0].ID != sched {
		t.Errorf("RecentAttempts(1) = %+v", limited)
	}

	// A finished attempt is not finished again.
	if err := store.FinishAttempt(ctx, sched, backup.Finish{At: t0.Add(time.Hour), State: backup.StateFailed, Error: "late"}); err != nil {
		t.Fatal(err)
	}
	// The actor's account goes; the history stays, without a name.
	exec(t, admin.Pool(), `DELETE FROM users WHERE id = '`+fixtureUser+`'`)
	got, _ = store.RecentAttempts(ctx, 50)
	if got[0].State != backup.StateSucceeded {
		t.Errorf("finishing twice changed the attempt to %s", got[0].State)
	}
	if got[1].Actor != nil || got[1].ActorName != "" {
		t.Errorf("attempt of a deleted user = %+v, want no actor", got[1])
	}
}

func TestBackupRunsRejectsInconsistentRows(t *testing.T) {
	_, _, admin := sqlStore(t)
	for _, sql := range []string{
		`INSERT INTO backup_runs (kind, started_at, state) VALUES ('weekly', now(), 'running')`,
		`INSERT INTO backup_runs (kind, started_at, state) VALUES ('manual', now(), 'paused')`,
		`INSERT INTO backup_runs (kind, started_at, state, finished_at) VALUES ('manual', now(), 'running', now())`,
		`INSERT INTO backup_runs (kind, started_at, state) VALUES ('manual', now(), 'failed')`,
		`INSERT INTO backup_runs (kind, started_at, state, slot) VALUES ('manual', now(), 'running', now())`,
	} {
		if _, err := admin.Pool().Exec(context.Background(), sql); err == nil {
			t.Errorf("accepted: %s", sql)
		}
	}
}

func TestSQLStoreLockIsExclusiveAcrossSessions(t *testing.T) {
	store, app, _ := sqlStore(t)
	ctx := context.Background()
	release, ok, err := store.TryLock(ctx)
	if err != nil || !ok {
		t.Fatalf("first TryLock = %v %v", ok, err)
	}
	other := backup.NewSQLStore(app)
	if _, ok, err := other.TryLock(ctx); err != nil || ok {
		t.Fatalf("second TryLock while held = %v %v, want refused", ok, err)
	}
	release()
	release2, ok, err := other.TryLock(ctx)
	if err != nil || !ok {
		t.Fatalf("TryLock after release = %v %v", ok, err)
	}
	release2()
}

func TestSQLStoreSettingsAndAdmins(t *testing.T) {
	store, _, admin := sqlStore(t)
	ctx := context.Background()
	if _, found, err := store.LoadSetting(ctx, "backup"); err != nil || found {
		t.Fatalf("LoadSetting before any save = found %v, %v", found, err)
	}
	if err := store.SaveSetting(ctx, "backup", []byte(`{"enabled":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSetting(ctx, "backup", []byte(`{"enabled":false}`)); err != nil {
		t.Fatal(err)
	}
	v, found, err := store.LoadSetting(ctx, "backup")
	if err != nil || !found || string(v) != `{"enabled": false}` {
		t.Errorf("LoadSetting = %s %v %v", v, found, err)
	}

	exec(t, admin.Pool(), `INSERT INTO users (id, email, display_name, password_hash, instance_admin, disabled_at) VALUES
		('00000000-0000-4000-8000-0000000000d1', 'root@example.com', 'Root', 'x', true, NULL),
		('00000000-0000-4000-8000-0000000000d2', 'gone@example.com', 'Gone', 'x', true, now()),
		('00000000-0000-4000-8000-0000000000d3', 'admin2@example.com', 'Second', 'x', true, NULL),
		('00000000-0000-4000-8000-0000000000d4', 'member@example.com', 'Member', 'x', false, NULL)`)
	emails, err := store.InstanceAdminEmails(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(emails, []string{"admin2@example.com", "root@example.com"}) {
		t.Errorf("InstanceAdminEmails = %v", emails)
	}
}

// sqlService is a Service over the real store and the real dump through
// pulsix_app, backing up into dir.
func sqlService(t *testing.T, app *db.DB, dir string, now func() time.Time, dumps *int, mu *sync.Mutex) *backup.Service {
	t.Helper()
	dump := func(ctx context.Context, w io.Writer, opts backup.DumpOptions) (backup.Header, error) {
		mu.Lock()
		*dumps++
		mu.Unlock()
		return backup.Dump(ctx, app.Pool(), testKey, w, opts)
	}
	storageFor := func(backup.Settings, string) (backup.Storage, error) { return backup.NewDirStorage(dir), nil }
	svc := backup.NewService(backup.NewSQLStore(app), crypto.NewBox(testKey), backup.HeaderKey(testKey), dump, storageFor, &fakeMailer{}, "1.2.3", now, nil)
	t.Cleanup(svc.Stop)
	return svc
}

func TestRunDueClaimsSlotOnce(t *testing.T) {
	app := testdb.Open(t)
	ctx := context.Background()
	dir := t.TempDir()
	c := &clock{now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	var dumps int
	var mu sync.Mutex
	a := sqlService(t, app, dir, c.Now, &dumps, &mu)
	b := sqlService(t, app, dir, c.Now, &dumps, &mu)

	in := backup.DefaultSettings()
	in.Enabled = true
	if err := a.SaveSettings(ctx, in, nil); err != nil {
		t.Fatal(err)
	}
	c.Set(time.Date(2026, 11, 2, 3, 0, 10, 0, time.UTC))

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, svc := range []*backup.Service{a, b} {
		wg.Go(func() { errs[i] = svc.RunDue(ctx) })
	}
	wg.Wait()
	// Whichever lost the race may have found the lock busy; a later tick
	// must find the slot taken.
	for i, svc := range []*backup.Service{a, b} {
		if errs[i] != nil {
			t.Fatalf("RunDue: %v", errs[i])
		}
		if err := svc.RunDue(ctx); err != nil {
			t.Fatal(err)
		}
	}

	if dumps != 1 {
		t.Errorf("%d dumps for one slot, want 1", dumps)
	}
	attempts, err := backup.NewSQLStore(app).RecentAttempts(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].State != backup.StateSucceeded || attempts[0].Kind != backup.KindScheduled {
		t.Fatalf("attempts = %+v, want one succeeded scheduled attempt", attempts)
	}
	objs, err := backup.NewDirStorage(dir).List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 || objs[0].Name != attempts[0].ObjectName || objs[0].HeaderErr != nil {
		t.Errorf("objects = %+v, want the one backup", objs)
	}
}

func TestBackupRefusedWhileAnotherSessionHoldsTheLock(t *testing.T) {
	app := testdb.Open(t)
	ctx := context.Background()
	var dumps int
	var mu sync.Mutex
	svc := sqlService(t, app, t.TempDir(), time.Now, &dumps, &mu)

	release, ok, err := backup.NewSQLStore(app).TryLock(ctx)
	if err != nil || !ok {
		t.Fatalf("TryLock = %v %v", ok, err)
	}
	if _, err := svc.StartManual(ctx, uuid.New()); !errors.Is(err, backup.ErrBusy) {
		t.Errorf("StartManual while another session backs up = %v, want ErrBusy", err)
	}
	release()

	// Released: a running row left behind is stale and does not block.
	exec(t, sourceDB(t).Pool(), `INSERT INTO backup_runs (kind, started_at, state) VALUES ('manual', now(), 'running')`)
	e, err := svc.Backup(ctx, backup.KindManual, nil, backup.NewDirStorage(t.TempDir()), false)
	if err != nil || e.State != backup.StateSucceeded {
		t.Fatalf("Backup = %+v %v", e, err)
	}
	attempts, _ := backup.NewSQLStore(app).RecentAttempts(ctx, 50)
	states := []string{attempts[0].State, attempts[1].State}
	slices.Sort(states)
	if !slices.Equal(states, []string{backup.StateAbandoned, backup.StateSucceeded}) {
		t.Errorf("attempt states = %v, want the stale one abandoned and the new one succeeded", states)
	}
}

// TestAbandonedSlotCanBeClaimedOnceMore: an abandoned scheduled attempt
// releases its slot, and the slot is still claimed by one manager only.
func TestAbandonedSlotCanBeClaimedOnceMore(t *testing.T) {
	store, app, _ := sqlStore(t)
	ctx := context.Background()
	slot := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	if _, ok, err := store.InsertAttempt(ctx, backup.NewAttempt{Kind: backup.KindScheduled, Slot: &slot, StartedAt: slot, ObjectName: "a.pxbak"}); err != nil || !ok {
		t.Fatalf("first claim: %v %v", ok, err)
	}
	if _, err := store.AbandonRunning(ctx, slot.Add(time.Minute), "interrupted"); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.SlotClaimed(ctx, slot); err != nil || claimed {
		t.Fatalf("SlotClaimed after the attempt was abandoned = %v %v, want free", claimed, err)
	}

	// Two managers race for the re-opened slot.
	stores := []*backup.SQLStore{backup.NewSQLStore(app), backup.NewSQLStore(app)}
	inserted := make([]bool, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, st := range stores {
		wg.Go(func() {
			_, inserted[i], errs[i] = st.InsertAttempt(ctx, backup.NewAttempt{Kind: backup.KindScheduled, Slot: &slot, StartedAt: slot.Add(2 * time.Minute), ObjectName: "b.pxbak"})
		})
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || inserted[0] == inserted[1] {
		t.Fatalf("re-claims: inserted %v, errors %v; want exactly one", inserted, errs)
	}
	if claimed, _ := store.SlotClaimed(ctx, slot); !claimed {
		t.Error("the re-claimed slot reads as free")
	}
	// Succeeded and failed attempts keep their slot too.
	for _, state := range []string{backup.StateSucceeded, backup.StateFailed} {
		attempts, _ := store.RecentAttempts(ctx, 50)
		for _, a := range attempts {
			if a.State == backup.StateRunning {
				if err := store.FinishAttempt(ctx, a.ID, backup.Finish{At: slot.Add(3 * time.Minute), State: state}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if _, ok, err := store.InsertAttempt(ctx, backup.NewAttempt{Kind: backup.KindScheduled, Slot: &slot, StartedAt: slot.Add(4 * time.Minute), ObjectName: "c.pxbak"}); err != nil || ok {
			t.Errorf("claim of a %s slot: inserted=%v err=%v, want refused", state, ok, err)
		}
		if state == backup.StateSucceeded {
			// Make the slot's attempt failed instead, for the next round.
			if _, err := app.Pool().Exec(ctx, `UPDATE backup_runs SET state = 'running', finished_at = NULL WHERE state = 'succeeded'`); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestInterruptedScheduledBackupIsRetriedAgainstTheDatabase runs the
// interruption and the retry through the real store and dump.
func TestInterruptedScheduledBackupIsRetriedAgainstTheDatabase(t *testing.T) {
	app := testdb.Open(t)
	dir := t.TempDir()
	c := &clock{now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	var dumps int
	var mu sync.Mutex
	block := make(chan struct{})
	interrupt := true
	dump := func(ctx context.Context, w io.Writer, opts backup.DumpOptions) (backup.Header, error) {
		mu.Lock()
		dumps++
		stop := interrupt
		mu.Unlock()
		if stop {
			close(block)
			<-ctx.Done()
			return backup.Header{}, ctx.Err()
		}
		return backup.Dump(ctx, app.Pool(), testKey, w, opts)
	}
	storageFor := func(backup.Settings, string) (backup.Storage, error) { return backup.NewDirStorage(dir), nil }
	svc := backup.NewService(backup.NewSQLStore(app), crypto.NewBox(testKey), backup.HeaderKey(testKey), dump, storageFor, &fakeMailer{}, "1.2.3", c.Now, nil)
	t.Cleanup(svc.Stop)
	in := backup.DefaultSettings()
	in.Enabled = true
	if err := svc.SaveSettings(context.Background(), in, nil); err != nil {
		t.Fatal(err)
	}
	c.Set(time.Date(2026, 11, 2, 3, 0, 5, 0, time.UTC))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.RunDue(ctx) }()
	<-block
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	interrupt = false
	mu.Unlock()
	c.Set(time.Date(2026, 11, 2, 3, 1, 5, 0, time.UTC))
	if err := svc.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}

	attempts, err := backup.NewSQLStore(app).RecentAttempts(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 || attempts[0].State != backup.StateSucceeded || attempts[1].State != backup.StateAbandoned ||
		!attempts[0].Slot.Equal(*attempts[1].Slot) {
		t.Fatalf("attempts = %+v, want the abandoned one and the retry for the same slot", attempts)
	}
	objs, _ := backup.NewDirStorage(dir).List(context.Background())
	if len(objs) != 1 || objs[0].Name != attempts[0].ObjectName {
		t.Errorf("objects = %+v, want the retry's backup", objs)
	}
}
