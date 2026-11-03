package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/crypto"
)

var (
	// ErrInvalid wraps every settings or argument error the caller can fix.
	ErrInvalid = errors.New("backup: invalid")
	// ErrBusy means another backup is running, on this manager or another.
	ErrBusy = errors.New("backup: another backup is running")
	// ErrNotFound means the destination holds no backup of that name.
	ErrNotFound = errors.New("backup: no such backup")
	// ErrStorageUnavailable means the destination could not be listed; List
	// still returns the recorded attempts with it.
	ErrStorageUnavailable = errors.New("backup: the backup destination is unavailable")

	errStopped = errors.New("backup: the backup service has stopped")
	// errUploadEnded unblocks a dump still writing after the upload returned.
	errUploadEnded = errors.New("backup: the upload ended")
)

// Backup kinds.
const (
	KindScheduled = "scheduled"
	KindManual    = "manual"
)

// Attempt and entry states. StateDamaged is only ever an Entry's: a stored
// object whose header cannot be read.
const (
	StateRunning   = "running"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
	StateAbandoned = "abandoned"
	StateDamaged   = "damaged"
)

const (
	// recentAttempts is how many attempts List merges with the objects.
	recentAttempts = 50
	reasonStale    = "the manager stopped before this backup finished"
	reasonStopped  = "the manager shut down during this backup"
	// finishTimeout bounds recording an attempt's end, which runs on a
	// context detached from the backup's own cancellation.
	finishTimeout = 30 * time.Second
)

// DumpFunc writes one backup to w. The caller binds it to a pool and the
// master key (Dump through the manager's pulsix_app pool, or the owner pool
// in the CLI).
type DumpFunc func(ctx context.Context, w io.Writer, opts DumpOptions) (Header, error)

// AdminMailer tells every instance administrator about a failed scheduled
// backup. An instance without outbound mail drops the message.
type AdminMailer interface {
	SendToInstanceAdmins(ctx context.Context, subject, body string) error
}

// Store keeps the backup settings and the attempt history, and the lock
// that says a backup is running.
type Store interface {
	// LoadSetting returns the instance setting key; found is false when
	// none is stored.
	LoadSetting(ctx context.Context, key string) (value []byte, found bool, err error)
	SaveSetting(ctx context.Context, key string, value []byte) error
	// TryLock takes the instance-wide backup lock without waiting. ok is
	// false when another session holds it; otherwise release must be
	// called once the backup is over.
	TryLock(ctx context.Context) (release func(), ok bool, err error)
	// AnyRunning reports whether an attempt is recorded as running.
	AnyRunning(ctx context.Context) (bool, error)
	// AbandonRunning marks every running attempt abandoned, with reason.
	AbandonRunning(ctx context.Context, at time.Time, reason string) (int64, error)
	// SlotClaimed reports whether a scheduled attempt holds slot. An
	// abandoned attempt does not: its slot can be claimed again.
	SlotClaimed(ctx context.Context, slot time.Time) (bool, error)
	// InsertAttempt records a running attempt. For a scheduled attempt
	// whose slot is already held by an attempt that is not abandoned,
	// inserted is false.
	InsertAttempt(ctx context.Context, a NewAttempt) (id uuid.UUID, inserted bool, err error)
	// FinishAttempt records the end of a running attempt; an attempt that
	// is no longer running is left as it is.
	FinishAttempt(ctx context.Context, id uuid.UUID, f Finish) error
	// RecentAttempts returns the newest attempts first.
	RecentAttempts(ctx context.Context, limit int) ([]Attempt, error)
}

// NewAttempt is an attempt as it starts.
type NewAttempt struct {
	Kind       string
	Slot       *time.Time // scheduled attempts only
	StartedAt  time.Time
	ObjectName string
	Actor      *uuid.UUID
}

// Finish is how an attempt ended.
type Finish struct {
	At    time.Time
	State string // succeeded, failed or abandoned
	Size  int64  // succeeded only
	Error string
}

// Attempt is one recorded backup attempt.
type Attempt struct {
	ID         uuid.UUID
	Kind       string
	Slot       *time.Time
	StartedAt  time.Time
	FinishedAt *time.Time
	State      string
	ObjectName string
	Size       int64
	Error      string
	Actor      *uuid.UUID
	// ActorName is the actor's display name; empty without an actor or
	// once the account is gone.
	ActorName string
}

// Entry is one row of the backup list: a stored backup, an attempt that
// left none, or both merged.
type Entry struct {
	Name          string
	CreatedAt     time.Time
	Kind          string
	Actor         *uuid.UUID
	ActorName     string
	PulsixVersion string
	Size          int64
	State         string
	Error         string
	// Verified is true for a stored backup whose header is authentic and
	// matches its name (MarkVerified); false for every other entry.
	Verified bool
}

