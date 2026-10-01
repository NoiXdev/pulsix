// Package items implements discovery and item decisions (v0.3b-3 design,
// "Resolving a decision" and "Decisions"): a small glob over item names,
// Decide, the strict total order that picks which decision — if any —
// governs one (host, check_key, item), and the Service that validates and
// previews decisions before they are stored.
//
// internal/rules must not import internal/checks, and evaluation needs the
// ignore decision; a leaf package both of them may depend on resolves that
// tension without a third adapter layer between the two. items therefore
// does not import internal/checks, and must not ever. It does import
// internal/rules — deliberately, for the one thing Service.go needs from
// it, the catalogue of check keys a decision's check_key may name — which
// is safe only because the dependency runs one way: internal/rules does
// not import internal/items (`go list -deps ./internal/rules/...` carries
// no path back here). The cycle to watch for, before adding any further
// import to this package, is exactly that one reversed: anything that
// would make internal/rules (or something it depends on) import
// internal/items.
package items

import (
	"errors"
	"fmt"
	"strings"
)

// Match reports whether pattern matches item. '*' matches any run of
// characters, including none; every other character is a literal; the
// match is anchored at both ends. That is the whole syntax — no `?`, no
// character classes, no escaping, so a character that would be a
// metacharacter in a richer syntax (`.`, `?`, `[`, `\`, `%`, `_`) matches
// only itself.
//
// Implemented by splitting pattern on '*' and walking the literal segments
// in order: the first must prefix item, the last must suffix what remains,
// and the middle ones must each occur in sequence after that. There is no
// backtracking — a middle literal is matched at its first occurrence, never
// reconsidered — so a pattern of a hundred stars still costs only linear
// time, the reason this is a dozen lines of literal walking rather than a
// regular expression.
func Match(pattern, item string) bool {
	segments := strings.Split(pattern, "*")
	if len(segments) == 1 {
		return item == pattern
	}

	first := segments[0]
	if !strings.HasPrefix(item, first) {
		return false
	}
	item = item[len(first):]

	middle := segments[1 : len(segments)-1]
	last := segments[len(segments)-1]
	for _, m := range middle {
		if m == "" {
			continue // adjacent stars contribute nothing to search for
		}
		idx := strings.Index(item, m)
		if idx == -1 {
			return false
		}
		item = item[idx+len(m):]
	}
	return strings.HasSuffix(item, last)
}

// MaxPatternLen bounds a pattern so neither the matcher nor a LIKE
// translation can be handed something unbounded.
const MaxPatternLen = 512

// ErrInvalidInput means pattern itself is malformed: empty, or longer than
// MaxPatternLen. The same sentinel shape every other domain service in this
// codebase uses (see e.g. rules.ErrInvalidInput's own doc comment).
var ErrInvalidInput = errors.New("invalid input")

// ValidPattern refuses an empty pattern and one past MaxPatternLen. It does
// not otherwise constrain the pattern — any string of literals and stars
// within that bound is valid, including one that is only stars.
func ValidPattern(p string) error {
	if p == "" {
		return fmt.Errorf("%w: pattern must not be empty", ErrInvalidInput)
	}
	if len(p) > MaxPatternLen {
		return fmt.Errorf("%w: pattern exceeds %d characters", ErrInvalidInput, MaxPatternLen)
	}
	return nil
}
