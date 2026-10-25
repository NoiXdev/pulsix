// Redaction of credential-bearing URLs from git's diagnostic tails: git
// echoes URLs verbatim on failure ("fatal: unable to access
// 'https://user:token@host/…'"), so a stderr tail that lands in the job's
// error text must never carry them.
package runner

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

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

// TestGitDiagnosticRedactsBeforeTruncating: a git failure's stderr tail is
// bounded to 4 KiB, and the redaction must see the WHOLE credential-bearing
// URL: truncating first can cut "user:token@" so the pattern no longer
// matches and the token's tail survives.
func TestGitDiagnosticRedactsBeforeTruncating(t *testing.T) {
	const url = "https://deploy:tok-ABCDEFGHIJKLMNOPQRSTUVWXYZ@example.org/repo.git"
	other := "https://ci:another-secret-token-value@mirror.example.org/repo.git"
	// The other URL's userinfo sits right at the start of the kept tail.
	head := strings.Repeat("x", 100) + "fatal: " + other + "/: auth failed\n"
	// Size the stderr so the 4 KiB tail starts right after "ci:another-":
	// the kept text then begins "secret-token-value@mirror…", which the
	// userinfo pattern (it needs "user:") no longer recognizes.
	cut := strings.Index(head, "another-") + len("another-")
	stderr := head + strings.Repeat("y", cut+4<<10-len(head))
	got := gitDiagnostic(stderr, url)
	if len(got) > 4<<10 {
		t.Fatalf("diagnostic = %d bytes, want at most 4 KiB", len(got))
	}
	for _, secret := range []string{"another-secret-token-value", "secret-token", "token-value"} {
		if strings.Contains(got, secret) {
			t.Fatalf("diagnostic leaks %q: %q", secret, got[:120])
		}
	}
}

// TestRedactsBase64Spellings is backlog 2.48. A secret that a task reads
// out of a file and prints is not printed as itself: ansible.builtin.slurp
// returns file contents base64-encoded, and b64encode is an everyday
// filter. Before this the encoded value went through the redactor
// untouched and into the run log, where anyone with jobs.read could
// decode it — the plaintext never appearing is what made it look safe.
func TestRedactsBase64Spellings(t *testing.T) {
	const secret = "Sup3r-Secret-Become"
	r := newRedactor(&workerpb.Job{Redact: []string{secret}})

	for name, encoded := range map[string]string{
		"standard":          base64.StdEncoding.EncodeToString([]byte(secret)),
		"standard unpadded": base64.RawStdEncoding.EncodeToString([]byte(secret)),
		"url":               base64.URLEncoding.EncodeToString([]byte(secret)),
		"url unpadded":      base64.RawURLEncoding.EncodeToString([]byte(secret)),
	} {
		t.Run(name, func(t *testing.T) {
			got := r.text("ok: [app1] => {\"content\": \"" + encoded + "\", \"encoding\": \"base64\"}")
			if strings.Contains(got, encoded) {
				t.Fatalf("the %s spelling survived: %s", name, got)
			}
			if !strings.Contains(got, redactedMarker) {
				t.Fatalf("nothing was replaced: %s", got)
			}
		})
	}

	// The plaintext still goes, and ordinary output is untouched — the
	// encodings are additions, not a replacement.
	if got := r.text("password is " + secret); strings.Contains(got, secret) {
		t.Fatalf("plaintext survived: %s", got)
	}
	if plain := "ok: [app1] => changed=false"; r.text(plain) != plain {
		t.Fatalf("ordinary output was altered: %s", r.text(plain))
	}
}

// TestBase64SpellingsStayWithinWhatWeCanName pins the two decisions that
// bound this list, because both are the kind that quietly erode: every
// entry is replaced wherever it appears, so an entry added on a hunch
// costs real output.
func TestBase64SpellingsStayWithinWhatWeCanName(t *testing.T) {
	const secret = "Sup3r-Secret-Become"

	// A digest is not the secret, and no producer of one has been named.
	// If that changes, this test is the place the reasoning is revisited.
	r := newRedactor(&workerpb.Job{Redact: []string{secret}})
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(secret)))
	if got := r.text("checksum: " + digest); !strings.Contains(got, digest) {
		t.Fatalf("a hash is redacted; the list is meant to hold reversible spellings only: %s", got)
	}

	// Below minRedactLen nothing is collected at all, encodings included:
	// a short value cannot be protected in free text, and its encoding is
	// no more distinctive than the threshold already judged too risky.
	short := newRedactor(&workerpb.Job{Redact: []string{"ab"}})
	if len(short.values) != 0 {
		t.Fatalf("a value below minRedactLen produced %v", short.values)
	}

	// Every spelling is at least as long as its input, so none of them can
	// be the short string the threshold exists to keep out.
	for _, enc := range base64Spellings(secret) {
		if len(enc) < len(secret) {
			t.Fatalf("encoding %q is shorter than the secret it encodes", enc)
		}
	}
}
