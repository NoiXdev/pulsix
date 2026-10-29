//go:build !linux

package supervisor

import (
	"errors"
	"syscall"
)

// newSystem refuses: the supervisor stands on a child subreaper and pidfds,
// which only Linux has. The worker never starts the supervisor elsewhere
// (worker.Load leaves Config.PlaySupervisor empty off Linux), so reaching
// this is a wiring bug, and Main says so by name.
func newSystem() (system, error) {
	return nil, errors.New("the play supervisor needs Linux")
}

// raise is never reached off Linux, since newSystem refuses first.
func raise(sig syscall.Signal) error {
	return errors.New("the play supervisor needs Linux")
}
