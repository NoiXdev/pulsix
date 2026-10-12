// The v0.3a milestone gate (backlog 3.4a, first half): a real agent
// binary's loop — internal/agent, the same code cmd/agent runs — enrolling
// against a fully wired manager (internal/app.Build) over loopback mTLS,
// against a TimescaleDB testcontainer. Nothing is faked on either side:
// the agent pins the manager's CA from its enrollment token, the manager
// issues its certificate, the stream carries real heartbeats and real
// facts collected from this machine, and the operator drives every step
// through the HTTP API the GUI uses.
//
// The four things this gate exists to prove, in one scenario:
//
//  1. An unbound token creates its host: the agent appears as a host that
//     did not exist before, monitored, tagged from its own facts.
//  2. A bound token binds: a second agent lands on the host an operator
//     pre-created, not on a new one.
//  3. Renewal swaps the certificate under a running agent, and the new
//     serial is the one the manager knows.
//  4. Revocation is immediate: the binding is gone and the revoked
//     certificate no longer opens a stream.
package e2e

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/agent"
	"github.com/noixdev/pulsix/internal/agent/facts"
	"github.com/noixdev/pulsix/internal/app"
	"github.com/noixdev/pulsix/internal/config"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// agentWorld is the wired manager the agent scenario drives.
type agentWorld struct {
	api      *apiClient
	org      string
	zoneID   string
	grpcAddr string
}

// agentDTO mirrors the Agents page's row (internal/api's agentDTO) —
// what the operator sees is what this gate asserts.
type agentDTO struct {
	HostID        string     `json:"host_id"`
	HostName      string     `json:"host_name"`
	AgentName     string     `json:"agent_name"`
	Version       string     `json:"version"`
	Status        string     `json:"status"`
	LastSeen      *time.Time `json:"last_seen"`
	CertExpiresAt *time.Time `json:"cert_expires_at"`
}

