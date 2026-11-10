// Package apitest spins up the full API against the test database.
package apitest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/accounts"
	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/alertroutes"
	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/api"
	"github.com/noixdev/pulsix/internal/app"
	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/backup"
	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/calendar"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/clientip"
	"github.com/noixdev/pulsix/internal/config"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/items"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/mail"
	"github.com/noixdev/pulsix/internal/maintenance"
	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/oncall"
	"github.com/noixdev/pulsix/internal/outbound"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/rules"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/setup"
	"github.com/noixdev/pulsix/internal/shared"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/tenancy"
	"github.com/noixdev/pulsix/internal/triggers"
	"github.com/noixdev/pulsix/internal/workers"
)

// SentMail is one message captured by Mailer.
type SentMail struct{ To, Subject, Body string }

// Mailer is a recording tenancy.Mailer used in place of the real SMTP
// sender Task 12 wires in production. It is safe for concurrent use since
// the httptest server may serve requests on more than one goroutine.
type Mailer struct {
	mu   sync.Mutex
	sent []SentMail
	fail error
	// unconfigured makes Configured report an instance without outgoing
	// mail (SetConfigured); the zero value is a configured instance.
	unconfigured bool
}

// Send records the message and succeeds, unless FailWith set an error to
// return instead (in which case nothing is recorded).
func (m *Mailer) Send(_ context.Context, to, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	m.sent = append(m.sent, SentMail{To: to, Subject: subject, Body: body})
	return nil
}

// SendMany records one message addressed to every recipient (To joined
// with ", "), like mail.SMTP.SendMany sends one.
func (m *Mailer) SendMany(ctx context.Context, to []string, subject, body string) error {
	return m.Send(ctx, strings.Join(to, ", "), subject, body)
}

// Configured reports whether the fake instance has outgoing mail.
func (m *Mailer) Configured(context.Context) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.unconfigured, nil
}

// SetConfigured switches the fake instance's outgoing mail on or off.
func (m *Mailer) SetConfigured(ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unconfigured = !ok
}

// FailWith makes every later Send return err — e.g. an error wrapping
// mail.ErrNotConfigured for an instance without outbound mail, or any other
// error for a delivery failure. nil restores normal recording.
func (m *Mailer) FailWith(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fail = err
}

// Sent returns every message recorded so far, oldest first.
func (m *Mailer) Sent() []SentMail {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SentMail, len(m.sent))
	copy(out, m.sent)
	return out
}

// testMasterKey is a fixed, non-secret key used only to seal TOTP secrets in
// tests. Never used outside apitest.
var testMasterKey = [32]byte{'p', 'u', 'l', 's', 'i', 'x', '-', 't', 'e', 's', 't', '-', 'k', 'e', 'y'}

// SetupToken is the setup token of testMasterKey, which POST
// /api/v1/setup/complete requires on every server New builds.
func SetupToken() string { return setup.Token(testMasterKey) }

// Server is a running API plus helpers.
type Server struct {
	T        *testing.T
	DB       *db.DB
	Q        *gen.Queries
	Sessions *auth.Sessions
	Mail     *Mailer
	// Queue is the job queue itself, for the tests that drive Claim /
	// Finish / AppendEvent directly (the claim-driven flows the API
	// cannot reach): fixture-only, exactly like Q.
	Queue *jobs.SQLQueue
	Deps  api.Deps
	HTTP  *httptest.Server
	// BackupDir is the folder the default Deps.Backups writes its backups
	// to (backup.DestinationFolder), a fresh temporary directory.
	BackupDir string
	// Checks is the monitoring check pipeline's service (v0.3b-1), the
	// same instance Deps.Checks carries — exposed here, exactly like
	// Queue, so SeedOrg can give a fixture org the "Linux base" seed set
	// organization creation gives a real one, and so a test can seed
	// samples or definitions directly through the service the routes
	// under test read from.
	Checks *checks.Service
	// Rules is the rule engine's service (v0.3b-2), the same instance
	// Deps.Rules carries — exposed here, exactly like Checks, so SeedOrg
	// can give a fixture org the five-row starter rule set a real
	// organization gets, and so a test can seed rules directly through the
	// service the routes under test read from.
	Rules *rules.Service
	// Items is discovery and item decisions' service (v0.3b-3), the same
	// instance Deps.Items carries — exposed here, exactly like Checks and
	// Rules, so a test can seed decisions directly through the service the
	// routes under test read from. Discovering an item is not exposed on
	// Service at all (only the evaluation ticker calls Store.Discover), so
	// a test that needs one to exist builds its own items.NewSQLStore(s.DB)
	// and calls Discover directly, scoped with db.WithOrgScope — exactly
	// how checksFixture.record seeds observations through s.Checks instead
	// of a second, parallel ingest path.
	Items *items.Service
	// Shared is the installation-owned definition/projection service
	// (v0.2f), the same instance Deps.Shared carries — exposed here,
	// exactly like Checks/Rules/Items, so SeedOrg can give a fixture org
	// whatever repository definitions already exist the way a real
	// organization's own creation hook would, and so a test can define a
	// shared repository directly through the service the instance-admin
	// routes under test read from.
	Shared *shared.Service
	// Alerts is the alert lifecycle's own service (v0.4a), the same
	// instance Deps.Alerts carries. Opening an alert is not exposed on
	// Service at all (only Apply, driven by a rules evaluation, does
	// that) — a test that needs an open alert to exist builds its own
	// alerts.NewSQLStore(s.DB) and drives Execute directly on a bare tx,
	// exactly the shape internal/alerts' own store_sql_test.go uses.
	Alerts *alerts.Service
	// AlertRoutes is the bridge between monitoring and delivery (v0.4b
	// Task 10), the same instance Deps.AlertRoutes carries — exposed here,
	// exactly like Checks/Rules/Items/Alerts, so a test can seed or inspect
	// routes directly through the service the routes under test read from.
	AlertRoutes *alertroutes.Service
	// Oncall is the on-call rotation service (v0.4c Task 8), the same
	// instance Deps.Oncall carries — exposed here, exactly like
	// Checks/Rules/Items/Alerts/AlertRoutes, so a test can seed or inspect
	// rotations directly through the service the routes under test read
	// from.
	Oncall *oncall.Service
	// Push records every check-configuration push the check routes make
	// after a write commits (api.Deps.AgentPush). Production wires
	// *agentapi.Service here; a test has no agent stream to observe, so
	// it asserts on what the API asked for instead.
	Push *PushRecorder
	// HostReads counts the reads Deps.Hosts makes through its store, by
	// store method — including under WithHostsStore, which composes its
	// own wrapper on top of the counter rather than replacing it. It
	// exists so a test can pin how MANY queries a request costs, not only
	// what came back: the check list labels every host-bound definition
	// with its host's name, and a batched lookup and the N+1 loop it
	// replaced are indistinguishable from the response body alone
	// (task-10 review round 2, finding 2).
	//
	// Services other than Deps.Hosts hold the same *hosts.Service
	// instance and so are counted too; anything built on its own store is
	// not. Reset() before the request under test.
	HostReads *HostReadCounter
	// PublicURL is the test server's own base URL, computed from its
	// listener before Start() (so it is already known when an Option
	// runs) — the same value production derives PULSIX_PUBLIC_URL from,
	// for an Option that needs to rebuild a Deps field that carries it
	// (e.g. WithRestrictiveNotifyPolicy).
	PublicURL string
}

