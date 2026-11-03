package backup

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"
)

// DefaultTick is how often a manager looks for a due scheduled backup.
const DefaultTick = time.Minute

// dueSlot returns the current scheduled slot and whether it is due: on or
// after the time the schedule was set, and not claimed by any attempt that
// was not abandoned.
func (s *Service) dueSlot(ctx context.Context, b settingsBlob) (time.Time, bool, error) {
	loc, err := location(b.TimeZone)
	if err != nil {
		return time.Time{}, false, err
	}
	slot := SlotFor(s.now(), b.TimeOfDay, loc)
	if b.ScheduledSince != nil && slot.Before(*b.ScheduledSince) {
		return slot, false, nil
	}
	claimed, err := s.store.SlotClaimed(ctx, slot)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("look up backup slot: %w", err)
	}
	return slot, !claimed, nil
}

// dueSlotIfEnabled is the current slot when the schedule is on and the slot
// is due, otherwise nil.
func (s *Service) dueSlotIfEnabled(ctx context.Context, b settingsBlob) (*time.Time, error) {
	if !b.Enabled {
		return nil, nil
	}
	at, due, err := s.dueSlot(ctx, b)
	if err != nil || !due {
		return nil, err
	}
	return &at, nil
}

// RunDue is one tick of the schedule. It runs the current slot's backup
// when that is due, then prunes the scheduled backups (after a success
// only) and mails the instance administrators about a failure.
//
// Several managers may tick at once: the slot goes to the one whose attempt
// row claims it first. A tick that finds another backup running leaves the
// slot alone and tries again on the next tick. A tick with nothing due
// still marks stale running attempts abandoned.
//
// An abandoned attempt gives its slot back, so a backup cut short by a
// shutdown or a crash is taken again on a later tick, as long as its slot
// is still the current one; an earlier day's slot is never run again.
// Every tick ends by mailing the administrators about such an earlier slot
// that was only ever interrupted (notifyInterrupted).
func (s *Service) RunDue(ctx context.Context) error {
	b, err := s.loadBlob(ctx)
	if err != nil {
		return err
	}
	if err := s.runSlot(ctx, b); err != nil || ctx.Err() != nil {
		return err // a tick cut short by a shutdown ends with its backup
	}
	return s.notifyInterrupted(ctx, b)
}

// runSlot is RunDue's backup of the current slot under the settings b.
func (s *Service) runSlot(ctx context.Context, b settingsBlob) error {
	slot, err := s.dueSlotIfEnabled(ctx, b)
	if err != nil {
		return err
	}
	if slot == nil {
		// Nothing to run; the lock is only worth taking for a stale row.
		running, err := s.store.AnyRunning(ctx)
		if err != nil || !running {
			return err
		}
	}

	release, ok, err := s.store.TryLock(ctx)
	if err != nil {
		return fmt.Errorf("scheduled backup: %w", err)
	}
	if !ok {
		return nil // a backup is running; the slot waits for the next tick
	}
	held := true
	defer func() {
		if held {
			release()
		}
	}()
	if slot == nil {
		// Holding the lock, every running attempt is stale. Abandoning a
		// crashed scheduled attempt frees its slot, which may be the
		// current one.
		if err := s.abandon(ctx); err != nil {
			return err
		}
		if slot, err = s.dueSlotIfEnabled(ctx, b); err != nil || slot == nil {
			return err
		}
	}

	r, inserted, err := s.record(ctx, KindScheduled, slot, nil)
	if err != nil || !inserted {
		return err // !inserted: another manager took the slot
	}
	set := b.settings()
	secret, err := s.openSecret(b)
	var st Storage
	if err == nil {
		st, err = s.storageFor(set, secret)
		if err != nil {
			err = fmt.Errorf("backup destination: %w", err)
		}
	}
	var e Entry
	if err != nil {
		e, _ = s.finish(ctx, r, 0, err)
	} else {
		e, _ = s.execute(ctx, r, st, set.IncludeMetrics)
	}
	if e.State == StateSucceeded {
		s.prune(ctx, st, set, r.name)
	}
	release()
	held = false

	if e.State == StateFailed {
		s.notifyFailure(ctx, *slot, set, st, e.Error)
	}
	return nil
}

