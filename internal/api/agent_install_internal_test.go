package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"

	"github.com/noixdev/pulsix/internal/agentdist"
	"github.com/noixdev/pulsix/internal/clientip"
)

// installTestDeps is the smallest Deps the install routes read: the public
// URL the script is templated with, the distribution they serve and the
// address resolver the rate limit keys on. Everything else in Deps is
// irrelevant here, which is why these routes are tested without a database.
func installTestDeps(publicURL string, bin *fstest.MapFS) Deps {
	d := Deps{PublicURL: publicURL, ClientIP: clientip.New(nil)}
	if bin != nil {
		d.AgentBinaries = bin
	}
	return d
}

// installRouter mounts the two public agent-distribution routes exactly as
// api.New does, so the paths under test are the paths that ship.
func installRouter(t *testing.T, deps Deps) http.Handler {
	t.Helper()
	r := chi.NewRouter()
	if err := registerInstallRoutes(r, deps); err != nil {
		t.Fatalf("registerInstallRoutes: %v", err)
	}
	return r
}

func getFrom(t *testing.T, h http.Handler, path, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestInstallScriptIsRenderedWithTheManagerURL pins what a host installing
// an agent actually receives: the script templated with this manager's own
// public URL (no trailing slash, so the download URLs it builds have no
// double slash), the systemd unit inlined into its heredoc, and no leftover
// template syntax.
func TestInstallScriptIsRenderedWithTheManagerURL(t *testing.T) {
	h := installRouter(t, installTestDeps("https://manager.example/", nil))
	rec := getFrom(t, h, "/install.sh", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/x-shellscript") {
		t.Errorf("Content-Type = %q, want text/x-shellscript", ct)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`BASE_URL="https://manager.example"`,
		"$BASE_URL/agents/bin/linux/$PULSIX_ARCH",
		"ExecStart=/usr/local/bin/pulsix-agent",
		"EnvironmentFile=/etc/pulsix/agent.env",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the served script must contain %q", want)
		}
	}
	if strings.Contains(body, "{{") || strings.Contains(body, "}}") {
		t.Error("the served script still carries template syntax")
	}
}

// TestInstallScriptWithoutPublicURL: a manager that does not know its own
// public URL cannot name the host the agent downloads from, so the route
// says so instead of serving a script whose download URLs are empty.
func TestInstallScriptWithoutPublicURL(t *testing.T) {
	h := installRouter(t, installTestDeps("", nil))
	rec := getFrom(t, h, "/install.sh", "")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "PULSIX_PUBLIC_URL") {
		t.Errorf("the refusal must name the setting to fix: %s", rec.Body.String())
	}
}

// TestRenderInstallScriptRefusesAUnitThatBreaksTheHeredoc: the unit is
// inlined into a quoted heredoc, so a unit carrying the delimiter on its
// own line would end the heredoc early and hand the rest of itself to the
// shell as commands. The renderer refuses that instead of emitting it.
func TestRenderInstallScriptRefusesAUnitThatBreaksTheHeredoc(t *testing.T) {
	hostile := []byte("[Service]\nPULSIX_UNIT_EOF\nrm -rf /\n")
	if _, err := renderInstallScript(agentdist.InstallScript(), hostile, "https://manager.example"); err == nil {
		t.Fatal("render accepted a unit that closes the heredoc, want an error")
	}
	if _, err := renderInstallScript(agentdist.InstallScript(), agentdist.Unit(), "https://manager.example"); err != nil {
		t.Fatalf("render of the real unit failed: %v", err)
	}
}