// HostReadCounter counts hosts.Store reads by method name. Safe for
// concurrent use: the API under test serves requests on its own goroutines.
type HostReadCounter struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *HostReadCounter) add(method string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == nil {
		c.n = map[string]int{}
	}
	c.n[method]++
}

// Reset forgets every count so far, so a test can measure one request
// rather than everything its fixture did first.
func (c *HostReadCounter) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n = map[string]int{}
}

// Count reports how many times the named store method was called.
func (c *HostReadCounter) Count(method string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[method]
}

// Total reports every counted read, whichever method it went through.
func (c *HostReadCounter) Total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for _, n := range c.n {
		total += n
	}
	return total
}

// countingHostStore is the real SQL store with its read paths counted. It
// embeds the interface rather than listing all ~25 methods, so a method
// added to hosts.Store needs no change here — only the reads a test cares
// about are intercepted, and everything else passes straight through.
type countingHostStore struct {
	hosts.Store
	counter *HostReadCounter
}

func (c countingHostStore) GetHost(ctx context.Context, orgID, hostID uuid.UUID) (hosts.Host, error) {
	c.counter.add("GetHost")
	return c.Store.GetHost(ctx, orgID, hostID)
}

func (c countingHostStore) ListAllHosts(ctx context.Context, orgID uuid.UUID) ([]hosts.Host, error) {
	c.counter.add("ListAllHosts")
	return c.Store.ListAllHosts(ctx, orgID)
}

func (c countingHostStore) ListHostsByIDs(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]hosts.Host, error) {
	c.counter.add("ListHostsByIDs")
	return c.Store.ListHostsByIDs(ctx, orgID, ids)
}

// Option customizes a Server's Deps before its httptest.Server starts.
type Option func(*Server)

// WithSecureCookies sets Deps.Secure, so tests can exercise the Secure
// attribute on the real Set-Cookie response of the login endpoint instead
// of only asserting it on code with no caller (Finding 3, fix round 1).
func WithSecureCookies() Option {
	return func(s *Server) { s.Deps.Secure = true }
}

// WithAuthRateLimit sets Deps.AuthRateLimit — the failed-auth-attempt
// budget per address per minute of the four rate-limited auth endpoints
// (login, TOTP verify, passkey login finish, invite accept; backlog 1.20).
// The apitest default leaves the limiter disabled (Deps.AuthRateLimit 0),
// so no existing test's logins are throttled; the rate-limit tests turn it
// on here with a small budget.
func WithAuthRateLimit(n int) Option {
	return func(s *Server) { s.Deps.AuthRateLimit = n }
}

// WithVersion sets Deps.Version, the manager's build version: the value the
// version_status of workers and agents is measured against. Left unset it
// is empty, which makes every status "unknown".
func WithVersion(version string) Option {
	return func(s *Server) { s.Deps.Version = version }
}

// WithAgentBinaries sets Deps.AgentBinaries — the agent distribution
// GET /agents/bin/{os}/{arch} serves. A test build embeds no real agent
// binaries (that needs `make agent-binaries`), so a test that wants the
// download route to serve something hands it a small in-memory filesystem
// named the way the distribution is: pulsix-agent-<os>-<arch>[.sha256].
func WithAgentBinaries(dist fs.FS) Option {
	return func(s *Server) { s.Deps.AgentBinaries = dist }
}

// WithTrustedProxies replaces Deps.ClientIP with a resolver trusting the
// given ranges (PULSIX_TRUSTED_PROXIES), so a test can drive the forwarded
// address the API's rate limiting keys on — the default resolver trusts
// nothing and counts every request under the loopback peer.
func WithTrustedProxies(trusted []netip.Prefix) Option {
	return func(s *Server) { s.Deps.ClientIP = clientip.New(trusted) }
}

// WithAuthWrapper replaces Deps.Auth with wrap's return value, applied to
// the default real auth.Service. It exists so a test can make one method
// fail on demand — the real service, backed by a working test database,
// otherwise has no easy way to fail (Finding 1's forced-revoke-failure
// test, Finding 6's unmapped-error test).
func WithAuthWrapper(wrap func(api.AuthService) api.AuthService) Option {
	return func(s *Server) { s.Deps.Auth = wrap(s.Deps.Auth) }
}

// failingAuditStore always fails Insert while forwarding List/ListForActor
// to a real store, so a test can drive the API normally (its reads keep
// working) while every Record call fails.
type failingAuditStore struct{ audit.Store }

func (failingAuditStore) Insert(context.Context, audit.Event) error {
	return errors.New("simulated audit store failure")
}

// WithFailingAudit replaces Deps.Audit with a recorder whose store always
// fails to Insert. It exists for
// TestRecordingFailureDoesNotBreakRequest (audit_handlers_test.go), the one
// test in this codebase for the tradeoff auditMiddleware's own doc comment
// describes: a mutating request's own effect and response must not depend
// on whether recording that it happened succeeds.
func WithFailingAudit() Option {
	return func(s *Server) {
		s.Deps.Audit = audit.NewRecorder(failingAuditStore{Store: audit.NewSQLStore(s.DB)}, time.Now)
	}
}

// WithHostsStore rebuilds Deps.Hosts on wrap's return value, applied to the
// real SQL store. It exists for the row-level-security proof
// (rls_test.go): a wrapper can run an arbitrary statement with the exact
// context a real org-scoped handler hands its store, and so observe what
// the database lets that request see. The org-created hooks keep pointing
// at the unwrapped service; both write through the same database.
//
// wrap is applied ON TOP of the counting store, not instead of it, so
// Server.HostReads keeps counting whatever reads wrap passes through. The
// alternative — rebuilding on a bare SQL store — would leave a test that
// used both options reading Total() == 0 forever, and every "want 0"
// assertion in it would pass for the wrong reason.
func WithHostsStore(wrap func(hosts.Store) hosts.Store) Option {
	return func(s *Server) {
		s.Deps.Hosts = hosts.NewService(wrap(countingHostStore{Store: hosts.NewSQLStore(s.DB), counter: s.HostReads}), time.Now)
	}
}

// WithRestrictiveNotifyPolicy rebuilds Deps.Notify's Sender on
// production's actual outbound policy — no PULSIX_NOTIFY_ALLOW_CIDRS
// entries, so loopback, link-local and the other internal ranges are all
// refused — instead of New's default loopback-admitting policy (needed so
// most tests' httptest webhook receivers, which bind to 127.0.0.1, stay
// reachable). It exists for the one test that must prove Send test really
// is refused for a loopback destination through the real outbound client,
// not just permitted by the test fixture's own relaxation of it.
func WithRestrictiveNotifyPolicy() Option {
	return func(s *Server) {
		box := crypto.NewBox(testMasterKey)
		store := notify.NewSQLStore(s.DB)
		s.Deps.Notify = notify.NewService(notify.Deps{
			Channels: store, Deliveries: store, Digests: store, Box: box, Mail: s.Mail,
			Sender:    notify.NewChannelSender(box, s.Mail, outbound.NewClient(outbound.NewPolicy(nil, nil), 5*time.Second, nil), time.Now),
			PublicURL: s.PublicURL, Now: time.Now, Rand: rand.Reader,
		})
	}
}

