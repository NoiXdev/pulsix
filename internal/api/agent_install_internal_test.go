package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"

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
		"$BASE_URL/agents/bin/$PULSIX_OS/$PULSIX_ARCH",
		"ExecStart=/usr/local/bin/pulsix-agent",
		"EnvironmentFile=/etc/pulsix/agent.env",
		"<string>dev.noix.pulsix-agent</string>",
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

// TestInstallPs1IsRenderedWithTheManagerURL: the Windows installer is
// served like install.sh — templated with this manager's public URL — as
// plain text, since PowerShell reads it into a script block rather than
// saving it.
func TestInstallPs1IsRenderedWithTheManagerURL(t *testing.T) {
	h := installRouter(t, installTestDeps("https://manager.example/", nil))
	rec := getFrom(t, h, "/install.ps1", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/plain; charset=utf-8", ct)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got, want := rec.Header().Get("Content-Length"), strconv.Itoa(rec.Body.Len()); got != want {
		t.Errorf("Content-Length = %q, want %q", got, want)
	}
	body := rec.Body.String()
	for _, want := range []string{"$BaseUrl = 'https://manager.example'", "PulsixAgent"} {
		if !strings.Contains(body, want) {
			t.Errorf("the served script must contain %q", want)
		}
	}
	if strings.Contains(body, "{{") || strings.Contains(body, "}}") {
		t.Error("the served script still carries template syntax")
	}
}

func TestInstallPs1WithoutPublicURL(t *testing.T) {
	h := installRouter(t, installTestDeps("", nil))
	rec := getFrom(t, h, "/install.ps1", "")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "PULSIX_PUBLIC_URL") {
		t.Errorf("the refusal must name the setting to fix: %s", rec.Body.String())
	}
}

// A public URL the PowerShell installer cannot carry safely is a
// configuration the manager refuses to start with, not one it serves a
// broken (or injectable) script for.
func TestInstallRoutesRefuseAPublicURLThePowerShellScriptCannotCarry(t *testing.T) {
	r := chi.NewRouter()
	if err := registerInstallRoutes(r, installTestDeps("https://manager.example/it's", nil)); err == nil {
		t.Fatal("registerInstallRoutes accepted a public URL with a single quote")
	}
}

// TestInstallPs1SharesTheRateLimit: the Windows script spends the same
// per-address budget as the other distribution routes.
func TestInstallPs1SharesTheRateLimit(t *testing.T) {
	h := installRouter(t, installTestDeps("https://manager.example", nil))

	const flooder = "203.0.113.9:54321"
	for i := range installRateLimit {
		if rec := getFrom(t, h, "/install.sh", flooder); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i+1, rec.Code)
		}
	}
	rec := getFrom(t, h, "/install.ps1", flooder)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("install.ps1 after the budget = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("a 429 must carry Retry-After")
	}
	if rec := getFrom(t, h, "/install.ps1", "198.51.100.4:40000"); rec.Code != http.StatusOK {
		t.Errorf("another address = %d, want 200", rec.Code)
	}
}

