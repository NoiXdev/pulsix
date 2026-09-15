// Redaction of credential-bearing URLs from git's diagnostic tails: git
// echoes URLs verbatim on failure ("fatal: unable to access
// 'https://user:token@host/…'"), so a stderr tail that lands in the job's
// error text must never carry them.
package runner

import "testing"

func TestRedactURLs(t *testing.T) {
	const url = "https://user:secret-token@example.invalid/x.git"
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"the repo url goes entirely",
			"fatal: unable to access 'https://user:secret-token@example.invalid/x.git/': Could not resolve host",
			"fatal: unable to access '[repo url]/': Could not resolve host",
		},
		{
			"userinfo of other urls is redacted, scheme kept",
			"fatal: Authentication failed for 'https://deploy:hunter2@example.org/repo.git/'",
			"fatal: Authentication failed for 'https://[redacted]@example.org/repo.git/'",
		},
		{
			"ssh userinfo with password",
			"remote: ssh://deploy:secret@example.org:2222/x.git not found",
			"remote: ssh://[redacted]@example.org:2222/x.git not found",
		},
		{
			"scp-like form with password",
			"fatal: could not read from remote repository: deploy:secret@host.example.org:repo",
			"fatal: could not read from remote repository: [redacted]@host.example.org:repo",
		},
		{
			"user-only userinfo stays",
			"fatal: unable to access 'ssh://git@github.com/org/repo.git/': host unreachable",
			"fatal: unable to access 'ssh://git@github.com/org/repo.git/': host unreachable",
		},
		{
			"no credentials, unchanged",
			"fatal: unable to access 'https://example.invalid/x.git/': no route",
			"fatal: unable to access 'https://example.invalid/x.git/': no route",
		},
		{
			"the url appears mid-line",
			"warning: retrying against https://user:secret-token@example.invalid/x.git (attempt 2)",
			"warning: retrying against [repo url] (attempt 2)",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := redactURLs(c.in, url); got != c.want {
				t.Errorf("redactURLs =\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

func TestRedactURLsWithEmptyURL(t *testing.T) {
	// No repo URL to redact: only the userinfo pattern applies.
	got := redactURLs("fatal: unable to access 'https://u:p@host/x'", "")
	if got != "fatal: unable to access 'https://[redacted]@host/x'" {
		t.Errorf("redactURLs = %q", got)
	}
}
