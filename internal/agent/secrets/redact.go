package secrets

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
)

// RedactMinLen is the shortest secret value the Redactor replaces. Shorter
// values (a port, "yes") would mangle nearly every line they meet.
const RedactMinLen = 4

// Redacted is what a secret value is replaced with.
const Redacted = "[secret]"

// nonSecretFields are the secret field names whose values are connection
// settings, not secrets, and are therefore not replaced (owner decision of
// 2026-11-27, backlog 3.132 fix round 1). This is the only list: a field
// not named here — password, basic_auth_password, a field no plugin knows
// — counts as secret. The owner's list is host, port, user, username,
// database, socket, tls_server_name and the file paths tls_ca, tls_cert and
// tls_key; the rest are the other plainly non-secret fields the agent's
// plugins read (R13 of the 3.132 plan): mysql's and redis's tls mode,
// mysql's pipe name, postgres's sslmode and its certificate file paths
// sslrootcert, sslcert and sslkey, and the web checks' basic_auth_user.
var nonSecretFields = map[string]bool{
	"host": true, "port": true, "user": true, "username": true, "database": true, "socket": true,
	"tls_server_name": true, "tls_ca": true, "tls_cert": true, "tls_key": true,
	"tls": true, "pipe": true, "sslmode": true, "sslrootcert": true, "sslcert": true, "sslkey": true,
	"basic_auth_user": true,
}

// IsSecretField reports whether the Redactor replaces a field's value.
func IsSecretField(name string) bool { return !nonSecretFields[name] }

// Redactor replaces the values of a set's secret fields with Redacted
// (owner decision 4 of backlog 3.132, narrowed on 2026-11-27 to the fields
// IsSecretField names): in what the agent sends and in the log lines its
// status page shows. It holds the values and hands none of them out. A nil
// Redactor replaces nothing. It is safe for concurrent use.
type Redactor struct {
	r *strings.Replacer
	// values are the raw values, longest first, for RedactTail.
	values []string
}

func longestFirst(a, b string) int {
	if c := cmp.Compare(len(b), len(a)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

// newRedactor builds the redactor of secrets' secret field values of at
// least RedactMinLen bytes, longest first, so a value that contains a
// shorter one is replaced whole. Each value is also replaced in its quoted
// form (strconv.Quote without the outer quotes), which is how a log line
// writes a value holding a quote or a backslash. Nil when there is
// nothing to replace.
func newRedactor(secrets map[string]map[string]string) *Redactor {
	seen := map[string]bool{}
	var values []string
	for _, fields := range secrets {
		for name, v := range fields {
			if len(v) >= RedactMinLen && IsSecretField(name) && !seen[v] {
				seen[v] = true
				values = append(values, v)
			}
		}
	}
	if len(values) == 0 {
		return nil
	}
	slices.SortFunc(values, longestFirst)
	patterns := slices.Clone(values)
	for _, v := range values {
		q := strconv.Quote(v)
		if q = q[1 : len(q)-1]; !seen[q] {
			seen[q] = true
			patterns = append(patterns, q)
		}
	}
	slices.SortFunc(patterns, longestFirst)
	pairs := make([]string, 0, 2*len(patterns))
	for _, v := range patterns {
		pairs = append(pairs, v, Redacted)
	}
	return &Redactor{r: strings.NewReplacer(pairs...), values: values}
}

// Redact returns s with every secret value replaced.
func (r *Redactor) Redact(s string) string {
	if r == nil || s == "" {
		return s
	}
	return r.r.Replace(s)
}

// RedactTail is Redact for a text that was cut to a length bound: a value
// the cut went through left only its start, which Redact cannot see, so a
// trailing fragment of RedactMinLen bytes or more that a secret value
// starts with is removed as well.
func (r *Redactor) RedactTail(s string) string {
	if r == nil {
		return s
	}
	s = r.Redact(s)
	for _, v := range r.values {
		for k := min(len(v)-1, len(s)); k >= RedactMinLen; k-- {
			if strings.HasSuffix(s, v[:k]) {
				return s[:len(s)-k]
			}
		}
	}
	return s
}

// String never shows the values.
func (r *Redactor) String() string { return "secrets.Redactor" }

// GoString is String for %#v.
func (r *Redactor) GoString() string { return r.String() }
