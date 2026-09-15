package sshprobe

import "errors"

// isHopFailure reports whether err is a jump chain's own failure (wrapped
// in hopError by dial) rather than an outcome at the target itself.
func isHopFailure(err error) bool {
	var hop *hopError
	return errors.As(err, &hop)
}
