package repos

import (
	"encoding/json"
	"fmt"
)

// ValidateURL is urlProblem's verdict as an error wrapping ErrInvalidInput,
// for the tests of the URL rules. Production callers go through the
// service, or URLProblem.
func ValidateURL(raw string) error {
	if reason, _ := urlProblem(raw); reason != "" {
		return fmt.Errorf("%w: %s", ErrInvalidInput, reason)
	}
	return nil
}

// DecodeReport is decodeReport's verdict on a worker's report, for the
// tests of the report checks.
func DecodeReport(raw json.RawMessage) error {
	_, _, err := decodeReport(raw)
	return err
}

// MaxStoredResultReadBytes is the longest stored playbook result a read
// decodes, for the test of the read guard.
const MaxStoredResultReadBytes = maxStoredResultReadBytes