// prune deletes the scheduled backups the retention no longer keeps. Only
// backups whose header is authentic under this instance's header key take
// part, and the backup this run just stored (written) is never deleted; how
// many scheduled backups were left out as unverified is logged as a count.
// Failures are logged: the backup itself succeeded.
func (s *Service) prune(ctx context.Context, st Storage, set Settings, written string) {
	loc, err := location(set.TimeZone)
	if err != nil {
		loc = time.UTC
	}
	objs, err := st.List(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "prune backups: list", "err", err)
		return
	}
	MarkVerified(objs, s.headerKey)
	// Unverified scheduled objects (written before header MACs, or not by
	// this instance) never take part, so they would pile up unnoticed:
	// say how many there are, once per run.
	if n := unverifiedScheduled(objs); n > 0 {
		s.log.WarnContext(ctx, "prune backups: kept scheduled backups that are not verified", "count", n,
			"hint", "retention never deletes them; they stay until an administrator deletes them")
	}
	for _, name := range Prune(objs, set.Retention, loc, written, s.now()) {
		if err := st.Delete(ctx, name); err != nil {
			s.log.ErrorContext(ctx, "prune backups: delete", "name", name, "err", err)
			continue
		}
		s.log.InfoContext(ctx, "pruned scheduled backup", "name", name)
	}
}

// unverifiedScheduled counts the scheduled objects MarkVerified did not
// verify; the kind comes from the header, or from the name when the header
// is unreadable.
func unverifiedScheduled(objs []Object) int {
	n := 0
	for _, o := range objs {
		if _, kind, ok := o.created(); ok && kind == KindScheduled && !o.Verified {
			n++
		}
	}
	return n
}

// notifyFailure mails every instance administrator about a failed
// scheduled backup.
func (s *Service) notifyFailure(ctx context.Context, slot time.Time, set Settings, st Storage, reason string) {
	loc, err := location(set.TimeZone)
	if err != nil {
		loc = time.UTC
	}
	dest := "the backup folder"
	switch {
	case st != nil:
		dest = st.Describe()
	case set.Destination == DestinationS3:
		dest = "s3://" + set.S3.Bucket + "/" + set.S3.Prefix
	}
	body := fmt.Sprintf("The scheduled Pulsix backup of %s did not complete.\n\nDestination: %s\nError: %s\n\n"+
		"The next scheduled backup runs at the usual time. Until one succeeds, the newest backup is older than planned.\n",
		slot.In(loc).Format("2006-01-02 15:04 MST"), dest, reason)
	s.mailAdmins(ctx, "Pulsix backup failed", body)
}

// mailAdmins sends a backup mail to every instance administrator; a
// failure to send is logged.
func (s *Service) mailAdmins(ctx context.Context, subject, body string) {
	if err := s.mailer.SendToInstanceAdmins(ctx, subject, body); err != nil {
		s.log.ErrorContext(ctx, "mail instance administrators about a backup", "subject", subject, "err", err)
	}
}

// Ticker drives the schedule: one RunDue per tick.
type Ticker struct {
	s     *Service
	every time.Duration
}

// NewTicker builds the ticker; every <= 0 is DefaultTick.
func NewTicker(s *Service, every time.Duration) *Ticker {
	if every <= 0 {
		every = DefaultTick
	}
	return &Ticker{s: s, every: every}
}

// Run ticks until ctx ends, starting with one tick right away. A failed
// tick is logged and the next one tries again.
func (t *Ticker) Run(ctx context.Context) {
	tick := time.NewTicker(t.every)
	defer tick.Stop()
	for {
		t.once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// once runs one tick and keeps a panic in it from taking the manager down.
func (t *Ticker) once(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			t.s.log.ErrorContext(ctx, "backup tick panicked", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	if err := t.s.RunDue(ctx); err != nil && ctx.Err() == nil {
		t.s.log.ErrorContext(ctx, "backup tick failed", "err", err)
	}
}
