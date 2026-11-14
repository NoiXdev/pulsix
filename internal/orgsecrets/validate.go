package orgsecrets

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// Limits of a secret (spec 2026-11-14-snmp-checks, "Secret store on the
// manager"): the same names as agent.yaml's secrets, 1 to 16 fields.
const (
	maxFields     = 16
	maxValueBytes = 4096
)

// Problem texts. None of them carries anything the caller sent: a name or
// field name that fails its pattern could be a value pasted into the
// wrong box, and a value is never repeated at all.
const (
	badName      = "a secret name is 1 to 64 characters of A-Z a-z 0-9 _ . -"
	badFieldName = "a field name is 1 to 64 characters of A-Z a-z 0-9 _ . -"
)

// validName reports whether name is a secret's or a field's name: the
// agent.yaml pattern, checkplugins' secretName format.
func validName(name string) bool { return checkplugins.FormatSecretName.Valid(name) }

// validate checks a Put. Errors wrap ErrInvalidInput and name a field only
// once its name passed the pattern. Fields are checked in name order, so
// the same request always gets the same answer.
func validate(name string, fields map[string]string) error {
	if !validName(name) {
		return fmt.Errorf("%w: %s", ErrInvalidInput, badName)
	}
	if len(fields) == 0 || len(fields) > maxFields {
		return fmt.Errorf("%w: a secret has 1 to %d fields", ErrInvalidInput, maxFields)
	}
	for _, field := range slices.Sorted(maps.Keys(fields)) {
		if !validName(field) {
			return fmt.Errorf("%w: %s", ErrInvalidInput, badFieldName)
		}
		if problem := valueProblem(fields[field]); problem != "" {
			return fmt.Errorf("%w: field %s: %s", ErrInvalidInput, field, problem)
		}
	}
	return nil
}

// valueProblem describes what is wrong with a field's value, never the
// value itself; "" when it is fine.
func valueProblem(value string) string {
	switch {
	case value == "":
		return "the value is empty"
	case len(value) > maxValueBytes:
		return fmt.Sprintf("the value is longer than %d bytes", maxValueBytes)
	case !utf8.ValidString(value):
		return "the value is not valid UTF-8"
	case strings.ContainsRune(value, 0):
		return "the value contains a NUL character"
	}
	return ""
}
