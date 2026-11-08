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
		{"an @ in the query stays", "https://h/x?to=a@b", "https://h/x?to=a@b"},
		{"a user:password look-alike is over-redacted", "mailto:a@b.c", "[redacted]@b.c"},
		{"no url", "no url here", "no url here"},
		{"an @ in the password", "https://user@dom:pass@h/", "https://[redacted]@[redacted]@h/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Redact(c.in); got != c.want {
				t.Errorf("Redact(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
