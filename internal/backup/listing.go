package backup

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"
)

// List returns the backups at the configured destination merged with the
// recent attempts, newest first. A stored backup is listed once, with its
// attempt's actor when there is one; an attempt that left no backup
// (running, failed, abandoned) is listed on its own; a succeeded attempt
// whose backup was deleted since is not listed.
//
// When the destination cannot be listed, List still returns the attempts,
// with an error wrapping ErrStorageUnavailable.
func (s *Service) List(ctx context.Context) ([]Entry, error) {
	attempts, err := s.store.RecentAttempts(ctx, recentAttempts)
	if err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}
	var objs []Object
	st, listErr := s.destination(ctx)
	if listErr == nil {
		objs, listErr = st.List(ctx)
	}
	entries := merge(attempts, objs)
	if listErr != nil {
		return entries, fmt.Errorf("%w: %w", ErrStorageUnavailable, listErr)
	}
	return entries, nil
}

// merge joins attempts and stored objects by object name.
func merge(attempts []Attempt, objs []Object) []Entry {
	stored := make(map[string]Object, len(objs))
	for _, o := range objs {
		stored[o.Name] = o
	}
	listed := make(map[string]bool, len(objs))
	var out []Entry
	for _, a := range attempts {
		if o, ok := stored[a.ObjectName]; ok {
			if !listed[o.Name] {
				listed[o.Name] = true
				e := objectEntry(o)
				e.Actor, e.ActorName = a.Actor, a.ActorName
				out = append(out, e)
			}
			continue
		}
		if a.State == StateSucceeded {
			continue // its backup has been deleted since
		}
		out = append(out, Entry{
			Name: a.ObjectName, CreatedAt: a.StartedAt, Kind: a.Kind, Actor: a.Actor, ActorName: a.ActorName,
			State: a.State, Error: a.Error,
		})
	}
	for _, o := range objs {
		if !listed[o.Name] {
			out = append(out, objectEntry(o))
		}
	}
	slices.SortStableFunc(out, func(a, b Entry) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(b.Name, a.Name)
	})
	return out
}

// objectEntry describes a stored object from its header, or from its name
// when the header cannot be read.
func objectEntry(o Object) Entry {
	if o.HeaderErr != nil {
		at, kind, _ := ParseObjectName(o.Name)
		return Entry{Name: o.Name, CreatedAt: at, Kind: kind, Size: o.Size, State: StateDamaged, Error: o.HeaderErr.Error()}
	}
	return Entry{
		Name: o.Name, CreatedAt: o.Header.CreatedAt, Kind: o.Header.Kind, PulsixVersion: o.Header.PulsixVersion,
		Size: o.Size, State: StateSucceeded,
	}
}

// find returns the listed backup called name at the configured destination.
func (s *Service) find(ctx context.Context, name string) (Storage, Object, error) {
	st, err := s.destination(ctx)
	if err != nil {
		return nil, Object{}, err
	}
	objs, err := st.List(ctx)
	if err != nil {
		return nil, Object{}, fmt.Errorf("list backups: %w", err)
	}
	for _, o := range objs {
		if o.Name == name {
			return st, o, nil
		}
	}
	return nil, Object{}, fmt.Errorf("%w: %s", ErrNotFound, name)
}

// Delete removes the backup called name from the configured destination.
func (s *Service) Delete(ctx context.Context, name string) error {
	st, _, err := s.find(ctx, name)
	if err != nil {
		return err
	}
	if err := st.Delete(ctx, name); err != nil {
		return fmt.Errorf("delete backup %s: %w", name, err)
	}
	return nil
}

// Open returns the encrypted bytes of the backup called name and its size.
func (s *Service) Open(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	st, o, err := s.find(ctx, name)
	if err != nil {
		return nil, 0, err
	}
	rc, err := st.Open(ctx, name)
	if errors.Is(err, ErrObjectNotFound) {
		return nil, 0, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("open backup %s: %w", name, err)
	}
	return rc, o.Size, nil
}

// Status returns when the next scheduled backup runs (nil while the
// schedule is off) and a label for the configured destination.
func (s *Service) Status(ctx context.Context) (nextRun *time.Time, destination string, err error) {
	b, err := s.loadBlob(ctx)
	if err != nil {
		return nil, "", err
	}
	st, err := s.destination(ctx)
	if err != nil {
		return nil, "", err
	}
	if !b.Enabled {
		return nil, st.Describe(), nil
	}
	slot, due, err := s.dueSlot(ctx, b)
	if err != nil {
		return nil, "", err
	}
	if !due {
		loc, err := location(b.TimeZone)
		if err != nil {
			return nil, "", err
		}
		slot = nextSlotAfter(s.now(), b.TimeOfDay, loc)
	}
	return &slot, st.Describe(), nil
}
