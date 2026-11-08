package urlcred

import "testing"

func TestRedact(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"a token as a bare username", "https://ghp_abc123@github.com/o/r.git", "https://[redacted]@github.com/o/r.git"},
		{"a compound scheme", "git+https://tok@h/r", "git+https://[redacted]@h/r"},
		{"an upper-case scheme", "HTTPS://TOK@h/r", "HTTPS://[redacted]@h/r"},
		{"user and password", "https://u:p@h/x", "https://[redacted]@h/x"},
		{"an ssh user", "ssh://git@h/x", "ssh://[redacted]@h/x"},
		{"user:password in an scp-style address", "deploy:secret@host.example.org:repo", "[redacted]@host.example.org:repo"},
		{"an scp-style user stays", "git@github.com:o/r.git", "git@github.com:o/r.git"},
		{"an @ in the query is over-redacted", "https://h/x?to=a@b", "https://[redacted]@b"},
		{"an @ in the path is over-redacted", "https://registry.example/@scope/pkg", "https://[redacted]@scope/pkg"},
		// Backlog 7.98: a credential may hold an unencoded /, ? or #.
		{"a slash in the password", "https://user:ab/cd@host/r.git", "https://[redacted]@host/r.git"},
		{"a query and a fragment mark in the password", "https://user:a?b#c@host/r.git", "https://[redacted]@host/r.git"},
		{"a base64 token as a bare username", "https://abc/def+g==@host/r.git", "https://[redacted]@host/r.git"},
		{"in quotes in a git error", "fatal: unable to access 'https://tok/en@host/r.git/': 403", "fatal: unable to access 'https://[redacted]@host/r.git/': 403"},
		{"two urls on a line", "from https://a:b/c@h1/x to https://d/e@h2/y", "from https://[redacted]@h1/x to https://[redacted]@h2/y"},
		{"a slash in an scp-style password", "deploy:se/cret@host.example.org:repo", "[redacted]@host.example.org:repo"},
		{"a url without userinfo stays", "https://github.com/o/r.git", "https://github.com/o/r.git"},
		{"a port without userinfo stays", "https://h:8443/r.git", "https://h:8443/r.git"},
		{"a user:password look-alike is over-redacted", "mailto:a@b.c", "[redacted]@b.c"},
		{"no url", "no url here", "no url here"},
		{"an @ in the password", "https://user@dom:pass@h/", "https://[redacted]@h/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Redact(c.in); got != c.want {
				t.Errorf("Redact(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
