package secrets

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const testStatusToken = "0123456789abcdef0123456789abcdef" // 32 characters

// TestParseStatusPage: the status_page section (backlog 3.132) — absent,
// empty or without listen is off; loopback needs no token; a network
// address with a token is accepted; localhost binds 127.0.0.1.
func TestParseStatusPage(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want StatusPage
	}{
		{"absent", "secrets: {}\n", StatusPage{}},
		{"empty section", "status_page:\n", StatusPage{}},
		{"empty mapping", "status_page: {}\n", StatusPage{}},
		{"no listen", "status_page:\n  token: " + testStatusToken + "\n", StatusPage{}},
		{"loopback v4", "status_page:\n  listen: \"127.0.0.1:8742\"\n", StatusPage{Listen: "127.0.0.1:8742"}},
		{"loopback other 127", "status_page:\n  listen: 127.8.9.1:8742\n", StatusPage{Listen: "127.8.9.1:8742"}},
		{"loopback v6", "status_page:\n  listen: \"[::1]:8742\"\n", StatusPage{Listen: "[::1]:8742"}},
		{"localhost", "status_page:\n  listen: localhost:8742\n", StatusPage{Listen: "127.0.0.1:8742"}},
		{"loopback with token", "status_page:\n  listen: 127.0.0.1:8742\n  token: " + testStatusToken + "\n",
			StatusPage{Listen: "127.0.0.1:8742", Token: testStatusToken}},
		{"network", "status_page:\n  listen: 0.0.0.0:8742\n  token: " + testStatusToken + "\n",
			StatusPage{Listen: "0.0.0.0:8742", Token: testStatusToken, Network: true}},
		{"every interface", "status_page:\n  listen: \":8742\"\n  token: " + testStatusToken + "\n",
			StatusPage{Listen: ":8742", Token: testStatusToken, Network: true}},
		{"name", "status_page:\n  listen: status.example:8742\n  token: " + testStatusToken + "\n",
			StatusPage{Listen: "status.example:8742", Token: testStatusToken, Network: true}},
		{"tls", "status_page:\n  listen: 10.0.0.5:8742\n  token: " + testStatusToken + "\n  tls_cert: /etc/c.pem\n  tls_key: /etc/k.pem\n",
			StatusPage{Listen: "10.0.0.5:8742", Token: testStatusToken, TLSCert: "/etc/c.pem", TLSKey: "/etc/k.pem", Network: true}},
		{"flow", "status_page: {listen: \"127.0.0.1:9000\"}\n", StatusPage{Listen: "127.0.0.1:9000"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			set, err := parse([]byte(tc.src))
			if err != nil {
				t.Fatalf("parse = %v", err)
			}
			got, problem := set.StatusPage()
			if problem != "" {
				t.Fatalf("problem = %q", problem)
			}
			if got != tc.want {
				t.Fatalf("StatusPage = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestParseStatusPageRules: every mistake turns the page off with a line,
// a column and a fixed reason that never quotes what was written — and a
// network address without a token is one of them.
func TestParseStatusPageRules(t *testing.T) {
	const planted = "plantedvalue99"
	tests := []struct{ name, src, want string }{
		{"not a mapping", "status_page: [x]\n", "line 1, column 14: " + badStatusPage},
		{"scalar", "status_page: " + planted + "\n", "line 1, column 14: " + badStatusPage},
		{"unknown key", "status_page:\n  " + planted + ": 1\n", "line 2, column 3: " + badStatusPageKey},
		{"duplicate key", "status_page:\n  listen: 127.0.0.1:1\n  listen: 127.0.0.1:2\n", "line 3, column 3: listen is already set at line 2"},
		{"list value", "status_page:\n  listen: [" + planted + "]\n", "line 2, column 11: " + badStatusPageValue},
		{"no port", "status_page:\n  listen: " + planted + "\n", "line 2, column 11: " + badStatusListen},
		{"port zero", "status_page:\n  listen: 127.0.0.1:0\n", "line 2, column 11: " + badStatusListen},
		{"port too big", "status_page:\n  listen: 127.0.0.1:70000\n", "line 2, column 11: " + badStatusListen},
		{"port name", "status_page:\n  listen: 127.0.0.1:http\n", "line 2, column 11: " + badStatusListen},
		{"network without token", "status_page:\n  listen: 0.0.0.0:8742\n", "line 2, column 11: " + badStatusNoToken},
		{"every interface without token", "status_page:\n  listen: \":8742\"\n", "line 2, column 11: " + badStatusNoToken},
		{"short token", "status_page:\n  listen: 127.0.0.1:8742\n  token: " + planted + "\n", "line 3, column 10: " + badStatusToken},
		{"long token", "status_page:\n  token: " + strings.Repeat("x", maxStatusTokenLen+1) + "\n", "line 2, column 10: " + badStatusToken},
		{"cert without key", "status_page:\n  listen: 127.0.0.1:8742\n  tls_cert: /" + planted + "\n", "line 3, column 3: " + badStatusTLSPair},
		{"key without cert", "status_page:\n  listen: 127.0.0.1:8742\n  tls_key: /" + planted + "\n", "line 3, column 3: " + badStatusTLSPair},
		{"anchor inside", "status_page:\n  listen: &a 127.0.0.1:8742\n", "line 2, column 11: " + badAnchorReason},
		{"tag inside", "status_page: !!map\n  listen: 127.0.0.1:8742\n", "line 1, column 14: " + badTagReason},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			set, err := parse([]byte(tc.src))
			if err != nil {
				t.Fatalf("parse = %v, want the file accepted", err)
			}
			page, problem := set.StatusPage()
			if problem != tc.want {
				t.Fatalf("problem = %q, want %q", problem, tc.want)
			}
			if page.On() {
				t.Fatalf("a refused section turned the page on: %+v", page)
			}
			if strings.Contains(problem, planted) {
				t.Fatalf("problem quotes the file: %q", problem)
			}
		})
	}
}

// TestParseStatusPageProblemKeepsTheFile: a refused status_page leaves
// the secrets, commands, local checks and probe policy in effect.
func TestParseStatusPageProblemKeepsTheFile(t *testing.T) {
	src := "secrets:\n  db:\n    password: s3cr3t-value\n" +
		"commands:\n  df: [/bin/df]\n" +
		"checks:\n  local-cpu:\n    plugin: cpu\n    interval_seconds: 60\n" +
		"probes:\n  enabled: false\n" +
		"status_page:\n  listen: 0.0.0.0:8742\n  extra: &x 1\n"
	set, err := parse([]byte(src))
	if err != nil {
		t.Fatalf("parse = %v", err)
	}
	if _, problem := set.StatusPage(); problem == "" {
		t.Fatal("no status_page problem")
	}
	if got := set.Names(); len(got) != 1 || got[0] != "db" {
		t.Fatalf("secrets = %v", got)
	}
	if got := set.CommandNames(); len(got) != 1 {
		t.Fatalf("commands = %v", got)
	}
	if got := set.LocalChecks(); len(got) != 1 {
		t.Fatalf("local checks = %v", got)
	}
	if !set.Probes().Disabled {
		t.Fatal("probe policy lost")
	}
	if set.Redactor().Redact("x s3cr3t-value y") != "x [secret] y" {
		t.Fatal("redactor lost")
	}
}

// TestParseStatusPageSyntaxStaysFileLevel: an anchor or tag elsewhere
// still refuses the whole file; a second status_page key does too.
func TestParseStatusPageSyntaxStaysFileLevel(t *testing.T) {
	for _, src := range []string{
		"secrets: &a {}\nstatus_page:\n  listen: 127.0.0.1:8742\n",
		"status_page: {}\nstatus_page: {}\n",
		"status_page:\n  listen: [\n",
	} {
		if _, err := parse([]byte(src)); err == nil {
			t.Fatalf("parse(%q) accepted", src)
		}
	}
}

// TestSetStringNeverShowsToken: a printed or logged Set says whether the
// page is on, never its token.
func TestSetStringNeverShowsToken(t *testing.T) {
	set, err := parse([]byte("status_page:\n  listen: 0.0.0.0:8742\n  token: " + testStatusToken + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range []string{set.String(), fmt.Sprintf("%v %+v %#v", set, set, set)} {
		if strings.Contains(got, testStatusToken) || !strings.Contains(got, "status_page=true") {
			t.Fatalf("printed set = %q", got)
		}
	}
}

// TestWatcherLogsStatusPageProblemOnce: a refused section is a warning,
// once per change, and the file still loads.
func TestWatcherLogsStatusPageProblemOnce(t *testing.T) {
	f := newWatchFixture(t)
	f.write("status_page:\n  listen: 0.0.0.0:8742\n")
	if _, state := f.w.Current(); state != StateOK {
		t.Fatalf("state = %q", state)
	}
	f.advance(time.Minute)
	f.w.Current()
	if n := strings.Count(f.log.String(), "refused the status_page settings"); n != 1 {
		t.Fatalf("warned %d times:\n%s", n, f.log.String())
	}
}
