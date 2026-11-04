package accounts

import "context"

// RequestResetNow runs RequestReset's background work in the caller's
// goroutine and returns its error.
func RequestResetNow(s *Service, ctx context.Context, email string) error {
	return s.requestReset(ctx, email)
}