// New builds Deps from the test database and starts an httptest server.
//
// The WebAuthn relying party id and origin are derived from the test
// server's own URL via config.Config.RPID/Origin — the same derivation
// production uses from PULSIX_PUBLIC_URL — rather than hardcoding a literal
// origin that cannot be known before the server has a URL. That URL is not
// known until a listener exists, and building auth.Passkeys is what needs
// it, so the httptest.Server is created unstarted first (which does bind
// its listener up front) purely to learn its address; the real handler,
// built once Deps (including Passkeys) is complete, is only attached to it
// afterwards, before Start is called.
//
// httptest's default listener binds to the loopback IP (127.0.0.1), but
// go-webauthn (as of v0.18.1) rejects an RPID that is an IP address — it
// must be a domain string, per the spec. "localhost" resolves to the same
// loopback address on essentially every system without needing a listener
// bound to a literal hostname (net.Listen can't bind to "localhost" as
// such; it always resolves to an IP first), so PublicURL and s.HTTP.URL
// both use "localhost" with the listener's actual port instead of the
// address Start() would otherwise report.
func New(t *testing.T, opts ...Option) *Server {
	t.Helper()
	d := testdb.Open(t)
	// Q is for test fixtures only: it runs on the raw pool, outside any
	// organization scope. Every store below is built on d itself, exactly
	// like production wiring (internal/app.Build), so the API under test
	// runs each statement inside the request's organization.
	q := gen.New(d.Pool())
	appQ := gen.New(d)
	s := &Server{T: t, DB: d, Q: q, Sessions: auth.NewSessions(appQ, time.Now), Mail: &Mailer{}}

	ts := httptest.NewUnstartedServer(nil)
	_, port, err := net.SplitHostPort(ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	publicURL := "http://localhost:" + port
	s.PublicURL = publicURL
	cfg := config.Config{PublicURL: publicURL}

	totp := auth.NewTOTP(auth.NewSQLTOTPStore(d), crypto.NewBox(testMasterKey), "Pulsix Test", time.Now)
	passkeys, err := auth.NewPasskeys(auth.NewSQLPasskeyStore(appQ), auth.NewSQLStore(appQ), s.Sessions, d, cfg.RPID(), "Pulsix Test", cfg.Origin(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	// A passkey must count as a second factor at password login the same as
	// TOTP does, not only at the passkey login endpoints — otherwise a
	// passkey-only account's password alone would produce an MFA-complete
	// session (Finding 2, fix round 1).
	setupSvc := setup.NewService(setup.NewSQLStore(d), s.Sessions, crypto.NewBox(testMasterKey), time.Now, SetupToken())
	tenancySvc := tenancy.NewService(tenancy.NewSQLStore(d), s.Mail, publicURL, time.Now)
	s.HostReads = &HostReadCounter{}
	hostsSvc := hosts.NewService(countingHostStore{Store: hosts.NewSQLStore(d), counter: s.HostReads}, time.Now)
	// The monitoring check pipeline, wired exactly as app.Build wires it:
	// the same store, and the same two host ports (WithMatcher /
	// WithHostRoster) without which Preview and the resolver refuse to
	// run — the check routes call both, so a fixture missing them would
	// pass tests the real manager could not.
	s.Checks = checks.NewService(checks.NewSQLStore(d),
		checks.WithMatcher(app.NewChecksHostMatcher(hostsSvc)),
		checks.WithHostRoster(app.NewChecksHostRoster(hostsSvc)))
	// The rule engine (v0.3b-2): SeedForOrg is wired into the hook right
	// below, alongside checksSvc's own, exactly like production wiring
	// (internal/app/app.go) — rulesSvc needs no host ports of its own,
	// since SeedForOrg only ever writes org-scoped rows.
	s.Rules = rules.NewService(rules.NewSQLStore(d))
	// The API's read-only side onto the evaluator's own tables (v0.3b-2
	// Task 11), wired exactly like production (internal/app.Build): a
	// second, separately built *rules.SQLStore, since rules.StateStore is
	// deliberately narrower than the Store s.Rules already holds.
	ruleStates := rules.NewSQLStore(d)
	// Discovery and item decisions (v0.3b-3), wired exactly like production
	// wiring (internal/app.Build): the check-key set comes from the same
	// app.ItemCheckKeys adapter over rules.Catalogue(), so a test can never
	// validate a decision against a different check-key universe than the
	// manager actually runs.
	s.Items = items.NewService(items.NewSQLStore(d), app.ItemCheckKeys)
	s.Push = &PushRecorder{}
	// Installation-owned repository definitions (v0.2f), wired exactly like
	// production (internal/app.Build): sharedSvc needs no org-scoped ports
	// of its own, built here for the organization-created hook right below.
	s.Shared = shared.NewService(shared.NewSQLStore(d))
	// Every organization gets its "default" zone, its check definitions
	// seed set, its rule starter set and a projection of every existing
	// repository, job template AND check definition through this hook,
	// whichever of the two ways it was created — POST /orgs
	// (tenancy.Service.CreateOrg) or the first-run wizard
	// (setup.Service.Complete) — exactly like production wiring
	// (internal/app/app.go) chains all six hooks the same way, rules after
	// checks so its rules can always assume the checks their metrics
	// belong to already exist, and shared's three last (repositories, then
	// templates, then check definitions — app.go's own ordering note on
	// why check definitions run last) since propagation order among the
	// three never depends on anything this fixture does differently from
	// production. A whole-branch review found this chain missing the sixth
	// hook (ProjectAllCheckDefinitionsInto): the mismatch cost nothing
	// visible because every check-definition test that needed a projection
	// called the service directly, but it meant deleting app.go's own
	// argument would have left the entire suite green — see
	// TestOrgCreationProjectsAnExistingSharedCheckDefinition
	// (shared_check_handlers_test.go), added to close exactly that gap.
	orgCreated := app.ChainOrgCreatedHooks(
		hostsSvc.EnsureDefaultZone, s.Checks.SeedForOrg, s.Rules.SeedForOrg,
		s.Shared.ProjectAllInto, s.Shared.ProjectAllTemplatesInto, s.Shared.ProjectAllCheckDefinitionsInto,
	)
	tenancySvc.SetOrgCreatedHook(orgCreated)
	setupSvc.SetOrgCreatedHook(orgCreated)
	credsSvc := credentials.NewService(credentials.NewSQLStore(d), crypto.NewBox(testMasterKey), time.Now, rand.Reader)
	sshSvc := sshconf.NewService(sshconf.NewSQLStore(d), credsSvc, hostsSvc)
	invStore := inventory.NewSQLStore(d)
	inventorySvc := inventory.NewService(invStore, hostsSvc, sshSvc, crypto.NewBox(testMasterKey))
	renderer := inventory.NewRenderer(invStore, hostsSvc, sshSvc, credsSvc, crypto.NewBox(testMasterKey))
	// The job queue rides the same services the renderer needs, exactly
	// like production wiring (internal/app.Build): the template lookup
	// adapter wraps the templates and playbooks stores, and the reaper is
	// NOT started here — tests drive Claim/Finish through Deps' services or
	// the queue directly, and a background reaper would sweep jobs out from
	// under running assertions.
	templateStore := templates.NewSQLStore(d)
	playbooksStore := playbooks.NewSQLStore(d)
	templateLookup := app.NewTemplateLookup(templateStore, playbooksStore, crypto.NewBox(testMasterKey))
	jobsQueue := jobs.NewSQLQueue(d, crypto.NewBox(testMasterKey), renderer, credsSvc, templateLookup, sshSvc, time.Now, rand.Reader)
	resolver := jobs.NewScopeResolver(inventorySvc, inventorySvc, hostsSvc)
	jobsSvc := jobs.NewService(jobs.NewSQLStore(d), jobsQueue, templateLookup, resolver, hostsSvc, hostsSvc, crypto.NewBox(testMasterKey), time.Now, rand.Reader)
	// The same post-construction wiring production uses (internal/app):
	// the claim reads host-key pins from the hosts service, a finished
	// connection_test job records its outcome through it, and the rotation
	// ports close the credential-rotation loop.
	jobsQueue.SetKeyPins(hostsSvc)
	jobsQueue.SetTestSink(hostsSvc)
	// The run events' SSE fan-out: the service owns the subscriber
	// registry, the queue's append path feeds it — the same post-
	// construction wiring production uses (internal/app).
	jobsQueue.SetEventBroker(jobsSvc.EventsBroker())
	credsSvc.SetRotationEnqueuer(jobsSvc)
	jobsQueue.SetRotationFinisher(credsSvc)
	reposSvc := repos.NewService(repos.NewSQLStore(d), credsSvc, jobsSvc)
	// A repo playbook's claim needs its repository and the vault-identity
	// merge, exactly like production wiring (internal/app.Build's own
	// SetRepoSource/SetVaultResolver, right after reposSvc exists there for
	// the same circularity reason). Without this, Queue.Claim fails closed
	// on ANY repo-backed run job ("no repo source wired") — a gap that cost
	// nothing visible only because no test claimed one before backlog 2.91's
	// own test did (TestRunDetailStillShowsTheClaimedVaultIdentitiesAfterARepoEdit,
	// internal/api/job_handlers_test.go).
	jobsQueue.SetRepoSource(apitestRepoRefSource{reposSvc})
	jobsQueue.SetVaultResolver(apitestVaultResolver{})
	playbooksSvc := playbooks.NewService(playbooksStore, reposSvc, s.Shared)
	templatesSvc := templates.NewService(templateStore, playbooksSvc, inventorySvc, hostsSvc, credsSvc, crypto.NewBox(testMasterKey))
	// The automatic apply of variable sources, as production wires it
	// (internal/app.Build, backlog 7.75).
	jobsQueue.SetSourceApplier(templatesSvc)
	playbooksSvc.SetInlineApplier(templatesSvc)
	auditRec := audit.NewRecorder(audit.NewSQLStore(d), time.Now)
	// The schedules service (Task 7): fires job templates on a timetable
	// through the same org-scoped jobs.Service every other trigger uses,
	// exactly like production wiring (internal/app.Build) — the ticker
	// itself is not started here, the same way the reaper is not.
	schedulesSvc := schedules.NewService(schedules.NewSQLStore(d), templatesSvc, jobsSvc, auditRec, time.Now, rand.Reader, nil)
	// The triggers service (Task 10): the webhook receive pipeline behind
	// POST /hooks/jobs/{id} and POST /hooks/repos/{id}, built exactly like
	// production wiring (internal/app.Build) with PublicURL derived from
	// this test server's own URL, so Created.URL from a test-created
	// trigger already points at it.
	triggersSvc := triggers.NewService(triggers.Deps{
		Store: triggers.NewSQLStore(d), Templates: templatesSvc, Repos: reposSvc, Box: crypto.NewBox(testMasterKey),
		Runs: jobsSvc, Audit: auditRec, PublicURL: publicURL, IPLimit: clientip.NewLimiter(triggers.HookIPLimit, time.Minute, time.Now),
		Now: time.Now, Rand: rand.Reader,
	})
	// The worker registry (Task 15) signs with a CA stored on the test
	// database, as in production (internal/app.Build).
	testCA, err := ca.New(ca.NewSQLStore(d), crypto.NewBox(testMasterKey), ca.Config{CommonName: "pulsix-manager", ListenHost: "pulsix-manager"})
	if err != nil {
		t.Fatal(err)
	}
	if err := testCA.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	workersSvc := workers.NewService(workers.NewSQLStore(d), workers.WithSigner(testCA), workers.WithZones(hostsSvc))
	// The notification service (v0.2d): channels send through the
	// recording Mailer and an outbound client that admits loopback, so a
	// test's httptest receiver is reachable — production refuses loopback
	// unless PULSIX_NOTIFY_ALLOW_CIDRS lists it.
	notifyStore := notify.NewSQLStore(d)
	loopback := []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	notifySvc := notify.NewService(notify.Deps{
		Channels: notifyStore, Deliveries: notifyStore, Rules: notifyStore, Digests: notifyStore, Box: crypto.NewBox(testMasterKey), Mail: s.Mail,
		Sender: notify.NewChannelSender(crypto.NewBox(testMasterKey), s.Mail,
			outbound.NewClient(outbound.NewPolicy(loopback, nil), 5*time.Second, nil), time.Now),
		PublicURL: publicURL, Now: time.Now, Rand: rand.Reader,
	})
	// The maintenance service (v0.2d): group windows resolve through the
	// same scope resolver runs use, exactly like production wiring
	// (internal/app.Build).
	maintenanceSvc := maintenance.NewService(maintenance.NewSQLStore(d), app.NewGroupHosts(resolver), time.Now, rand.Reader, nil)
	schedulesSvc.SetMaintenance(maintenanceSvc)
	// The alert lifecycle's own service (v0.4a Task 8), wired exactly like
	// production (internal/app.Build): store plus clock, nothing else —
	// Apply and Sweep are driven by the evaluation ticker, not exercised
	// here, the same way the schedule/notification tickers above are not
	// started either.
	s.Alerts = alerts.NewService(alerts.NewSQLStore(d), time.Now)
	// The alert-route surface (v0.4b Task 10), wired exactly like
	// production (internal/app.Build): GroupsForHost comes from the same
	// checksSvc instance through app.NewAlertRouteGroups, the identical
	// adapter the follower uses for the same lookup — the follower itself
	// is not started here, like the reaper and the other tickers above.
	alertRoutesStore := alertroutes.NewSQLStore(d)
	s.AlertRoutes = alertroutes.NewService(alertRoutesStore, app.NewAlertRouteGroups(s.Checks), time.Now, uuid.New)
	// s.Alerts' own notification-hold port (v0.4c Task 11,
	// alerts.NotifyLedger), wired exactly like production
	// (internal/app.Build): the same store above, since *alertroutes.Store
	// already satisfies the interface.
	s.Alerts.SetNotifyLedger(alertRoutesStore)
	// The on-call rotation surface (v0.4c Task 8), wired exactly like
	// production (internal/app.Build): the same store a real manager's
	// follower would resolve a route's rotation through.
	s.Oncall = oncall.NewService(oncall.NewSQLStore(d), time.Now, uuid.New)
	// A finished scheduled run group queues its notifications, as in
	// production; the dispatcher is not started here (like the reaper).
	jobsQueue.SetRunGroupSink(notify.NewOutbox(d, publicURL, time.Now, rand.Reader, nil))
	s.Queue = jobsQueue
	agentsSvc := agents.NewService(agents.NewSQLStore(d), agents.WithProvisioner(app.NewAgentHostProvisioner(hostsSvc)),
		agents.WithCA(testCA))
	s.Deps = api.Deps{
		Sessions: s.Sessions, Auth: auth.NewService(auth.NewSQLStore(appQ), s.Sessions, auth.AnyEnabled(totp, passkeys), time.Now), TOTP: totp, Passkeys: passkeys,
		Tokens:    auth.NewTokens(auth.NewSQLTokenStore(d), time.Now),
		Enrolment: auth.NewEnrolment(totp, passkeys, s.Sessions, d),
		Tenancy:   tenancySvc,
		Accounts:  accounts.NewService(accounts.NewSQLStore(d), s.Mail, publicURL, time.Now, rand.Reader),
		Audit:     auditRec,
		ClientIP:  clientip.New(nil),
		Setup:     setupSvc,
		// Wired to the same setup.Service, so POST /admin/settings/smtp/test
		// exercises whatever a test actually stored through
		// PUT /admin/settings/smtp — no separate fake needed, since the
		// real *mail.SMTP simply fails to dial when nothing is configured
		// (or the test's own tests want that failure).
		Mailer:      mail.NewSMTP(setupSvc.LoadSMTP),
		Hosts:       hostsSvc,
		Credentials: credsSvc,
		SSH:         sshSvc,
		Inventory:   inventorySvc,
		Renderer:    renderer,
		Repos:       reposSvc,
		Shared:      s.Shared,
		Playbooks:   playbooksSvc,
		Templates:   templatesSvc,
		Jobs:        jobsSvc,
		Schedules:   schedulesSvc,
		Triggers:    triggersSvc,
		Workers:     workersSvc,
		// The agent registry, wired exactly as app.Build wires it (the
		// same store and host provisioner), so the API tests exercise the
		// real thing rather than a stand-in.
		Agents: agentsSvc,
		// The check pipeline and the write-path push hook it fires after
		// every mutating check route (api.CheckConfigPusher). The recorder
		// stands in for *agentapi.Service, which needs a live gRPC stream
		// to observe anything at all.
		Checks:     s.Checks,
		Rules:      s.Rules,
		RuleStates: ruleStates,
		// The items surface (v0.3b-3): the same instance exposed as s.Items,
		// and a real DirtyMarker on the test database — the item-decisions
		// routes' prompt-reevaluation effect is observable the same way
		// production reports it (a dirty check_eval_cursor row), not a
		// stand-in a test has to trust blindly.
		Items:       s.Items,
		EvalDirty:   rules.NewDirtyMarker(d),
		AgentPush:   s.Push,
		Notify:      notifySvc,
		Maintenance: maintenanceSvc,
		Calendar:    calendar.NewService(schedulesSvc, jobsSvc, maintenanceSvc, time.Now),
		Alerts:      s.Alerts,
		AlertRoutes: s.AlertRoutes,
		Oncall:      s.Oncall,
		Backups:     s.newBackups(),
		// What an enrollment snippet names; tests that need the worker
		// endpoint disabled clear it through an option.
		WorkerEndpoint: api.WorkerEndpoint{Addr: "localhost:8444", ServerName: "pulsix-manager"},
		// The same URL the passkey origin and the notification links use:
		// what GET /install.sh templates the agent download into.
		PublicURL: publicURL,
	}
	for _, opt := range opts {
		opt(s)
	}
	handler, err := api.New(s.Deps)
	if err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = handler
	ts.Start()
	ts.URL = publicURL
	t.Cleanup(ts.Close)
	s.HTTP = ts
	return s
}

// noBackupMail is the backup.AdminMailer of the test fixtures: nobody is
// told about a failed scheduled backup.
type noBackupMail struct{}

func (noBackupMail) SendToInstanceAdmins(context.Context, string, string) error { return nil }

// DumpBackup is backup.Dump bound to the test database and the fixtures'
// master key, the DumpFunc app.Build gives the manager.
func (s *Server) DumpBackup(ctx context.Context, w io.Writer, opts backup.DumpOptions) (backup.Header, error) {
	return backup.Dump(ctx, s.DB.Pool(), testMasterKey, w, opts)
}

// NewBackups builds a backup.Service on the test database that writes to
// dir, like the one app.Build wires with a folder in place of the
// container's /var/lib/pulsix/backups. dump is the service's DumpFunc,
// DumpBackup when nil. The service is stopped when the test ends.
func (s *Server) NewBackups(dir string, dump backup.DumpFunc) *backup.Service {
	if dump == nil {
		dump = s.DumpBackup
	}
	svc := backup.NewService(backup.NewSQLStore(s.DB), crypto.NewBox(testMasterKey), backup.HeaderKey(testMasterKey), dump, backup.StorageFor(dir),
		noBackupMail{}, "test", time.Now, nil)
	s.T.Cleanup(svc.Stop)
	return svc
}

func (s *Server) newBackups() api.BackupService {
	s.BackupDir = s.T.TempDir()
	return s.NewBackups(s.BackupDir, nil)
}

// Client returns an HTTP client with a cookie jar and, deliberately, a
// connection pool of its OWN.
//
// A client with no Transport uses http.DefaultTransport, whose pool is
// shared by every test in the binary while each test stands up and
// tears down its own httptest server. A pooled connection to a
// finished server's address outlives it, and the operating system
// hands the same localhost port to a later server — so a request can
// be sent down a socket belonging to a server that no longer exists
// and come back "connection reset by peer". Seen once, in
// TestAgentRoutesRoleMatrix, during a full-suite run that passed on
// its own afterwards; this removes the only route between two
// fixtures' servers I could find, rather than leaving a rare reset to
// be rediscovered later.
func (s *Server) Client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Transport: &http.Transport{DialContext: dialLoopbackV4}}
}

// dialLoopbackV4 sends every "localhost" connection straight to
// 127.0.0.1 (backlog 2.68).
//
// httptest listens on 127.0.0.1 only, while ts.URL names the same port
// as "localhost" — the spelling the manager's own PublicURL uses, which
// the passkey Origin check and every generated link compare against, so
// the URL cannot simply be changed. That leaves the client resolving
// the name, and on a machine with IPv6 loopback ::1 comes back ahead of
// 127.0.0.1: an address this listener never bound. Usually the IPv6
// attempt is refused and the dialer falls straight back, which is why
// it worked; under the full suite's load it intermittently did not, and
// a test failed with "connection reset by peer" against [::1]. Seen
// twice, in different tests, never when the package runs alone.
//
// Resolving it here keeps every URL, Origin and cookie domain exactly
// as the tests expect and removes the detour the listener never agreed
// to.
func dialLoopbackV4(ctx context.Context, network, addr string) (net.Conn, error) {
	if host, port, err := net.SplitHostPort(addr); err == nil && host == "localhost" {
		addr = net.JoinHostPort("127.0.0.1", port)
	}
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

// passwordHashCache memoizes HashPassword by plaintext password, for the
// lifetime of the test binary. auth.HashPassword runs the production
// argon2id parameters (internal/auth/password.go: time 3, memory 64 MiB, 2
// threads), which is the point of those parameters in production but makes
// it the dominant cost of `go test -race ./internal/api/...`: nearly every
// one of that package's tests calls SeedUser at least once, almost always
// with the same literal password, and hashing it fresh every time is what
// pushed the package past the race detector's 10-minute timeout.
//
// Reusing a cached hash across users seeded with the same password is safe:
// each hash produced by HashPassword still carries its own random salt (the
// cache stores the finished PHC string, not the salt or key material
// separately), so two users who happen to share a password in a test still
// get byte-identical stored hashes only because they share both password
// and cached salt — indistinguishable, for every assertion these tests
// make, from two users who coincidentally salted the same way. Every seeded
// hash still verifies exclusively through the real auth.VerifyPassword
// running the real production parameters; nothing here weakens what a login
// test exercises. A sync.Map is used, not a plain map, because apitest.New
// is called from many tests whose *testing.T.Parallel subtests can call
// SeedUser concurrently within one test binary.
var passwordHashCache sync.Map // string (password) -> string (PHC hash)

// hashPasswordCached returns HashPassword(password), computing it at most
// once per distinct password for the life of the test binary. See
// passwordHashCache for why reusing the hash (and so its salt) across
// seeded users is safe in tests.
func hashPasswordCached(password string) (string, error) {
	if cached, ok := passwordHashCache.Load(password); ok {
		return cached.(string), nil
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return "", err
	}
	// LoadOrStore so two goroutines racing to hash the same new password
	// converge on one winning hash instead of each seeding a different
	// (still individually valid) one.
	actual, _ := passwordHashCache.LoadOrStore(password, hash)
	return actual.(string), nil
}

// SeedUser inserts a user with a password.
func (s *Server) SeedUser(email, password string) uuid.UUID {
	s.T.Helper()
	hash, err := hashPasswordCached(password)
	if err != nil {
		s.T.Fatal(err)
	}
	id := uuid.Must(uuid.NewV7())
	if _, err := s.Q.CreateUser(context.Background(), gen.CreateUserParams{ID: id, Email: email, DisplayName: email, PasswordHash: &hash}); err != nil {
		s.T.Fatal(err)
	}
	return id
}

// SeedOrg inserts an org with ownerID as owner directly through s.Q
// (gen.CreateOrganization) rather than tenancy.Service.CreateOrg, so tests
// get a fixed, predictable slug instead of one CreateOrg would generate.
// That bypass is exactly why the calls below are not redundant with
// ChainOrgCreatedHooks (New, above): the hook is registered on tenancySvc/
// setupSvc, and only fires when creation goes through one of them — a
// direct s.Q.CreateOrganization call, like this one, never reaches it. So
// SeedOrg calls EnsureDefaultZone, checks' SeedForOrg, rules' SeedForOrg,
// shared's ProjectAllInto, shared's ProjectAllTemplatesInto AND shared's
// ProjectAllCheckDefinitionsInto itself, explicitly, the same six steps the
// hook would have run, so a seeded org still carries the same invariants a
// real one does (host_handlers_test.go relies on the zone;
// check_handlers_test.go and rule_handlers_test.go rely on their own seed
// sets the same way; a test that needs a seeded org to already carry an
// existing repository, job template or check definition's projection
// relies on these last three steps). A whole-branch review found this list
// stopping at templates, one short of app.go's own six — ChainOrgCreatedHooks
// above had the identical gap, and TestOrgCreationProjectsAnExistingSharedCheckDefinition
// (shared_check_handlers_test.go) is what closes both at once.
// TestOrgCreationSeedsCheckDefinitions and
// TestFirstRunWizardSeedsCheckDefinitions (internal/api/host_handlers_test.go)
// are what actually exercise the hook path end to end, through the real
// routes, in place of this shortcut.
func (s *Server) SeedOrg(slug string, ownerID uuid.UUID) uuid.UUID {
	s.T.Helper()
	ctx := context.Background()
	id := uuid.Must(uuid.NewV7())
	if _, err := s.Q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: id, Slug: slug, Name: slug}); err != nil {
		s.T.Fatal(err)
	}
	if err := s.Q.AddMembership(ctx, gen.AddMembershipParams{OrgID: id, UserID: ownerID, Role: gen.OrgRoleOwner}); err != nil {
		s.T.Fatal(err)
	}
	if err := s.Deps.Hosts.EnsureDefaultZone(ctx, id); err != nil {
		s.T.Fatal(err)
	}
	if err := s.Checks.SeedForOrg(ctx, id); err != nil {
		s.T.Fatal(err)
	}
	if err := s.Rules.SeedForOrg(ctx, id); err != nil {
		s.T.Fatal(err)
	}
	if err := s.Shared.ProjectAllInto(ctx, id); err != nil {
		s.T.Fatal(err)
	}
	if err := s.Shared.ProjectAllTemplatesInto(ctx, id); err != nil {
		s.T.Fatal(err)
	}
	if err := s.Shared.ProjectAllCheckDefinitionsInto(ctx, id); err != nil {
		s.T.Fatal(err)
	}
	return id
}

