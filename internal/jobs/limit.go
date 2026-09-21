// The template limit's enqueue gate (backlog 2.45): which host names a
// limit pattern decides to match. A template limit is free-form — passed
// to ansible-playbook verbatim, only length and NUL are validated — so the
// matcher implements the host pattern classes that matter (literal names,
// fnmatch-style globs, exclusions, unions) and refuses to decide for
// everything else: an undecidable pattern never drops a zone's job, the
// caller fails open and ansible still applies the real limit at claim
// time. Matching is case-sensitive, like ansible's.
package jobs

import (
	"path"
	"strings"
)

// limitTerm is one ':'- or ','-separated term of a limit: a positive name
// or glob, or its exclusion.
type limitTerm struct {
	exclude bool
	pat     string
}

// limitPattern is a parsed limit. decided is false when any term is outside
// the supported grammar — the whole pattern is then undecidable and the
// caller keeps every zone's job.
type limitPattern struct {
	terms   []limitTerm
	decided bool
}

// parseLimit splits a limit into terms. ':' is the union separator the
// backlog decision names; ',' is ansible's other union separator and a
// template limit (free-form) may use it, so both split — a comma-joined
// name treated as one literal could never match a real host name and would
// wrongly drop zones. An empty limit is no limit: it parses to no terms and
// matches everything.
func parseLimit(limit string) limitPattern {
	p := limitPattern{decided: true}
	for _, raw := range strings.FieldsFunc(limit, func(r rune) bool { return r == ':' || r == ',' }) {
		term := limitTerm{pat: raw}
		if strings.HasPrefix(raw, "!") {
			term.exclude, term.pat = true, raw[1:]
		}
		p.terms = append(p.terms, term)
		p.decided = p.decided && validGlob(term.pat)
	}
	return p
}

// matches reports whether name matches p, and whether p could decide it at
// all. A host matches when at least one positive term (a literal name or a
// glob on the host name) matches it and no exclusion matches it; an
// exclusions-only limit ("!web3") matches every host except the excluded
// ones, mirroring ansible's semantics. An undecidable pattern — or one
// whose execution fails anyway (defense against a pattern Go's parser
// reads differently than fnmatch) — reports decided=false; the caller keeps
// the zone's job (fail-open) and the limit string still ships on the job
// payload for ansible to decide at runtime.
func (p limitPattern) matches(name string) (matched, decided bool) {
	if !p.decided {
		return false, false
	}
	hasPositive, positive := false, false
	for _, t := range p.terms {
		m, err := path.Match(t.pat, name)
		if err != nil {
			return false, false
		}
		if t.exclude {
			if m {
				return false, true
			}
			continue
		}
		hasPositive = true
		if m {
			positive = true
		}
	}
	return positive || !hasPositive, true
}

// validGlob reports whether pat is a pattern path.Match can carry the
// fnmatch semantics of: name characters (a-z A-Z 0-9 . _ -), the '*'/ '?'
// wildcards and plain '[...]'-classes. Ansible's remaining operators and
// escape hatches — '~' regex, '@' file lists, '&' intersection, '{a,b}'
// brace expansion, whitespace, backslash escapes, '!' anywhere but an
// exclusion's head, negated classes — leave the term undecidable instead of
// guessing.
func validGlob(pat string) bool {
	for i := 0; i < len(pat); {
		switch c := pat[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
			i++
		case c == '*' || c == '?':
			i++
		case c == '[':
			i++
			// Negated classes are undecidable: fnmatch negates with '!'
			// where Go's path.Match reads it as a literal class member and
			// negates '^' where fnmatch keeps it literal — either spelling
			// would decide the wrong thing for one of the two.
			if i < len(pat) && (pat[i] == '!' || pat[i] == '^') {
				return false
			}
			start := i
			for i < len(pat) && pat[i] != ']' {
				if !classChar(pat[i]) {
					return false
				}
				i++
			}
			// Unterminated, or a ']' as the first content character: the
			// two forms Go's path.Match parses differently from fnmatch.
			if i >= len(pat) || i == start {
				return false
			}
			i++
		default:
			return false
		}
	}
	return true
}

// classChar bounds a class's contents to name characters and '-', so the
// class Go's path.Match builds cannot diverge from fnmatch's.
func classChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
}