// Service runs backups: on demand, on the daily schedule (RunDue, driven by
// a Ticker) and from the command line (Backup). It owns the backup settings
// and the attempt history; the destination's listing is the catalogue.
type Service struct {
	store      Store
	box        *crypto.Box
	headerKey  []byte
	dump       DumpFunc
	storageFor func(Settings, string) (Storage, error)
	mailer     AdminMailer
	version    string
	now        func() time.Time
	log        *slog.Logger

	// bg is the context of backups running in the background; Stop
	// cancels it and waits for them.
	bg      context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	mu      sync.Mutex
	stopped bool
}

// NewService builds the service. headerKey is HeaderKey of the master key
// the dump encrypts under; listings mark the backups it authenticates as
// verified. storageFor opens the destination the settings describe, given
// the S3 secret in the clear; version is the Pulsix version written into
// every backup. A nil now is time.Now and a nil log slog.Default.
func NewService(store Store, box *crypto.Box, headerKey []byte, dump DumpFunc, storageFor func(Settings, string) (Storage, error), mailer AdminMailer, version string, now func() time.Time, log *slog.Logger) *Service {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	bg, cancel := context.WithCancel(context.Background())
	return &Service{
		store: store, box: box, headerKey: headerKey, dump: dump, storageFor: storageFor, mailer: mailer, version: version, now: now, log: log,
		bg: bg, cancel: cancel,
	}
}

// Stop cancels backups running in the background and waits until each has
// recorded its end. StartManual fails afterwards. Stop may be called more
// than once.
func (s *Service) Stop() {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
}

// enter registers a background backup; false once stopped.
func (s *Service) enter() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return false
	}
	s.wg.Add(1)
	return true
}

// AbandonStale marks every running attempt abandoned when no backup holds
// the lock: such a row was left by a manager that crashed or came back with
// a restore. The manager calls it once at start; a running backup elsewhere
// keeps its row.
func (s *Service) AbandonStale(ctx context.Context) error {
	release, ok, err := s.store.TryLock(ctx)
	if err != nil {
		return fmt.Errorf("abandon stale backup attempts: %w", err)
	}
	if !ok {
		return nil
	}
	defer release()
	return s.abandon(ctx)
}

// abandon marks running attempts abandoned; the caller holds the lock, so
// none of them is running.
func (s *Service) abandon(ctx context.Context) error {
	n, err := s.store.AbandonRunning(ctx, s.now().UTC(), reasonStale)
	if err != nil {
		return fmt.Errorf("abandon stale backup attempts: %w", err)
	}
	if n > 0 {
		s.log.InfoContext(ctx, "marked backup attempts abandoned that no manager was running", "count", n)
	}
	return nil
}

// Backup takes one backup into st and waits for it: it records the attempt,
// dumps, and records the result. It returns ErrBusy while another backup
// runs. A failed backup returns its entry (state failed) and the error.
func (s *Service) Backup(ctx context.Context, kind string, actor *uuid.UUID, st Storage, includeMetrics bool) (Entry, error) {
	if kind != KindManual && kind != KindScheduled {
		return Entry{}, fmt.Errorf("%w: backup kind %q", ErrInvalid, kind)
	}
	release, ok, err := s.store.TryLock(ctx)
	if err != nil {
		return Entry{}, fmt.Errorf("backup: %w", err)
	}
	if !ok {
		return Entry{}, ErrBusy
	}
	defer release()
	r, _, err := s.record(ctx, kind, nil, actor)
	if err != nil {
		return Entry{}, err
	}
	return s.execute(ctx, r, st, includeMetrics)
}

// StartManual starts a manual backup to the configured destination in the
// background and returns its attempt's id. It returns ErrBusy while another
// backup runs.
func (s *Service) StartManual(ctx context.Context, actor uuid.UUID) (uuid.UUID, error) {
	if !s.enter() {
		return uuid.Nil, errStopped
	}
	started := false
	defer func() {
		if !started {
			s.wg.Done()
		}
	}()

	set, secret, err := s.current(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	st, err := s.storageFor(set, secret)
	if err != nil {
		return uuid.Nil, fmt.Errorf("backup destination: %w", err)
	}
	release, ok, err := s.store.TryLock(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("backup: %w", err)
	}
	if !ok {
		return uuid.Nil, ErrBusy
	}
	// Until the background backup owns the lock, every way out releases
	// it, a panic in record included (final review minor 5).
	defer func() {
		if !started {
			release()
		}
	}()
	r, _, err := s.record(ctx, KindManual, nil, &actor)
	if err != nil {
		return uuid.Nil, err
	}
	started = true
	go func() {
		defer s.wg.Done()
		defer release()
		// No request's recover reaches this goroutine: a panic in the
		// upload or in recording the end would end the manager. It fails
		// the attempt instead, and the lock is released after that.
		defer func() {
			if p := recover(); p != nil {
				s.failPanicked(s.bg, r, p)
			}
		}()
		_, _ = s.execute(s.bg, r, st, set.IncludeMetrics)
	}()
	return r.id, nil
}