// wireAgentManager boots the manager the way ansible_test.go's scenario
// does — the real app, its real gRPC surface on a loopback listener, an
// operator session with the mandatory second factor — without the SSH and
// git fixtures, which no agent flow touches.
//
// notifyAllow re-admits destination ranges notification webhooks may
// reach (config.Config's own NotifyAllowCIDRs, empty — so every loopback
// destination is refused — unless a caller passes some), the same
// ansible_test.go's wiring.notifyAllow exists for. Variadic and almost
// always omitted: only a scenario whose channel must actually be
// deliverable, not merely accepted, needs it (alertroutes_test.go's own
// webhook receiver).
func wireAgentManager(t *testing.T, ctx context.Context, notifyAllow ...netip.Prefix) agentWorld {
	t.Helper()

	httpLis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcLis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	masterKey := [32]byte{'e', '2', 'e', '-', 'a', 'g', 'e', 'n', 't', '-', 'k', 'e', 'y', '0', '0', '1'}
	cfg := config.Config{
		Listen:      httpLis.Addr().String(),
		DatabaseURL: testdb.URL(t),
		PublicURL:   "http://localhost:" + portOf(httpLis.Addr().String()),
		MasterKey:   masterKey,
		// "localhost" is the CA's SAN and the name the agent's TLS
		// verification expects; the harness serves on its own listener,
		// exactly like cmd/manager with PULSIX_GRPC_LISTEN.
		GRPCListen:       "localhost:0",
		GRPCPublicAddr:   "localhost:" + portOf(grpcLis.Addr().String()),
		RetestInterval:   24 * time.Hour,
		JobMaxAttempts:   config.DefaultJobMaxAttempts,
		Version:          "e2e",
		NotifyAllowCIDRs: notifyAllow,
	}
	a, err := app.Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	go func() { _ = a.GRPC.Serve(grpcLis) }()
	t.Cleanup(func() {
		stopped := make(chan struct{})
		go func() { a.GRPC.GracefulStop(); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(3 * time.Second):
			a.GRPC.Stop()
		}
	})
	httpSrv := &http.Server{Handler: a.Handler}
	go func() { _ = httpSrv.Serve(httpLis) }()
	t.Cleanup(func() { _ = httpSrv.Close() })

	org := "e2e-" + strings.ReplaceAll(uuid.Must(uuid.NewV7()).String()[:8], "-", "")
	api := newAPIClient(t, "http://localhost:"+portOf(httpLis.Addr().String()))
	api.do(http.MethodPost, "/api/v1/setup/complete", map[string]any{
		"admin_email":    "root@e2e.test",
		"admin_password": "correct horse battery",
		"admin_name":     "E2E Admin",
		"org_slug":       org,
		"org_name":       "E2E Org",
	}, nil)
	var enrolOut struct {
		Secret string `json:"secret"`
	}
	api.do(http.MethodPost, "/api/v1/auth/totp/enrol", struct{}{}, &enrolOut)
	code, err := totp.GenerateCode(enrolOut.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	api.do(http.MethodPost, "/api/v1/auth/totp/confirm", map[string]string{"code": code}, nil)

	var zonesOut struct {
		Zones []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"zones"`
	}
	api.do(http.MethodGet, "/api/v1/orgs/"+org+"/zones", nil, &zonesOut)
	zoneID := ""
	for _, z := range zonesOut.Zones {
		if z.Slug == "default" {
			zoneID = z.ID
		}
	}
	if zoneID == "" {
		t.Fatal("the organization carries no default zone after setup")
	}
	return agentWorld{api: api, org: org, zoneID: zoneID, grpcAddr: "localhost:" + portOf(grpcLis.Addr().String())}
}

// createToken asks the manager for an enrollment token the way the Add
// agent dialog does. hostID empty leaves it unbound.
func (w agentWorld) createToken(t *testing.T, hostID, nameHint string) string {
	t.Helper()
	body := map[string]any{"zone_id": w.zoneID, "max_uses": 1, "expires_in_minutes": 30}
	if hostID != "" {
		body["host_id"] = hostID
	}
	if nameHint != "" {
		body["name_hint"] = nameHint
	}
	var out struct {
		Token          string `json:"token"`
		InstallCommand string `json:"install_command"`
	}
	w.api.do(http.MethodPost, "/api/v1/orgs/"+w.org+"/agents/tokens", body, &out)
	if !strings.HasPrefix(out.Token, "pxa1.") {
		t.Fatalf("token = %q, want an agent (pxa1) token", out.Token)
	}
	if !strings.Contains(out.InstallCommand, out.Token) || !strings.Contains(out.InstallCommand, "/install.sh") {
		t.Fatalf("install command = %q, want it to carry the token and the script URL", out.InstallCommand)
	}
	return out.Token
}

// agents reads the Agents page's list.
func (w agentWorld) agents(t *testing.T) []agentDTO {
	t.Helper()
	var out struct {
		Agents []agentDTO `json:"agents"`
	}
	w.api.do(http.MethodGet, "/api/v1/orgs/"+w.org+"/agents", nil, &out)
	return out.Agents
}

// startAgent runs one real agent loop against the manager, with its own
// data dir, and returns a stop function. The loop's error is reported
// unless it is the clean shutdown.
func startAgent(t *testing.T, w agentWorld, dir, token, name string, opts ...agent.LoopOption) (stop func()) {
	t.Helper()
	cfg, err := agent.Load([]string{
		"PULSIX_MANAGER_ADDR=" + w.grpcAddr,
		"PULSIX_MANAGER_SERVER_NAME=localhost",
		"PULSIX_ENROLL_TOKEN=" + token,
		"PULSIX_AGENT_NAME=" + name,
		"PULSIX_DATA_DIR=" + dir,
		"PULSIX_HEARTBEAT_INTERVAL=5s",
	})
	if err != nil {
		t.Fatalf("agent config: %v", err)
	}
	cfg.Version = "v0.3a-e2e"
	ctx, cancel := context.WithCancel(context.Background())
	// The loop's own logger is discarded: a failing gate reports through
	// assertions, and the agent logs a reconnect per backoff step.
	opts = append(opts, agent.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	done := make(chan error, 1)
	go func() { done <- agent.NewLoop(cfg, agent.NewDialer(cfg), facts.New(cfg.Version), opts...).Run(ctx) }()
	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("agent %s loop: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("agent %s did not shut down", name)
		}
	}
}

// waitForAgent polls the Agents page until pred holds, which is how an
// operator learns an agent arrived: nothing pushes to the page.
func waitForAgent(t *testing.T, w agentWorld, what string, pred func([]agentDTO) bool) []agentDTO {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last []agentDTO
	for time.Now().Before(deadline) {
		last = w.agents(t)
		if pred(last) {
			return last
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; agents = %+v", what, last)
	return nil
}

func TestAgentEnrollmentGate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := wireAgentManager(t, ctx)

	// --- 1. An unbound token creates its host ---------------------------
	createdDir := t.TempDir()
	created := startAgent(t, w, createdDir, w.createToken(t, "", "e2e-created"), "e2e-created")
	list := waitForAgent(t, w, "the created agent", func(as []agentDTO) bool {
		return len(as) == 1 && as[0].Status == "online" && as[0].LastSeen != nil
	})
	first := list[0]
	if first.AgentName != "e2e-created" || first.Version != "v0.3a-e2e" || first.CertExpiresAt == nil {
		t.Fatalf("created agent = %+v", first)
	}
	if first.HostName != "e2e-created" {
		t.Errorf("host name = %q, want the token's name hint", first.HostName)
	}

	// The host it created is monitored and carries the facts the agent
	// collected from this machine, with the automatic tags derived from
	// them (os is the one every platform reports).
	// The facts ride their own message on the stream: the heartbeat that
	// made the agent "online" says nothing about them having arrived yet,
	// so the read waits for them rather than racing the push.
	host := waitForFacts(t, w, first.HostID)
	if !host.Host.Monitored {
		t.Error("the enrolled host is not monitored")
	}
	for _, key := range []string{"os", "arch", "hostname", "agent_version"} {
		if host.Host.Facts[key] == "" {
			t.Errorf("fact %q is empty; facts = %v", key, host.Host.Facts)
		}
	}
	if got := host.Host.Facts["agent_version"]; got != "v0.3a-e2e" {
		t.Errorf("agent_version fact = %q, want the running build", got)
	}
	var tags struct {
		Tags []struct {
			Key, Value, Source string
		} `json:"tags"`
	}
	w.api.do(http.MethodGet, "/api/v1/orgs/"+w.org+"/hosts/"+first.HostID+"/tags", nil, &tags)
	osTag := ""
	for _, tag := range tags.Tags {
		if tag.Key == "os" {
			osTag = tag.Value
			if tag.Source != "fact" {
				t.Errorf("os tag source = %q, want fact", tag.Source)
			}
		}
	}
	if osTag == "" || osTag != host.Host.Facts["os"] {
		t.Errorf("os tag = %q, want the reported fact %q", osTag, host.Host.Facts["os"])
	}

	// The host card shows the same agent the list does.
	var card struct {
		Agent agentDTO `json:"agent"`
	}
	w.api.do(http.MethodGet, "/api/v1/orgs/"+w.org+"/hosts/"+first.HostID+"/agent", nil, &card)
	if card.Agent.AgentName != first.AgentName || card.Agent.Status != "online" {
		t.Errorf("host card = %+v", card.Agent)
	}

	// --- 2. A bound token binds to the host an operator pre-created -----
	var boundHost struct {
		Host struct {
			ID string `json:"id"`
		} `json:"host"`
	}
	w.api.do(http.MethodPost, "/api/v1/orgs/"+w.org+"/hosts",
		map[string]any{"name": "e2e-preexisting", "zone_id": w.zoneID}, &boundHost)
	bound := startAgent(t, w, t.TempDir(), w.createToken(t, boundHost.Host.ID, ""), "e2e-bound")
	waitForAgent(t, w, "the bound agent", func(as []agentDTO) bool {
		for _, a := range as {
			if a.HostID == boundHost.Host.ID && a.Status == "online" {
				return true
			}
		}
		return false
	})
	if got := len(w.agents(t)); got != 2 {
		t.Fatalf("agents = %d, want 2 — a bound token must not create a host", got)
	}
	bound()

	// --- 3. Renewal swaps the certificate under a running agent ---------
	// The agent renews inside the last third of its 90-day certificate, so
	// the gate moves the agent's own clock there instead of waiting: the
	// manager still sees a valid certificate on its real clock, which is
	// exactly the situation a renewal happens in.
	dir := t.TempDir()
	renewing := startAgent(t, w, dir, w.createToken(t, "", "e2e-renew"), "e2e-renew")
	waitForAgent(t, w, "the renewing agent's first connection", func(as []agentDTO) bool {
		return len(as) == 3
	})
	before := serialOf(t, dir)
	renewing()

	renewed := startAgent(t, w, dir, "", "e2e-renew",
		agent.WithNow(func() time.Time { return time.Now().Add(80 * 24 * time.Hour) }))
	deadline := time.Now().Add(30 * time.Second)
	after := before
	for time.Now().Before(deadline) && after == before {
		time.Sleep(200 * time.Millisecond)
		after = serialOf(t, dir)
	}
	if after == before {
		t.Fatalf("the certificate was not renewed (serial stayed %s)", before)
	}
	renewed()
	// The manager knows the agent under its new certificate: the renewed
	// identity still opens a stream.
	stillWorks := startAgent(t, w, dir, "", "e2e-renew")
	waitForAgent(t, w, "the renewed agent to reconnect", func(as []agentDTO) bool {
		for _, a := range as {
			if a.AgentName == "e2e-renew" && a.Status == "online" {
				return true
			}
		}
		return false
	})
	stillWorks()

	// --- 4. Revocation ends the agent ------------------------------------
	created()
	w.api.do(http.MethodPost, "/api/v1/orgs/"+w.org+"/agents/"+first.HostID+"/revoke", nil, nil)
	for _, a := range w.agents(t) {
		if a.HostID == first.HostID {
			t.Fatal("the revoked agent is still listed")
		}
	}
	host = hostOf(t, w, first.HostID)
	if host.Host.Monitored {
		t.Error("the host stayed monitored after its agent was revoked")
	}
	// The certificate itself is refused from now on — the binding being
	// gone from the page is not the same claim. This is the revoked
	// agent's own identity, dialing the way it would after a reconnect.
	assertSerialRefused(t, w, createdDir)
}

// assertSerialRefused opens a stream with the identity persisted in dir
// and requires the manager to refuse it as unauthenticated.
func assertSerialRefused(t *testing.T, w agentWorld, dir string) {
	t.Helper()
	cfg, err := agent.Load([]string{
		"PULSIX_MANAGER_ADDR=" + w.grpcAddr,
		"PULSIX_MANAGER_SERVER_NAME=localhost",
		"PULSIX_AGENT_NAME=revoked",
		"PULSIX_DATA_DIR=" + dir,
	})
	if err != nil {
		t.Fatalf("agent config: %v", err)
	}
	id, err := agent.LoadIdentity(dir)
	if err != nil || id == nil {
		t.Fatalf("load revoked identity: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := agent.NewDialer(cfg).DialAgent(ctx, id)
	if err != nil {
		t.Fatalf("dial with the revoked identity: %v", err)
	}
	defer func() { _ = conn.Close() }()
	stream, err := conn.AgentStream(ctx)
	if err == nil {
		// The dial is lazy: the refusal surfaces on the first message.
		_, err = stream.Recv()
	}
	if code := status.Code(err); code != codes.Unauthenticated {
		t.Fatalf("stream with the revoked certificate = %v (code %s), want Unauthenticated", err, code)
	}
}

// hostRead is the host detail the gate asserts against.
type hostRead struct {
	Host struct {
		ID        string            `json:"id"`
		Monitored bool              `json:"monitored"`
		Facts     map[string]string `json:"facts"`
	} `json:"host"`
}

func hostOf(t *testing.T, w agentWorld, hostID string) hostRead {
	t.Helper()
	var out hostRead
	w.api.do(http.MethodGet, "/api/v1/orgs/"+w.org+"/hosts/"+hostID, nil, &out)
	return out
}

// waitForFacts polls the host until the agent's first facts push has
// landed. An agent reports its heartbeat and its facts as two separate
// messages, so "online" arrives strictly before the facts do.
func waitForFacts(t *testing.T, w agentWorld, hostID string) hostRead {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last hostRead
	for time.Now().Before(deadline) {
		last = hostOf(t, w, hostID)
		if last.Host.Facts["os"] != "" {
			return last
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for the agent's facts; host = %+v", last.Host)
	return last
}

// serialOf reads the serial of the certificate persisted in dir — the
// handle the manager knows an agent by, and what a renewal changes.
func serialOf(t *testing.T, dir string) string {
	t.Helper()
	id, err := agent.LoadIdentity(dir)
	if err != nil || id == nil {
		t.Fatalf("load identity from %s: %v", dir, err)
	}
	return id.Leaf().SerialNumber.String()
}