// TestAgentInstallCommandWindows pins the PowerShell line the Add-agent
// dialog shows, built from the same parts as the shell command.
func TestAgentInstallCommandWindows(t *testing.T) {
	const prefix = "[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor 3072; & ([scriptblock]::Create((irm "
	for _, tt := range []struct {
		name     string
		deps     Deps
		nameHint string
		want     string
	}{
		{
			name: "plain",
			deps: Deps{PublicURL: "https://manager.example/", WorkerEndpoint: WorkerEndpoint{Addr: "manager.example:8444", ServerName: "manager.example"}},
			want: prefix + "https://manager.example/install.ps1))) -Manager manager.example:8444",
		},
		{
			name: "server name differs from the dialed host",
			deps: Deps{PublicURL: "https://manager.example", WorkerEndpoint: WorkerEndpoint{Addr: "10.0.0.5:8444", ServerName: "grpc.manager.example"}},
			want: prefix + "https://manager.example/install.ps1))) -Manager '10.0.0.5:8444' -ServerName grpc.manager.example",
		},
		{
			name:     "name hint",
			deps:     Deps{PublicURL: "https://manager.example", WorkerEndpoint: WorkerEndpoint{Addr: "manager.example:8444"}},
			nameHint: "edge-1",
			want:     prefix + "https://manager.example/install.ps1))) -Manager manager.example:8444 -Name edge-1",
		},
		{
			name:     "server name and name hint",
			deps:     Deps{PublicURL: "https://manager.example", WorkerEndpoint: WorkerEndpoint{Addr: "10.0.0.5:8444", ServerName: "grpc.manager.example"}},
			nameHint: "edge-1",
			want:     prefix + "https://manager.example/install.ps1))) -Manager '10.0.0.5:8444' -ServerName grpc.manager.example -Name edge-1",
		},
		{
			name: "fallbacks without a public URL or endpoint",
			deps: Deps{},
			// < and > are redirections in PowerShell: the placeholder is quoted.
			want: prefix + "https://<manager>/install.ps1))) -Manager '<manager>:8444'",
		},
		{
			// PowerShell would read a bare 007 as the number 7.
			name:     "a hint that starts with a digit is quoted",
			deps:     Deps{PublicURL: "https://manager.example", WorkerEndpoint: WorkerEndpoint{Addr: "manager.example:8444"}},
			nameHint: "007",
			want:     prefix + "https://manager.example/install.ps1))) -Manager manager.example:8444 -Name '007'",
		},
		{
			// The hint is free text an operator typed: anything beyond a
			// plain word is quoted so it stays one argument and is never
			// run as code in an administrator's PowerShell.
			name:     "a hint that is not a plain word is quoted",
			deps:     Deps{PublicURL: "https://manager.example", WorkerEndpoint: WorkerEndpoint{Addr: "manager.example:8444"}},
			nameHint: "it's $(Remove-Item C:\\) \u2019x",
			want:     prefix + "https://manager.example/install.ps1))) -Manager manager.example:8444 -Name 'it''s $(Remove-Item C:\\) \u2019\u2019x'",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := agentInstallCommandWindows(tt.deps, tt.nameHint); got != tt.want {
				t.Errorf("agentInstallCommandWindows() =\n  %s\nwant\n  %s", got, tt.want)
			}
		})
	}
}

// TestShellArg pins the exact quoting of the Linux/macOS install line: plain
// words stay bare, everything else becomes one single-quoted word. The
// strings are asserted, never executed.
func TestShellArg(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"edge-1", "edge-1"},
		{"manager.example:8444", "manager.example:8444"},
		{"a/b@c%d+e=f,g_h", "a/b@c%d+e=f,g_h"},
		{"x; rm -rf /", "'x; rm -rf /'"},
		{"$(id)", "'$(id)'"},
		{"a'b", `'a'\''b'`},
		{"-x", "'-x'"},
		{"<manager>:8444", "'<manager>:8444'"},
		{"", "''"},
	} {
		if got := shellArg(tt.in); got != tt.want {
			t.Errorf("shellArg(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

func TestAgentInstallCommandQuotesWhatItInterpolates(t *testing.T) {
	const prefix = "curl -fsSL https://manager.example/install.sh | sudo sh -s -- "
	deps := Deps{PublicURL: "https://manager.example", WorkerEndpoint: WorkerEndpoint{Addr: "manager.example:8444"}}
	for _, tt := range []struct {
		name     string
		deps     Deps
		nameHint string
		want     string
	}{
		{"plain", deps, "", prefix + "--manager manager.example:8444"},
		{"plain hint", deps, "edge-1", prefix + "--manager manager.example:8444 --name edge-1"},
		{
			"server name differs",
			Deps{PublicURL: "https://manager.example", WorkerEndpoint: WorkerEndpoint{Addr: "10.0.0.5:8444", ServerName: "grpc.manager.example"}},
			"edge-1",
			prefix + "--manager 10.0.0.5:8444 --server-name grpc.manager.example --name edge-1",
		},
		{"hostile hint", deps, "x; rm -rf /", prefix + "--manager manager.example:8444 --name 'x; rm -rf /'"},
		{"substitution", deps, "$(id)", prefix + "--manager manager.example:8444 --name '$(id)'"},
		{"apostrophe", deps, "a'b", prefix + `--manager manager.example:8444 --name 'a'\''b'`},
		{"leading dash", deps, "-x", prefix + "--manager manager.example:8444 --name '-x'"},
		{
			"hostile config values",
			Deps{PublicURL: "https://manager.example", WorkerEndpoint: WorkerEndpoint{Addr: "a b:8444", ServerName: "$(id)"}},
			"",
			prefix + "--manager 'a b:8444' --server-name '$(id)'",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := agentInstallCommand(tt.deps, tt.nameHint); got != tt.want {
				t.Errorf("agentInstallCommand() =\n  %s\nwant\n  %s", got, tt.want)
			}
		})
	}
}
