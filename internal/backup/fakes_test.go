package backup_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/backup"
	"github.com/noixdev/pulsix/internal/crypto"
)

// fakeStore keeps settings and attempts in memory. lockedElsewhere stands
// for another session holding the backup lock.
type fakeStore struct {
	mu sync.Mutex
	// fail makes the named method return its error; panicOn makes it panic.
	fail            map[string]error
	panicOn         string
	settings        map[string][]byte
	attempts        []backup.Attempt
	lockedElsewhere bool
	locked          bool
	lockCalls       int
}

func newFakeStore() *fakeStore {
	return &fakeStore{settings: map[string][]byte{}, fail: map[string]error{}}
}

// failure returns the injected error of method; the caller holds f.mu.
func (f *fakeStore) failure(method string) error {
	if f.panicOn == method {
		panic("fake store: " + method)
	}
	return f.fail[method]
}

func (f *fakeStore) LoadSetting(_ context.Context, key string) ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure("LoadSetting"); err != nil {
		return nil, false, err
	}
	v, ok := f.settings[key]
	return v, ok, nil
}

func (f *fakeStore) SaveSetting(_ context.Context, key string, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure("SaveSetting"); err != nil {
		return err
	}
	f.settings[key] = slices.Clone(value)
	return nil
}

func (f *fakeStore) TryLock(context.Context) (func(), bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure("TryLock"); err != nil {
		return nil, false, err
	}
	f.lockCalls++
	if f.lockedElsewhere || f.locked {
		return nil, false, nil
	}
	f.locked = true
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.locked = false
	}, true, nil
}

