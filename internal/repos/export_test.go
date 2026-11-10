package repos

import "fmt"

// ValidateURL is urlProblem's verdict as an error wrapping ErrInvalidInput,
// for the tests of the URL rules. Production callers go through the
// service, or URLProblem.
func ValidateURL(raw string) error {
	if reason, _ := urlProblem(raw); reason != "" {
		return fmt.Errorf("%w: %s", ErrInvalidInput, reason)
	}
	return nil
}
