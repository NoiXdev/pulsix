package secrets

import (
	"cmp"
	"slices"
	"strings"
)

// RedactMinLen is the shortest secret value the Redactor replaces. Shorter
// values (a port, "yes") would mangle nearly every line they meet.
const RedactMinLen = 4

// Redacted is what a secret value is replaced with.
const Redacted = "[secret]"

// Redactor replaces the field values of a set's secrets with Redacted
// (owner decision 4 of backlog 3.132): in what the agent sends and in the
// log lines its status page shows. It holds the values and hands none of
// them out. A nil Redactor replaces nothing. It is safe for concurrent
// use.
type Redactor struct {
	r *strings.Replacer
}

// newRedactor builds the redactor of secrets' field values of at least
// RedactMinLen bytes, longest first, so a value that contains a shorter
// one is replaced whole. Nil when there is nothing to replace.
func newRedactor(secrets map[string]map[string]string) *Redactor {
	seen := map[string]bool{}
	var values []string
	for _, fields := range secrets {
		for _, v := range fields {
			if len(v) >= RedactMinLen && !seen[v] {
				seen[v] = true
				values = append(values, v)
			}
		}
	}
	if len(values) == 0 {
		return nil
	}
	slices.SortFunc(values, func(a, b string) int {
		if c := cmp.Compare(len(b), len(a)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	pairs := make([]string, 0, 2*len(values))
	for _, v := range values {
		pairs = append(pairs, v, Redacted)
	}
	return &Redactor{r: strings.NewReplacer(pairs...)}
}

// Redact returns s with every secret value replaced.
func (r *Redactor) Redact(s string) string {
	if r == nil || s == "" {
		return s
	}
	return r.r.Replace(s)
}

// String never shows the values.
func (r *Redactor) String() string { return "secrets.Redactor" }

// GoString is String for %#v.
func (r *Redactor) GoString() string { return r.String() }