func (f *fakeStore) AnyRunning(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure("AnyRunning"); err != nil {
		return false, err
	}
	for _, a := range f.attempts {
		if a.State == backup.StateRunning {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) AbandonRunning(_ context.Context, at time.Time, reason string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure("AbandonRunning"); err != nil {
		return 0, err
	}
	var n int64
	for i := range f.attempts {
		if f.attempts[i].State == backup.StateRunning {
			f.attempts[i].State = backup.StateAbandoned
			f.attempts[i].FinishedAt = &at
			f.attempts[i].Error = reason
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) SlotClaimed(_ context.Context, slot time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure("SlotClaimed"); err != nil {
		return false, err
	}
	for _, a := range f.attempts {
		if a.Kind == backup.KindScheduled && a.State != backup.StateAbandoned && a.Slot != nil && a.Slot.Equal(slot) {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) InsertAttempt(_ context.Context, n backup.NewAttempt) (uuid.UUID, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure("InsertAttempt"); err != nil {
		return uuid.Nil, false, err
	}
	if n.Kind == backup.KindScheduled && n.Slot != nil {
		for _, a := range f.attempts {
			if a.Kind == backup.KindScheduled && a.State != backup.StateAbandoned && a.Slot != nil && a.Slot.Equal(*n.Slot) {
				return uuid.Nil, false, nil
			}
		}
	}
	id := uuid.New()
	f.attempts = append(f.attempts, backup.Attempt{
		ID: id, Kind: n.Kind, Slot: n.Slot, StartedAt: n.StartedAt, State: backup.StateRunning,
		ObjectName: n.ObjectName, Actor: n.Actor,
	})
	return id, true, nil
}

func (f *fakeStore) FinishAttempt(_ context.Context, id uuid.UUID, fin backup.Finish) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure("FinishAttempt"); err != nil {
		return err
	}
	for i := range f.attempts {
		if f.attempts[i].ID == id && f.attempts[i].State == backup.StateRunning {
			at := fin.At
			f.attempts[i].State, f.attempts[i].FinishedAt = fin.State, &at
			f.attempts[i].Size, f.attempts[i].Error = fin.Size, fin.Error
		}
	}
	return nil
}

func (f *fakeStore) RecentAttempts(_ context.Context, limit int) ([]backup.Attempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure("RecentAttempts"); err != nil {
		return nil, err
	}
	out := slices.Clone(f.attempts)
	slices.SortStableFunc(out, func(a, b backup.Attempt) int { return b.StartedAt.Compare(a.StartedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// attempt returns the attempt with id.
func (f *fakeStore) attempt(t *testing.T, id uuid.UUID) backup.Attempt {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.attempts {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no attempt %s", id)
	return backup.Attempt{}
}

// addAttempt seeds an attempt row directly.
func (f *fakeStore) addAttempt(a backup.Attempt) uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	f.attempts = append(f.attempts, a)
	return a.ID
}

func (f *fakeStore) allAttempts() []backup.Attempt {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.attempts)
}

// memStorage is an in-memory Storage that records deletions.
type memStorage struct {
	mu      sync.Mutex
	objects map[string][]byte
	deleted []string
	putErr  error
	listErr error
	// putPanics makes Put panic before it reads anything.
	putPanics bool
}

func newMemStorage() *memStorage { return &memStorage{objects: map[string][]byte{}} }

func (m *memStorage) Put(ctx context.Context, name string, r io.Reader) (int64, error) {
	if m.putPanics {
		panic("mem storage: put " + name)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, fmt.Errorf("upload %s: %w", name, err)
	}
	if m.putErr != nil {
		return 0, m.putErr
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[name] = data
	return int64(len(data)), nil
}

func (m *memStorage) Open(_ context.Context, name string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[name]
	if !ok {
		return nil, fmt.Errorf("open %s: %w", name, backup.ErrObjectNotFound)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *memStorage) List(context.Context) ([]backup.Object, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var objs []backup.Object
	for name, data := range m.objects {
		if !strings.HasSuffix(name, ".pxbak") {
			continue
		}
		o := backup.Object{Name: name, Size: int64(len(data))}
		o.Header, o.HeaderErr = backup.ReadHeader(bytes.NewReader(data))
		objs = append(objs, o)
	}
	slices.SortFunc(objs, func(a, b backup.Object) int { return strings.Compare(b.Name, a.Name) })
	return objs, nil
}

func (m *memStorage) Delete(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, name)
	m.deleted = append(m.deleted, name)
	return nil
}

func (m *memStorage) Describe() string { return "memory" }

// putBackup stores a header-only backup object created at at, with an
// unsigned header.
func (m *memStorage) putBackup(t *testing.T, at time.Time, kind string) string {
	t.Helper()
	name := backup.ObjectName(at, kind)
	m.putHeader(t, name, backup.Header{CreatedAt: at, Kind: kind, PulsixVersion: "0.9.0"})
	return name
}

// putSignedBackup is putBackup with the header signed under the harness's
// header key, as the harness's own backups are.
func (m *memStorage) putSignedBackup(t *testing.T, at time.Time, kind string) string {
	t.Helper()
	name := backup.ObjectName(at, kind)
	h := backup.SignHeader(backup.Header{CreatedAt: at, Kind: kind, PulsixVersion: "0.9.0"}, backup.HeaderKey(harnessKey))
	m.putHeader(t, name, h)
	return name
}

// putHeader stores an object under name holding h and a short payload.
func (m *memStorage) putHeader(t *testing.T, name string, h backup.Header) {
	t.Helper()
	var buf bytes.Buffer
	if err := backup.WriteHeader(&buf, h); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[name] = append(buf.Bytes(), "payload"...)
}

func (m *memStorage) has(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.objects[name]
	return ok
}

func (m *memStorage) deletedNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.deleted)
}

// fakeDump writes a header and a short payload, or fails with err.
type fakeDump struct {
	// headerKey signs the header, as Dump does with the master key's.
	headerKey []byte
	mu        sync.Mutex
	calls     []backup.DumpOptions
	err       error
	// block, when set, makes the dump wait for its context to end.
	block  bool
	panics bool
}

func (d *fakeDump) dump(ctx context.Context, w io.Writer, opts backup.DumpOptions) (backup.Header, error) {
	d.mu.Lock()
	d.calls = append(d.calls, opts)
	err, block := d.err, d.block
	d.mu.Unlock()
	if d.panics {
		panic("fake dump")
	}
	h := backup.Header{CreatedAt: opts.Now().UTC(), Kind: opts.Kind, PulsixVersion: opts.PulsixVersion, IncludesMetrics: opts.IncludeMetrics}
	h = backup.SignHeader(h, d.headerKey)
	if err := backup.WriteHeader(w, h); err != nil {
		return backup.Header{}, err
	}
	if block {
		<-ctx.Done()
		return backup.Header{}, ctx.Err()
	}
	if err != nil {
		return backup.Header{}, err
	}
	if _, err := w.Write([]byte("encrypted payload")); err != nil {
		return backup.Header{}, err
	}
	return h, nil
}

func (d *fakeDump) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls)
}

// fakeMailer records the mails sent to instance admins.
type fakeMailer struct {
	mu    sync.Mutex
	mails []string // subject + "\n" + body
}

func (m *fakeMailer) SendToInstanceAdmins(_ context.Context, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mails = append(m.mails, subject+"\n"+body)
	return nil
}

func (m *fakeMailer) sent() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.mails)
}

// clock is a settable time source.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// harnessKey is the master key of the harness's service.
var harnessKey = [32]byte{9}

// harness is a Service over fakes.
type harness struct {
	svc     *backup.Service
	store   *fakeStore
	storage *memStorage
	dump    *fakeDump
	mailer  *fakeMailer
	clock   *clock
	// secrets records the secret every storageFor call received.
	secrets []string
}

var errStorageConfig = errors.New("storage config refused")

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		store: newFakeStore(), storage: newMemStorage(), dump: &fakeDump{headerKey: backup.HeaderKey(harnessKey)}, mailer: &fakeMailer{},
		clock: &clock{now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)},
	}
	storageFor := func(s backup.Settings, secret string) (backup.Storage, error) {
		h.secrets = append(h.secrets, secret)
		if s.Destination == backup.DestinationS3 && s.S3.Bucket == "refused" {
			return nil, errStorageConfig
		}
		return h.storage, nil
	}
	h.svc = backup.NewService(h.store, crypto.NewBox(harnessKey), backup.HeaderKey(harnessKey), h.dump.dump, storageFor, h.mailer, "1.2.3", h.clock.Now, nil)
	t.Cleanup(h.svc.Stop)
	return h
}

// enableSchedule saves an enabled daily schedule at timeOfDay UTC.
func (h *harness) enableSchedule(t *testing.T, timeOfDay string, r backup.Retention) {
	t.Helper()
	in := backup.DefaultSettings()
	in.Enabled, in.TimeOfDay, in.TimeZone, in.Retention = true, timeOfDay, "UTC", r
	if err := h.svc.SaveSettings(context.Background(), in, nil); err != nil {
		t.Fatalf("save settings: %v", err)
	}
}