// TestAgentBinaryServesTheEmbeddedFile: the two files install.sh fetches —
// the binary and its checksum — come back byte for byte, each with the
// content type its consumer expects.
func TestAgentBinaryServesTheEmbeddedFile(t *testing.T) {
	binary := []byte("\x7fELF-not-really-but-bytes-are-bytes")
	checksum := []byte("2f0c6a1e  pulsix-agent-linux-amd64\n")
	dist := &fstest.MapFS{
		"pulsix-agent-linux-amd64":        {Data: binary},
		"pulsix-agent-linux-amd64.sha256": {Data: checksum},
	}
	h := installRouter(t, installTestDeps("https://manager.example", dist))

	for _, tt := range []struct {
		path, contentType string
		want              []byte
	}{
		{"/agents/bin/linux/amd64", "application/octet-stream", binary},
		{"/agents/bin/linux/amd64.sha256", "text/plain", checksum},
	} {
		t.Run(tt.path, func(t *testing.T) {
			rec := getFrom(t, h, tt.path, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
			}
			if got := rec.Body.Bytes(); string(got) != string(tt.want) {
				t.Errorf("body = %q, want %q", got, tt.want)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, tt.contentType) {
				t.Errorf("Content-Type = %q, want %s", ct, tt.contentType)
			}
			if got, want := rec.Header().Get("Content-Length"), strconv.Itoa(len(tt.want)); got != want {
				t.Errorf("Content-Length = %q, want %q", got, want)
			}
		})
	}
}

// TestAgentBinaryUnknownOrHostileTarget: a target that is not in the dist
// is a 404, and a target that is not a plain lowercase GOOS/GOARCH word
// never reaches the filesystem at all — no path outside the distribution
// can be named, escaped to, or probed through these routes.
func TestAgentBinaryUnknownOrHostileTarget(t *testing.T) {
	dist := &fstest.MapFS{
		"pulsix-agent-linux-amd64": {Data: []byte("binary")},
		"secret.txt":               {Data: []byte("not part of the distribution")},
	}
	h := installRouter(t, installTestDeps("https://manager.example", dist))

	for _, path := range []string{
		"/agents/bin/linux/riscv64",         // shape is fine, not built
		"/agents/bin/plan9/amd64",           // ditto
		"/agents/bin/linux/amd64.sha512",    // only .sha256 is a checksum
		"/agents/bin/linux/..",              // traversal, literal
		"/agents/bin/linux/%2e%2e%2fsecret", // traversal, encoded
		"/agents/bin/..%2fsecret.txt/amd64",
		"/agents/bin/linux/AMD64", // upper case is not a GOARCH
	} {
		t.Run(path, func(t *testing.T) {
			rec := getFrom(t, h, path, "")
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "not part of the distribution") {
				t.Fatal("the route served a file from outside the distribution")
			}
		})
	}
}

// TestAgentBinaryWithoutADistribution: a manager built without running the
// agent-binary build carries no binaries. The route says exactly that, with
// the command that fixes it — rather than 404, which reads as "wrong URL".
func TestAgentBinaryWithoutADistribution(t *testing.T) {
	h := installRouter(t, installTestDeps("https://manager.example", nil))
	rec := getFrom(t, h, "/agents/bin/linux/amd64", "")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "make agent-binaries") {
		t.Errorf("the refusal must name the build step: %s", rec.Body.String())
	}
}

// TestInstallRoutesAreRateLimitedPerAddress: both routes are public, so one
// address cannot spend the manager on them indefinitely — and the budget is
// per address, so a flooding client cannot keep another host from
// installing its agent. The refusal carries Retry-After.
func TestInstallRoutesAreRateLimitedPerAddress(t *testing.T) {
	dist := &fstest.MapFS{"pulsix-agent-linux-amd64": {Data: []byte("binary")}}
	h := installRouter(t, installTestDeps("https://manager.example", dist))

	const flooder = "203.0.113.7:54321"
	for i := range installRateLimit {
		if rec := getFrom(t, h, "/install.sh", flooder); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i+1, rec.Code)
		}
	}
	rec := getFrom(t, h, "/install.sh", flooder)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status after the budget = %d, want 429", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" {
		t.Error("a 429 must carry Retry-After")
	} else if secs, err := strconv.Atoi(ra); err != nil || secs < 1 {
		t.Errorf("Retry-After = %q, want a positive number of seconds", ra)
	}

	// The binary route shares the address's budget — the flood is the
	// client, not the route.
	if rec := getFrom(t, h, "/agents/bin/linux/amd64", flooder); rec.Code != http.StatusTooManyRequests {
		t.Errorf("binary route for the flooding address = %d, want 429", rec.Code)
	}
	// Another address is untouched.
	if rec := getFrom(t, h, "/agents/bin/linux/amd64", "198.51.100.4:40000"); rec.Code != http.StatusOK {
		t.Errorf("another address = %d, want 200", rec.Code)
	}
}