// AddMember adds userID to orgID with role, without going through the
// tenancy service or any invite flow — for tests that need a member at a
// specific role (not the owner SeedOrg already creates) with no interest in
// exercising ChangeRole/Invite themselves.
func (s *Server) AddMember(orgID, userID uuid.UUID, role tenancy.Role) {
	s.T.Helper()
	if err := s.Q.AddMembership(context.Background(), gen.AddMembershipParams{OrgID: orgID, UserID: userID, Role: gen.OrgRole(role)}); err != nil {
		s.T.Fatal(err)
	}
}

// uniqueSuffix returns a short, collision-safe string for a fixture that
// does not care about a specific value, only uniqueness — OrgWithToken and
// SeedChannel use it so a test can call them with no slug/name of its own
// to invent.
func uniqueSuffix() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
}

// OrgWithToken seeds a fresh owner user and organization and returns the
// org id and a bearer token scoped to the owner role — every permission,
// the default a CRUD happy-path test wants. Pair it with Get/Post/Patch
// below, which authenticate with the returned token rather than a cookie
// (bearerRequest's own shape, token_bearer_test.go), and see
// OrgWithTokenPerms for a token capped to fewer permissions.
func (s *Server) OrgWithToken(t *testing.T) (uuid.UUID, string) {
	t.Helper()
	return s.orgWithTokenRole(t, tenancy.RoleOwner)
}

