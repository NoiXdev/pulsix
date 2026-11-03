package backup

import (
	"context"
	"fmt"
	"time"
)

// notifyInterrupted mails the instance administrators about every past
// scheduled slot whose attempts were all interrupted: the manager stopped
// during each of them, and none succeeded or failed (a failure has its own
// mail, notifyFailure).
//
// Past means before the current slot. RunDue only ever runs the current
// slot, so a slot before it is never retried, while the current slot may
// still be until the next one starts; mailing about it earlier could tell
// of a backup that is about to be taken after all. This holds whether or
// not the schedule is still enabled, and across a changed time of day: a
// slot is mailed as soon as the current slot lies after it.
//
// Each slot is mailed at most once. The slot's newest attempt is marked
// notified before the mail goes out, and only the manager whose mark took
// sends it; a mail that then fails to send is logged, not retried.
func (s *Service) notifyInterrupted(ctx context.Context, b settingsBlob) error {
	loc, err := location(b.TimeZone)
	if err != nil {
		return err
	}
	current := SlotFor(s.now(), b.TimeOfDay, loc)
	slots, err := s.store.InterruptedSlots(ctx, current)
	if err != nil {
		return fmt.Errorf("look up interrupted backup slots: %w", err)
	}
	for _, slot := range slots {
		marked, err := s.store.MarkNotified(ctx, slot.NewestAttempt, s.now().UTC())
		if err != nil {
			return fmt.Errorf("mark interrupted backup slot notified: %w", err)
		}
		if !marked {
			continue // another manager sent this mail
		}
		s.mailAdmins(ctx, "Pulsix backup interrupted", interruptedBody(slot.Slot, loc))
	}
	return nil
}

// interruptedBody is the mail about an interrupted slot, its time read in
// the schedule's zone.
func interruptedBody(slot time.Time, loc *time.Location) string {
	return fmt.Sprintf("The scheduled Pulsix backup of %s did not complete: the manager was stopped during it, "+
		"and it was not taken again that day.\n\n"+
		"The next scheduled backup runs at the usual time. Until one succeeds, the newest backup is older than planned.\n",
		slot.In(loc).Format("2006-01-02 15:04 MST"))
}
