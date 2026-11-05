// The limit matcher's pattern classes (backlog 2.45): a template limit is
// free-form (only length and NUL are validated), so the matcher must decide
// for every string a host pattern class it understands — and refuse to
// decide (fail-open) for everything else.
package jobs

import "testing"

func TestLimitPatternMatches(t *testing.T) {
	cases := []struct {
		limit   string
		name    string
		matched bool
		decided bool
	}{
		// An empty limit is no limit: everything matches.
		{"", "web1", true, true},

		// Literal names, case-sensitive.
		{"web1", "web1", true, true},
		{"web1", "web2", false, true},
		{"Web1", "web1", false, true},

		// fnmatch-style globs on the name.
		{"web*", "web1", true, true},
		{"web*", "db1", false, true},
		{"*", "db1", true, true},
		{"web?", "web1", true, true},
		{"web?", "web12", false, true},
		// Character classes, plain (fnmatch and Go agree on them).
		{"[wh]eb*", "web1", true, true},
		{"[a-z]*", "web1", true, true},
		{"[a-z]*", "Web1", false, true},
		// A reversed range is an empty class: no match, but decided.
		{"[z-a]1", "a1", false, true},

		// Negated classes are undecidable: fnmatch negates with '!' and
		// Go's path.Match with '^', each reading the other's marker as a
		// literal class member — either would decide wrongly.
		{"[!w]eb1", "web1", false, false},
		{"[^w]eb1", "web1", false, false},

		// ':'- and ','-separated unions: any positive term matches.
		{"web1:db1", "db1", true, true},
		{"web1,db1", "db1", true, true},
		{"web1:db1", "mail1", false, true},

		// Exclusions subtract from the union.
		{"web*:!web3", "web3", false, true},
		{"web*:!web3", "web4", true, true},
		{"*:!web3", "web3", false, true},

		// An exclusions-only limit matches every host except the excluded
		// ones (ansible's "!web3" limit = all hosts but web3).
		{"!web3", "web3", false, true},
		{"!web3", "web1", true, true},
		{"!", "web1", true, true},

		// Terms the matcher cannot parse leave the decision undecidable —
		// the caller keeps the zone's job and ansible decides at runtime.
		{"~web.*", "web1", false, false},
		{"@hosts", "web1", false, false},
		{"web*&db*", "web1", false, false},
		{"web 1", "web1", false, false},
		{"web[1", "web1", false, false},
		{"web\\1", "web1", false, false},
		{"web!x", "web1", false, false},
		{"{a,b}", "a", false, false},

		// A limit of only union separators is no limit at all: it decides
		// nothing and matches everything (fail-open like an empty limit).
		{":", "web1", true, true},
		{",,", "web1", true, true},
		// A trailing separator is dropped, the real terms stay.
		{"web1:", "web1", true, true},
		{"web1:", "db1", false, true},
	}
	for _, c := range cases {
		p := parseLimit(c.limit)
		matched, decided := p.matches(c.name)
		if matched != c.matched || decided != c.decided {
			t.Errorf("parseLimit(%q).matches(%q) = (%v, %v), want (%v, %v)",
				c.limit, c.name, matched, decided, c.matched, c.decided)
		}
	}
}

// TestNarrowLimit pins the limit a run narrowed to one host ships (backlog
// 7.76): the host name alone without an effective limit, else Ansible's
// intersection of the effective limit with the host name, joined with the
// separator the effective limit itself uses — Ansible splits a pattern
// holding a comma on commas only, so ":&" after a comma list would glue
// the intersection onto its last term.
func TestNarrowLimit(t *testing.T) {
	cases := []struct{ effective, host, want string }{
		{"", "web-01", "web-01"},
		{"web*", "web-01", "web*:&web-01"},
		{"web*:!web-03", "web-01", "web*:!web-03:&web-01"},
		{"web*,db*", "web-01", "web*,db*,&web-01"},
		{"!web-03", "web-01", "!web-03:&web-01"},
	}
	for _, c := range cases {
		if got := narrowLimit(c.effective, c.host); got != c.want {
			t.Errorf("narrowLimit(%q, %q) = %q, want %q", c.effective, c.host, got, c.want)
		}
	}
}