// OrgWithTokenPerms is OrgWithToken, except the returned token is capped at
// the lowest role that grants every one of perms — the same tenancy.Allows
// matrix requireOrg itself checks against — so a test can prove that a
// caller holding exactly these permissions, and no more, is refused
// something that needs a permission not in the list (e.g.
// TestAlertRouteWriteNeedsTheChannelPermission: checks.read/checks.write
// but not channels.write).
func (s *Server) OrgWithTokenPerms(t *testing.T, perms ...tenancy.Permission) (uuid.UUID, string) {
	t.Helper()
	role := tenancy.RoleOwner
	for _, candidate := range []tenancy.Role{tenancy.RoleViewer, tenancy.RoleOperator, tenancy.RoleAdmin, tenancy.RoleOwner} {
		grantsAll := true
		for _, p := range perms {
			if !tenancy.Allows(candidate, p) {
				grantsAll = false
				break
			}
		}
		if grantsAll {
			role = candidate
			break
		}
	}
	return s.orgWithTokenRole(t, role)
}

// orgWithTokenRole seeds the user/org/token OrgWithToken and
// OrgWithTokenPerms both build on. The org's slug is rewritten to its own
// id (SeedOrg mints a random one) so the returned uuid.UUID can build both
// a request path (org.String(), since {org} is resolved by slug —
// orgScopeMiddleware/GetOrgBySlug) and every direct-service fixture call
// below (SeedChannel, SeedHost) that needs the real org id.
func (s *Server) orgWithTokenRole(t *testing.T, role tenancy.Role) (uuid.UUID, string) {
	t.Helper()
	suffix := uniqueSuffix()
	uid := s.SeedUser(suffix+"@example.test", "correct horse battery")
	org := s.SeedOrg("org-"+suffix, uid)
	if _, err := s.DB.Pool().Exec(context.Background(), `UPDATE organizations SET slug = $1 WHERE id = $2`, org.String(), org); err != nil {
		t.Fatal(err)
	}
	plain, _, err := s.Deps.Tokens.Create(context.Background(), uid, org, "ci", string(role), nil)
	if err != nil {
		t.Fatal(err)
	}
	return org, plain
}

