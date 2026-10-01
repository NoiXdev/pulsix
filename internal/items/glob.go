// Package items implements discovery and item decisions (v0.3b-3 design,
// "Resolving a decision" and "Decisions"): a small glob over item names,
// Decide, the strict total order that picks which decision — if any —
// governs one (host, check_key, item), and the Service that validates and
// previews decisions before they are stored.
//
// items must import neither internal/rules nor internal/checks, and must
// not ever — that is the whole point of this package: evaluation needs
// Decide to resolve the ignore decision it holds while already inside
// internal/rules, over internal/items' own types (Target, Resolution), so
// the edge internal/rules -> internal/items must stay free to exist. A
// leaf both of them may depend on is what makes that possible; the moment
// this package imports either, it stops being a leaf and that edge
// becomes a cycle the compiler refuses. This is also why Service does not
// know the metric catalogue itself: the set of valid check keys a
// decision's check_key may name is a required constructor argument
// (NewService's own doc comment), supplied by whoever wires this package
// up — internal/app, from rules.Catalogue() — not looked up here.
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
