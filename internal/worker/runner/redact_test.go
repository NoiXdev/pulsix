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

// TestRedactsPythonJSONSpelling is backlog 7.9. ansible's result display
// prints module results with Python's json.dumps(ensure_ascii=False), which
// escapes '"', '\\' and control characters but leaves '&', '<' and '>'
// literal, while Go's json.Marshal HTML-escapes those three. A secret mixing
// both kinds therefore matched neither listed spelling and reached viewers.
// The display line below is ansible-core 2.21.4's own output for such a
// secret (security audit run 1).
func TestRedactsPythonJSONSpelling(t *testing.T) {
	for _, tc := range []struct{ name, secret, display string }{
		{"quote and ampersand", `Tr&ub4dor"3x`, `ok: [localhost] => {` + "\r\n" + `    "db_password": "Tr&ub4dor\"3x"` + "\r\n" + `}`},
		{"backslash and angle brackets", `p<a>ss\w0rd`, `"secret": "p<a>ss\\w0rd"`},
		{"tab and newline", "line1\tpart&x\nline2", `"v": "line1\tpart&x\nline2"`},
		{"other control character", "ab\x01cd&ef", `"v": "ab\u0001cd&ef"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRedactor(&workerpb.Job{Redact: []string{tc.secret}})
			got := r.text(tc.display)
			if !strings.Contains(got, redactedMarker) {
				t.Fatalf("the display spelling of %q survived: %s", tc.secret, got)
			}
		})
	}
}

// TestRedactsBase64AtAnyOffset is backlog 7.11. base64 encodes three bytes
// at a time, so a secret inside a larger buffer (slurp of "pwd=<secret>")
// encodes differently depending on where it starts; the whole-value
// spellings only cover a secret at a 3-byte boundary. Every placement must
// lose the secret, whatever byte follows it.
func TestRedactsBase64AtAnyOffset(t *testing.T) {
	const secret = "s3cr3tPassw0rd"
	r := newRedactor(&workerpb.Job{Redact: []string{secret}})
	for _, prefix := range []string{"", "a", "ab", "abc", "pwd=", "db_pass=", "password: "} {
		for _, suffix := range []string{"", "\n", "x", "xy"} {
			for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
				content := enc.EncodeToString([]byte(prefix + secret + suffix))
				got := r.text(`{"content": "` + content + `"}`)
				if strings.Contains(got, content) || !strings.Contains(got, redactedMarker) {
					t.Fatalf("prefix %q suffix %q: the encoded secret survived: %s", prefix, suffix, got)
				}
			}
		}
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

	// No spelling is short enough to shred ordinary output: an aligned
	// core is at least minBase64Core bytes, so its encoding is at least
	// 8 characters of base64.
	for _, enc := range base64Spellings(secret) {
		if len(enc) < 8 {
			t.Fatalf("encoding %q is shorter than 8 characters", enc)
		}
	}
}

// TestRedactsASecretInsideJSONText is backlog 7.34. A secret held as JSON
// text inside a result (a to_json, a module that returns JSON as a string)
// is escaped once there and once more by the result display, and
// ansible's to_json escapes non-ASCII text as well. The display lines
// below are ansible-core 2.21.4's own output in the worker image for a
// debug of `{"p": secret} | to_json` and of `secret | to_json`.
func TestRedactsASecretInsideJSONText(t *testing.T) {
	for _, tc := range []struct{ name, secret, display string }{
		{"quote, backslash and ampersand", `Tr&ub4dor"3x\y`, `    "msg": "{\"p\": \"Tr&ub4dor\\\"3x\\\\y\"}"`},
		{"non-ASCII in a document", `Pässwörd"<9>`, `    "msg": "{\"p\": \"P\\u00e4ssw\\u00f6rd\\\"<9>\"}"`},
		{"non-ASCII on its own", `Pässwörd"<9>`, `    "msg": "\"P\\u00e4ssw\\u00f6rd\\\"<9>\""`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRedactor(&workerpb.Job{Redact: []string{tc.secret}})
			got := r.text(tc.display)
			if !strings.Contains(got, redactedMarker) {
				t.Fatalf("the nested JSON spelling of %q survived: %s", tc.secret, got)
			}
		})
	}
}

// TestRedactorCoversPythonReprSpelling is backlog 7.53. A play that builds a
// string from a dict or list ("cfg: {{ cfg }}") renders its members through
// Python's repr(), which is neither a JSON spelling nor the plain secret: for
// a value holding both quote kinds it writes a single-quoted literal with the
// single quote backslash-escaped (Pa"ss\'w0rd-9), and the result display then
// JSON-escapes that again (Pa\"ss\\'w0rd-9). The two event lines below are
// ansible-core 2.21.4's own runner_on_ok output for such a secret (security
// audit run 3, the secret is a dummy): the secret survived into both the
// event's result and its display text.
func TestRedactorCoversPythonReprSpelling(t *testing.T) {
	const secret = `Pa"ss'w0rd-9`
	r := newRedactor(&workerpb.Job{Redact: []string{secret}})

	for name, line := range map[string]string{
		"inline dict": `{"uuid": "fb33db89-3f67-4d77-98db-18e5ae27d452", "counter": 5, "stdout": "ok: [localhost] => {\r\n    \"msg\": \"cfg: {'p': 'Pa\\\"ss\\\\'w0rd-9'}\"\r\n}", "start_line": 4, "end_line": 7, "runner_ident": "v12", "event": "runner_on_ok", "pid": 16, "created": "2026-11-03T06:23:21.092959+00:00", "parent_uuid": "e575923b-c430-720b-5a92-000000000004", "event_data": {"playbook": "play.yml", "play": "localhost", "task": "inline-dict", "task_action": "debug", "resolved_action": "ansible.builtin.debug", "task_args": "", "host": "localhost", "remote_addr": "localhost", "res": {"changed": false, "msg": "cfg: {'p': 'Pa\"ss\\'w0rd-9'}", "_ansible_verbose_always": true}, "uuid": "fb33db89-3f67-4d77-98db-18e5ae27d452"}}`,
		"var dict":    `{"uuid": "f8fa53ad-3901-4ce3-877e-d7ed7ed46a7f", "counter": 8, "stdout": "ok: [localhost] => {\r\n    \"msg\": \"cfg: {'p': 'Pa\\\"ss\\\\'w0rd-9'}\"\r\n}", "start_line": 9, "end_line": 12, "runner_ident": "v12", "event": "runner_on_ok", "pid": 16, "created": "2026-11-03T06:23:21.102852+00:00", "parent_uuid": "e575923b-c430-720b-5a92-000000000005", "event_data": {"playbook": "play.yml", "play": "localhost", "task": "var-dict", "task_action": "debug", "resolved_action": "ansible.builtin.debug", "task_args": "", "host": "localhost", "remote_addr": "localhost", "res": {"changed": false, "msg": "cfg: {'p': 'Pa\"ss\\'w0rd-9'}", "_ansible_verbose_always": true}, "uuid": "f8fa53ad-3901-4ce3-877e-d7ed7ed46a7f"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			ev, _, err := ParseEvent([]byte(line))
			if err != nil {
				t.Fatalf("ParseEvent: %v", err)
			}
			got := r.eventPayload(ev)
			// "w0rd-9" is the secret's tail: any surviving spelling keeps it.
			if strings.Contains(got, "w0rd-9") {
				t.Fatalf("a spelling of the secret survived: %s", got)
			}
			if !strings.Contains(got, redactedMarker) {
				t.Fatalf("nothing was replaced: %s", got)
			}
		})
	}
}

// TestReprSpellingMatchesCPython pins reprSpelling to repr()'s string body.
// The expected bodies are what CPython 3 prints for each value.
func TestReprSpellingMatchesCPython(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain", "plain-pw", "plain-pw"},
		{"single quote only, double-quoted literal", `it's-a-pw`, `it's-a-pw`},
		{"double quote only, single-quoted literal", `say"hi"-pw`, `say"hi"-pw`},
		{"both quotes, single quote escaped", `Pa"ss'w0rd-9`, `Pa"ss\'w0rd-9`},
		{"backslash", `back\slash-pw`, `back\\slash-pw`},
		{"newline, return and tab", "a\nb\rc\td-pw", `a\nb\rc\td-pw`},
		{"control character", "ab\x01cd-pw", `ab\x01cd-pw`},
		{"delete", "ab\x7fcd-pw", `ab\x7fcd-pw`},
		{"printable non-ASCII stays", "Pässwörd-€-😀", "Pässwörd-€-😀"},
		{"no-break space", "ab cd-pw", `ab\xa0cd-pw`},
		{"zero-width space", "ab\u200bcd-pw", `ab\u200bcd-pw`},
		{"private use outside the BMP", "ab\U000f0000cd-pw", `ab\U000f0000cd-pw`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := reprSpelling(tc.in); got != tc.want {
				t.Fatalf("reprSpelling(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Each secret below is printed the way a template renders a dict holding it
// ({'p': <repr>}) and the way the result display shows that text (JSON-escaped
// once more); neither may keep the secret's tail.
func TestRedactsPythonReprSpellings(t *testing.T) {
	for _, tc := range []struct{ name, secret, literal string }{
		{"single quote only", `it's-w0rd-9`, `"it's-w0rd-9"`},
		{"double quote only", `say"hi"-w0rd-9`, `'say"hi"-w0rd-9'`},
		{"both quotes", `Pa"ss'w0rd-9`, `'Pa"ss\'w0rd-9'`},
		// Python picks the quote from the whole printed value: here a
		// double quote elsewhere in it forces the single-quoted literal.
		{"single quote inside a value holding a double quote", `it's-w0rd-9`, `'x "y" it\'s-w0rd-9'`},
		{"backslash", `back\slash-w0rd-9`, `'back\\slash-w0rd-9'`},
		{"non-printable byte", "ab\x01cd-w0rd-9", `'ab\x01cd-w0rd-9'`},
		{"newline", "ab\ncd-w0rd-9", `'ab\ncd-w0rd-9'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRedactor(&workerpb.Job{Redact: []string{tc.secret}})
			rendered := "cfg: {'p': " + tc.literal + "}"
			shown := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(rendered)
			for name, text := range map[string]string{"template output": rendered, "display text": shown} {
				got := r.text(text)
				if strings.Contains(got, "w0rd-9") || !strings.Contains(got, redactedMarker) {
					t.Fatalf("%s kept the secret: %s", name, got)
				}
			}
		})
	}
}