// SeedChannel creates an enabled webhook notification channel directly
// through Deps.Notify (bypassing the API, like the Checks/Rules fixtures
// above) and returns its id — a channel id to name in an alert route.
func (s *Server) SeedChannel(t *testing.T, orgID uuid.UUID) uuid.UUID {
	t.Helper()
	url := "https://hooks.example.test/" + uuid.NewString()
	c, err := s.Deps.Notify.CreateChannel(context.Background(), orgID, uuid.Nil, notify.Input{
		Kind: notify.KindWebhook, Name: "channel-" + uniqueSuffix(), Enabled: true,
		Webhook: &notify.WebhookInput{URL: &url, BodyTemplate: `{"text": "test"}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// SeedHost creates a managed host in org's default zone (EnsureDefaultZone,
// run by SeedOrg/orgWithTokenRole) and returns its id.
func (s *Server) SeedHost(t *testing.T, orgID uuid.UUID) uuid.UUID {
	t.Helper()
	scoped := db.WithOrgScope(context.Background(), orgID)
	zones, err := s.Deps.Hosts.ListZones(scoped, orgID)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}
	h, err := s.Deps.Hosts.CreateHost(scoped, orgID, hosts.HostInput{
		Name: "host-" + uniqueSuffix(), Address: "10.0.0.1", ZoneID: zones[0].ID, Managed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h.ID
}

// SeedOpenAlertWithDeferredNotification opens one tuple-scope alert on a
// fresh host in org and records its own "opened" event as held in
// alertroutes' ledger (migration 00052/00054's alert_notify_state) in
// state 'deferred' — the fixture TestAlertDetailReportsAHeldNotification
// needs for "the alert has been open since three, why did nobody wake me"
// to have an answer to check against. Seeded directly through
// alerts.NewSQLStore and alertroutes.NewSQLStore on the server's own *db.DB
// — the same shape alert_handlers_test.go's own seedOpenAlert uses to open
// an alert (opening one is not exposed on alerts.Service at all, only
// Apply, driven by a rules evaluation, does that) — rather than through
// the follower itself: the follower's own tick is a later concern this
// fixture has no need to run.
func (s *Server) SeedOpenAlertWithDeferredNotification(t *testing.T, orgID uuid.UUID) uuid.UUID {
	t.Helper()
	host := s.SeedHost(t, orgID)
	sub := alerts.Subject{HostID: host, Scope: alerts.ScopeTuple, CheckKey: "disk", Item: "/", Metric: "used_pct"}
	now := time.Now().UTC()
	ctx := context.Background()

	alertStore := alerts.NewSQLStore(s.DB)
	tx, err := s.DB.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := alertStore.Execute(ctx, tx, orgID, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: now, Severity: rules.StateCrit},
	}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("seed open alert: %v", err)
	}
	open, err := alertStore.OpenForHost(ctx, tx, orgID, host)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("open for host: %v", err)
	}
	var alertID uuid.UUID
	for _, a := range open {
		if a.Subject == sub {
			alertID = a.ID
		}
	}
	if alertID == uuid.Nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("no seeded alert found for subject %+v among %+v", sub, open)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	events, err := alertStore.Events(ctx, orgID, alertID)
	if err != nil || len(events) == 0 {
		t.Fatalf("events: %v, %v", events, err)
	}

	ledgerTx, err := s.DB.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin ledger tx: %v", err)
	}
	if err := alertroutes.NewSQLStore(s.DB).RecordDecision(ctx, ledgerTx, orgID, events[0].ID, alertroutes.StateDeferred, now); err != nil {
		_ = ledgerTx.Rollback(ctx)
		t.Fatalf("seed deferred ledger row: %v", err)
	}
	if err := ledgerTx.Commit(ctx); err != nil {
		t.Fatalf("commit ledger tx: %v", err)
	}
	return alertID
}

// Response is one bearer-authenticated HTTP response from Get/Post/Patch/
// Delete below, shaped like httptest.ResponseRecorder (Code, Body) for
// assertions that read the same either way — even though these actually
// round-trip over the httptest server's real listener rather than an
// in-process ServeHTTP call: a bearer credential is transport-level, unlike
// the cookie jar Client() hands out, so there is no in-process shortcut
// here.
type Response struct {
	Code int
	Body *bytes.Buffer
}

// bearerDo issues a bearer-authenticated request. ifMatch, when non-empty,
// is sent as If-Match; body, when non-nil, is marshaled as the JSON
// request body.
func (s *Server) bearerDo(t *testing.T, method, tok, path, ifMatch string, body any) *Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.HTTP.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return &Response{Code: resp.StatusCode, Body: bytes.NewBuffer(data)}
}

// Get issues a bearer-authenticated GET.
func (s *Server) Get(t *testing.T, tok, path string) *Response {
	t.Helper()
	return s.bearerDo(t, http.MethodGet, tok, path, "", nil)
}

// Post issues a bearer-authenticated POST with a JSON body.
func (s *Server) Post(t *testing.T, tok, path string, body any) *Response {
	t.Helper()
	return s.bearerDo(t, http.MethodPost, tok, path, "", body)
}

// Patch issues a bearer-authenticated PATCH with a JSON body and, when
// ifMatch is non-empty, an If-Match header — "" leaves the header off
// entirely, for a test proving the route refuses a write carrying none
// (backlog 1.31).
func (s *Server) Patch(t *testing.T, tok, path, ifMatch string, body any) *Response {
	t.Helper()
	return s.bearerDo(t, http.MethodPatch, tok, path, ifMatch, body)
}

// Delete issues a bearer-authenticated DELETE, If-Match the same way Patch
// handles it.
func (s *Server) Delete(t *testing.T, tok, path, ifMatch string) *Response {
	t.Helper()
	return s.bearerDo(t, http.MethodDelete, tok, path, ifMatch, nil)
}

// SeedPasskey attaches a stored passkey row named name to userID, without
// running a WebAuthn ceremony — for tests that only need the account to
// have a second factor (a passkey counts as one wherever TOTP does, see
// auth.SecondFactors), not a credential that can actually sign in. It
// returns the passkey's id.
func (s *Server) SeedPasskey(userID uuid.UUID, name string) uuid.UUID {
	s.T.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := s.DB.Pool().Exec(context.Background(),
		`INSERT INTO passkeys (id, user_id, name, credential_id, public_key, attestation) VALUES ($1, $2, $3, $4, $5, 'none')`,
		id, userID, name, []byte("seeded-"+id.String()), []byte("seeded-public-key")); err != nil {
		s.T.Fatal(err)
	}
	return id
}

// LoginAs returns a client whose jar holds a fully authenticated
// (MFA-complete) session cookie.
func (s *Server) LoginAs(userID uuid.UUID) *http.Client {
	s.T.Helper()
	return s.loginAsWithMFA(userID, true)
}

// LoginAsPending returns a client whose jar holds a session cookie that has
// not completed a required second factor (mfa_complete = false). It exists
// to test requireMFAComplete's rejection of half-authenticated sessions
// without needing Task 7's TOTP flow to actually create one (Finding 8,
// fix round 1).
func (s *Server) LoginAsPending(userID uuid.UUID) *http.Client {
	s.T.Helper()
	return s.loginAsWithMFA(userID, false)
}

func (s *Server) loginAsWithMFA(userID uuid.UUID, mfaComplete bool) *http.Client {
	s.T.Helper()
	_, cookie, err := s.Sessions.Create(context.Background(), userID, mfaComplete)
	if err != nil {
		s.T.Fatal(err)
	}
	c := s.Client()
	u, _ := url.Parse(s.HTTP.URL)
	c.Jar.SetCookies(u, []*http.Cookie{{Name: auth.CookieName, Value: cookie}})
	return c
}

// Push is one recorded check-configuration push: the API asked the agent
// side to hand a host — or, for an organization-wide push, every
// monitored host at once — its freshly resolved check set. HostID is
// uuid.Nil exactly when the push was organization-wide (PushOrg), which
// is the distinction the routes are asserted on: an organization-bound
// write must take the one-pass path, never a per-host fan-out.
type Push struct {
	OrgID, HostID uuid.UUID
	WholeOrg      bool
}

// PushRecorder stands in for api.Deps.AgentPush (*agentapi.Service in
// production, whose PushHosts only does anything for a host with a live
// gRPC stream). It records what the routes asked for, and can be made to
// fail, so a test can prove both that a write pushes the hosts it affects
// and that a failed push never fails the write — the agent re-asserts its
// configuration version every 60 seconds regardless.
//
// Safe for concurrent use: the httptest server may serve requests on more
// than one goroutine.
type PushRecorder struct {
	mu     sync.Mutex
	pushes []Push
	err    error
}

// PushHosts implements api.CheckConfigPusher. Each id is recorded as its
// own Push, so a test can still assert "exactly these hosts" — the batch
// is how the manager talks to the resolver, not a change in what a write
// is supposed to reach.
func (r *PushRecorder) PushHosts(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) error {
	for _, id := range hostIDs {
		if err := r.record(ctx, Push{OrgID: orgID, HostID: id}); err != nil {
			return err
		}
	}
	return nil
}

// PushOrg implements api.CheckConfigPusher.
func (r *PushRecorder) PushOrg(ctx context.Context, orgID uuid.UUID) error {
	return r.record(ctx, Push{OrgID: orgID, WholeOrg: true})
}

// record captures one push.
//
// It deliberately does NOT check ctx.Err(): inside a huma handler the
// request context has not been cancelled yet whether or not the route
// detached it, so such an assertion can never fail and would only look
// like the detachment was covered. That contract is pinned where it is
// observable, against pushAffected directly —
// TestPushAffectedSurvivesACancelledRequest, internal/api/check_push_internal_test.go.
func (r *PushRecorder) record(_ context.Context, p Push) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.pushes = append(r.pushes, p)
	return nil
}

// Pushes returns every push recorded so far, oldest first.
func (r *PushRecorder) Pushes() []Push {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Push, len(r.pushes))
	copy(out, r.pushes)
	return out
}

// PushedHosts reports whether every id was pushed at least once, by a
// per-host push. An organization-wide push does not count: it reaches
// those hosts, but through a different path, and a test asserting "these
// hosts specifically" must not pass because everything was pushed.
func (r *PushRecorder) PushedHosts(ids ...uuid.UUID) bool {
	seen := map[uuid.UUID]struct{}{}
	for _, p := range r.Pushes() {
		if !p.WholeOrg {
			seen[p.HostID] = struct{}{}
		}
	}
	for _, id := range ids {
		if _, ok := seen[id]; !ok {
			return false
		}
	}
	return true
}

// PushedWholeOrg reports how many organization-wide pushes were recorded.
func (r *PushRecorder) PushedWholeOrg() int {
	n := 0
	for _, p := range r.Pushes() {
		if p.WholeOrg {
			n++
		}
	}
	return n
}

// Reset drops every recorded push, so one test can assert on the pushes of
// one write without counting the fixture's own setup.
func (r *PushRecorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pushes = nil
}

// FailWith makes every later PushHosts return err instead of recording,
// for the "a push failure never fails the write" test. nil restores
// recording.
func (r *PushRecorder) FailWith(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

// apitestRepoRefSource mirrors internal/app's own unexported repoRefSource
// (repos.Service narrowed to the jobs queue's RepoSource port): app's
// adapters are deliberately unexported, so this fixture — which needs the
// real repos.Service wired onto a real claim, the same way production does
// — carries its own copy rather than an app import, the same reason
// internal/jobs/queue_sql_test.go's repoRefAdapter exists.
type apitestRepoRefSource struct{ s *repos.Service }

func (a apitestRepoRefSource) Get(ctx context.Context, orgID, repoID uuid.UUID) (jobs.RepoRef, error) {
	r, err := a.s.Get(ctx, orgID, repoID)
	if err != nil {
		return jobs.RepoRef{}, err
	}
	return jobs.RepoRef{
		ID: r.ID, URL: r.URL, Branch: r.Branch, Subpath: r.Subpath, CredentialID: r.CredentialID,
		VaultIdentities: apitestToJobsVault(r.VaultIdentities),
		SSHHostKey:      r.SSHHostKey, SSHHostPublicKey: r.SSHHostPublicKey,
	}, nil
}

// apitestVaultResolver mirrors internal/app's own unexported vaultResolver:
// the real repos.ResolveVaultIdentities (spec decision 2), reached through
// the conversion both directions need because the jobs package cannot
// import internal/repos (jobs.VaultResolver's own comment explains the
// cycle).
type apitestVaultResolver struct{}

func (apitestVaultResolver) ResolveVaultIdentities(repo, template []jobs.VaultIdentity) []jobs.VaultIdentity {
	resolved := repos.ResolveVaultIdentities(apitestToRepoVault(repo), apitestToRepoVault(template))
	return apitestToJobsVault(resolved)
}

func apitestToJobsVault(in []repos.VaultIdentity) []jobs.VaultIdentity {
	out := make([]jobs.VaultIdentity, len(in))
	for i, id := range in {
		out[i] = jobs.VaultIdentity{CredentialID: id.CredentialID, Label: id.Label, FromTemplate: id.FromTemplate}
	}
	return out
}

func apitestToRepoVault(in []jobs.VaultIdentity) []repos.VaultIdentity {
	out := make([]repos.VaultIdentity, len(in))
	for i, id := range in {
		out[i] = repos.VaultIdentity{CredentialID: id.CredentialID, Label: id.Label, FromTemplate: id.FromTemplate}
	}
	return out
}
