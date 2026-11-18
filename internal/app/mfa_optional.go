package app

import "log/slog"

// mfaOptionalWarning is the startup warning Build logs while
// PULSIX_MFA_OPTIONAL is on: accounts without a second factor sign in with
// their password alone.
const mfaOptionalWarning = "second factor is optional (PULSIX_MFA_OPTIONAL): do not use this on a production instance"

// warnMFAOptional logs mfaOptionalWarning.
func warnMFAOptional(log *slog.Logger) {
	log.Warn(mfaOptionalWarning)
}
