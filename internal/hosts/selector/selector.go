// Package selector implements Pulsix's tag-selector grammar: a small
// boolean expression over key/value tags, used both in memory (Matches,
// against a host's own tags) and translated to SQL (SQL, an EXISTS
// fragment over host_tags) so the same expression can filter hosts inside
// the database once monitoring groups need to (v0.4).
//
// Grammar (deliberately minimal — no `or`, no parentheses around
// sub-expressions, no negation of a whole condition beyond `!=`):
//
//	expr  := cond ("and" cond)*
//	cond  := key "=" value | key "!=" value
//	       | key "in" "(" value ("," value)* ")" | key "exists"
//	key   := [a-z0-9_.-]+
//	value := bare-word | '"' ... '"'
//
// Keywords ("and", "in", "exists") are matched case-insensitively; a key
// and every value are matched exactly, byte for byte, against tag data — a
// selector never itself lowercases or trims a value, since a tag value like
// "prod" and "Prod" are two different values as far as this grammar is
// concerned.
//
// The hand-written tokenizer lives in lexer.go, the recursive-descent
// parser in parser.go; this file holds the Selector type itself and its
// three consumer-facing operations (Matches, String, SQL).
package selector

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// keyPattern is the grammar's own key shape (not hosts.tagKeyPattern, which
// this package does not import — the two happen to agree today, and both
// exist because a selector's key and a tag's key are the same kind of
// string used in two different packages).
var keyPattern = regexp.MustCompile(`^[a-z0-9_.-]+$`)

// op names one condition's comparison.
type op int

const (
	opEq op = iota
	opNeq
	opIn
	opExists
)

// condition is one `key <op> value...` term. values holds exactly one
// element for opEq/opNeq, one or more for opIn (in the order written), and
// none for opExists.
type condition struct {
	key    string
	op     op
	values []string
}

// Selector is a parsed selector expression: every condition must match
// (there is no `or`) for the whole selector to match.
type Selector struct {
	conds []condition
}

// Matches reports whether every condition in s holds against tags. An empty
// or nil tags map simply has no keys, matching the same way a host with no
// tags of its own would.
func (s Selector) Matches(tags map[string]string) bool {
	for _, c := range s.conds {
		if !c.matches(tags) {
			return false
		}
	}
	return true
}

func (c condition) matches(tags map[string]string) bool {
	v, ok := tags[c.key]
	switch c.op {
	case opEq:
		return ok && v == c.values[0]
	case opNeq:
		// A tag that is simply absent counts as "not equal to" any value —
		// the same reading SQL() gives it (see (condition).sql's opNeq
		// case): "!=" means "this exact key=value pair is not currently
		// set", not "the key is set to something else".
		return !ok || v != c.values[0]
	case opIn:
		if !ok {
			return false
		}
		for _, want := range c.values {
			if v == want {
				return true
			}
		}
		return false
	case opExists:
		return ok
	default:
		return false
	}
}

// String renders s in canonical form: lowercase keywords, `in` values
// sorted and comma-separated, one space around every operator, conditions
// joined by " and " in the order they were written. Two selectors that mean
// the same thing but were written differently converge on it (e.g. an
// `in` list's original order and a source expression's exact case are both
// lost, deliberately) — see TestSelectorCanonicalRoundTrip and
// TestSelectorCanonicalString.
func (s Selector) String() string {
	parts := make([]string, len(s.conds))
	for i, c := range s.conds {
		parts[i] = c.string()
	}
	return strings.Join(parts, " and ")
}

func (c condition) string() string {
	switch c.op {
	case opEq:
		return c.key + " = " + formatValue(c.values[0])
	case opNeq:
		return c.key + " != " + formatValue(c.values[0])
	case opIn:
		sorted := append([]string(nil), c.values...)
		sort.Strings(sorted)
		formatted := make([]string, len(sorted))
		for i, v := range sorted {
			formatted[i] = formatValue(v)
		}
		return c.key + " in (" + strings.Join(formatted, ", ") + ")"
	case opExists:
		return c.key + " exists"
	default:
		return ""
	}
}

// formatValue quotes v only when it would not otherwise survive a re-parse
// unchanged: an empty value, or one containing whitespace or any of the
// grammar's own punctuation. Every other value is printed bare, so
// formatValue never quotes more than a round trip actually requires.
func formatValue(v string) string {
	if needsQuote(v) {
		return `"` + v + `"`
	}
	return v
}

func needsQuote(v string) bool {
	if v == "" {
		return true
	}
	for _, r := range v {
		if !isWordChar(r) {
			return true
		}
	}
	return false
}

// SQL renders s as a fragment of SQL suitable for AND-ing into a WHERE
// clause over a query that aliases the hosts table "h" — each condition
// becomes an EXISTS (or NOT EXISTS) sub-query against host_tags, aliased
// "t" and correlated to the outer host via t.host_id = h.id. args holds one
// placeholder value per $N the fragment references, in order.
//
// offset lets a caller append this fragment to a query that already has
// its own parameters: placeholders are numbered starting at $(offset+1)
// rather than always from $1, so SQL(0) is the ordinary case (a query with
// no parameters of its own yet) and e.g. SQL(1) is for a caller whose query
// already uses $1 for something else (see hosts.SQLStore.QueryHosts, which
// uses org_id as $1 and passes offset 1 here).
//
// This fragment is intentionally silent about a tag's source (manual vs.
// fact, see hosts.Tag): it matches any row in host_tags with the given key
// (and, for opEq/opNeq/opIn, value), the same way Matches matches any entry
// in whatever tags map it is given. A caller that wants the merged
// (manual-overrides-fact) view Matches expects when given a host's merged
// tags must arrange for "host_tags" to resolve to that merged view within
// its own query — see hosts.SQLStore.QueryHosts's own doc comment for how.
func (s Selector) SQL(offset int) (string, []any) {
	var args []any
	parts := make([]string, len(s.conds))
	for i, c := range s.conds {
		frag, condArgs := c.sql(offset + len(args))
		parts[i] = frag
		args = append(args, condArgs...)
	}
	return strings.Join(parts, " AND "), args
}

func (c condition) sql(offset int) (string, []any) {
	switch c.op {
	case opEq:
		return fmt.Sprintf("EXISTS (SELECT 1 FROM host_tags t WHERE t.host_id = h.id AND t.key = $%d AND t.value = $%d)",
			offset+1, offset+2), []any{c.key, c.values[0]}
	case opNeq:
		// "Not currently set to this value" covers both "key is absent" and
		// "key is set to a different value" — the same reading (matches)
		// gives opNeq above.
		return fmt.Sprintf("NOT EXISTS (SELECT 1 FROM host_tags t WHERE t.host_id = h.id AND t.key = $%d AND t.value = $%d)",
			offset+1, offset+2), []any{c.key, c.values[0]}
	case opIn:
		placeholders := make([]string, len(c.values))
		args := make([]any, 0, len(c.values)+1)
		args = append(args, c.key)
		for i, v := range c.values {
			placeholders[i] = fmt.Sprintf("$%d", offset+2+i)
			args = append(args, v)
		}
		return fmt.Sprintf("EXISTS (SELECT 1 FROM host_tags t WHERE t.host_id = h.id AND t.key = $%d AND t.value IN (%s))",
			offset+1, strings.Join(placeholders, ", ")), args
	case opExists:
		return fmt.Sprintf("EXISTS (SELECT 1 FROM host_tags t WHERE t.host_id = h.id AND t.key = $%d)", offset+1), []any{c.key}
	default:
		return "", nil
	}
}
