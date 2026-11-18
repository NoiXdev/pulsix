package api

import (
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/noixdev/pulsix/internal/agentdist"
	"github.com/noixdev/pulsix/internal/assistantscript"
	"github.com/noixdev/pulsix/internal/clientip"
)

// The agent distribution's public routes: GET /install.sh (Linux and macOS)
// and GET /install.ps1 (Windows) hand a host the installer templated with
// this manager's own public URL, and
// GET /agents/bin/{os}/{arch}[.sha256] hands it the binary that installer
// downloads plus the checksum it verifies against. Both are reachable
// without a session — a host being installed has no credential yet, and
// the enrollment token it is given is the credential that matters — so both
// are rate limited per address and serve nothing but the embedded
// distribution (internal/agentdist).

const (
	// installRateLimit is how many requests one address may make to the
	// distribution routes per installRateWindow. One install does three
	// (script, binary, checksum), and a fleet rollout runs many of them
	// from one egress address, so the budget is deliberately roomy — 20
	// installs a minute from a single NAT — while still bounding what a
	// flood costs: the binary is megabytes per request.
	installRateLimit  = 60
	installRateWindow = time.Minute
)

// agentTargetPart is the shape a GOOS or GOARCH word has. The binary route
// builds a filename from the two path parameters, so they are validated
// before anything touches the distribution: no separator, no dot, no upper
// case, nothing a path could be escaped with.
var agentTargetPart = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// installServer serves the agent distribution. script and psScript are
// rendered once at construction (both inputs are static: the embedded
// templates and the configured public URL), nil when the manager has no
// public URL to name; bin is nil in a build without agent binaries — see
// Deps.AgentBinaries.
type installServer struct {
	script   []byte
	psScript []byte
	bin      fs.FS
	lim      *clientip.Limiter
	ip       *clientip.Resolver
}

// registerInstallRoutes mounts the distribution routes on the root router
// (they sit outside /api: an installer fetches them with curl, not through
// the API client, and the SPA never calls them). It fails only when the
// embedded install script cannot be rendered at all, which is a build
// wiring bug — api.New refuses to hand back a handler for it rather than
// discovering it on the first install. The PowerShell installer also
// refuses a public URL with a single quote in it (it would end the string
// the script carries the URL in), so such a URL stops the manager at
// startup rather than serving an injectable script.
func registerInstallRoutes(r chi.Router, deps Deps) error {
	s := &installServer{
		bin: deps.AgentBinaries,
		ip:  deps.ClientIP,
		lim: clientip.NewLimiter(installRateLimit, installRateWindow, time.Now),
	}
	if deps.PublicURL != "" {
		script, err := agentdist.RenderShellInstaller(deps.PublicURL)
		if err != nil {
			return fmt.Errorf("render install.sh: %w", err)
		}
		s.script = script
		psScript, err := agentdist.RenderPowerShellInstaller(deps.PublicURL)
		if err != nil {
			return fmt.Errorf("render install.ps1: %w", err)
		}
		s.psScript = psScript
	}
	r.Get("/install.sh", s.serveScript)
	r.Get("/install.ps1", s.servePowerShellScript)
	// The assistant connect script is static (no public URL needed): it is
	// the same bytes as scripts/assistant-mcp.sh, see internal/assistantscript.
	r.Get("/assistant-mcp.sh", s.serveAssistantScript)
	r.Get("/agents/bin/{os}/{arch}", s.serveBinary)
	return nil
}

func (s *installServer) serveScript(w http.ResponseWriter, r *http.Request) {
	s.writeScript(w, r, s.script, "text/x-shellscript; charset=utf-8")
}

// servePowerShellScript serves install.ps1 as plain text: PowerShell reads
// it into a script block (irm) rather than saving it, so no script type is
// needed, and a browser shows it instead of offering a download.
func (s *installServer) servePowerShellScript(w http.ResponseWriter, r *http.Request) {
	s.writeScript(w, r, s.psScript, "text/plain; charset=utf-8")
}

// serveAssistantScript serves the assistant connect script exactly as
// embedded: unlike install.sh it names no host, so it works on a manager
// without a public URL.
func (s *installServer) serveAssistantScript(w http.ResponseWriter, r *http.Request) {
	s.writeScript(w, r, assistantscript.Script(), "text/x-shellscript; charset=utf-8")
}

func (s *installServer) writeScript(w http.ResponseWriter, r *http.Request, script []byte, contentType string) {
	if !s.allow(w, r) {
		return
	}
	if script == nil {
		http.Error(w, "this manager does not know its own public URL (set PULSIX_PUBLIC_URL); the install script cannot name the host to download the agent from", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(script)))
	_, _ = w.Write(script)
}

func (s *installServer) serveBinary(w http.ResponseWriter, r *http.Request) {
	if !s.allow(w, r) {
		return
	}
	name, checksum, ok := agentBinaryName(chi.URLParam(r, "os"), chi.URLParam(r, "arch"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if s.bin == nil {
		http.Error(w, "this manager build carries no agent binaries: build them with `make agent-binaries` before building the manager", http.StatusServiceUnavailable)
		return
	}
	f, err := s.bin.Open(name)
	if err != nil {
		// Either the target was never built or the dist is incomplete;
		// from the caller's side both are "no such binary here".
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	contentType := "application/octet-stream"
	if checksum {
		contentType = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// The binary is ~12 MB, so it is streamed rather than read into
	// memory: a flood of concurrent downloads must not cost one full copy
	// of it per request. ServeContent needs a seeker for that (both
	// embed.FS and the tests' in-memory dist provide one) and keeps the
	// Content-Type set above; a filesystem without one falls back to a
	// plain copy, which still streams, only without range support.
	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, name, time.Time{}, rs)
		return
	}
	if info, err := f.Stat(); err == nil {
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	}
	_, _ = io.Copy(w, f)
}

// agentBinaryName maps the two path parameters to the distribution's file
// name, reporting whether the request is for a checksum and whether the
// target is well-formed at all. An ill-formed target never reaches the
// filesystem: the name is built from validated words, never from the raw
// parameters.
func agentBinaryName(goos, arch string) (name string, checksum bool, ok bool) {
	checksum = strings.HasSuffix(arch, agentdist.ChecksumSuffix)
	base := strings.TrimSuffix(arch, agentdist.ChecksumSuffix)
	if !agentTargetPart.MatchString(goos) || !agentTargetPart.MatchString(base) {
		return "", false, false
	}
	name = agentdist.BinaryPrefix + goos + "-" + base
	if checksum {
		name += agentdist.ChecksumSuffix
	}
	return name, checksum, true
}

// allow spends one of the address's requests and answers 429 when the
// budget is gone. The address is resolved through the same trusted-proxy
// rules every other rate limit here keys on (PULSIX_TRUSTED_PROXIES).
func (s *installServer) allow(w http.ResponseWriter, r *http.Request) bool {
	ip := s.ip.FromRequest(r)
	if s.lim.Allow(ip) {
		return true
	}
	remaining, _ := s.lim.Refused(ip)
	secs := int(math.Ceil(remaining.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	http.Error(w, "too many requests from this address; retry later", http.StatusTooManyRequests)
	return false
}
