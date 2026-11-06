package requirements

// SetLockWaiting makes s call f whenever an Ensure finds the cache's lock
// held by another, before it waits (tests only).
func SetLockWaiting(s *Service, f func()) { s.lockWaiting = f }

// SetTouch makes s mark an entry used with f instead of Chtimes (tests
// only).
func SetTouch(s *Service, f func(name string) error) { s.touch = f }
