// Package apitest spins up the full API against the test database.
package apitest

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api"
	"github.com/noixdev/pulsix/internal/app"
	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/clientip"
	"github.com/noixdev/pulsix/internal/config"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/mail"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/setup"
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
}

// Option customizes a Server's Deps before its httptest.Server starts.
type Option func(*Server)

// WithSecureCookies sets Deps.Secure, so tests can exercise the Secure
// attribute on the real Set-Cookie response of the login endpoint instead
// of only asserting it on code with no caller (Finding 3, fix round 1).
func WithSecureCookies() Option {
	return func(s *Server) { s.Deps.Secure = true }
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
func WithHostsStore(wrap func(hosts.Store) hosts.Store) Option {
	return func(s *Server) {
		s.Deps.Hosts = hosts.NewService(wrap(hosts.NewSQLStore(s.DB)), time.Now)
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
	cfg := config.Config{PublicURL: publicURL}

	totp := auth.NewTOTP(auth.NewSQLTOTPStore(d), crypto.NewBox(testMasterKey), "Pulsix Test", time.Now)
	passkeys, err := auth.NewPasskeys(auth.NewSQLPasskeyStore(appQ), auth.NewSQLStore(appQ), s.Sessions, cfg.RPID(), "Pulsix Test", cfg.Origin(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	// A passkey must count as a second factor at password login the same as
	// TOTP does, not only at the passkey login endpoints — otherwise a
	// passkey-only account's password alone would produce an MFA-complete
	// session (Finding 2, fix round 1).
	setupSvc := setup.NewService(setup.NewSQLStore(d), s.Sessions, crypto.NewBox(testMasterKey), time.Now)
	tenancySvc := tenancy.NewService(tenancy.NewSQLStore(d), s.Mail, publicURL, time.Now)
	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	// Every organization gets its "default" zone through this hook,
	// whichever of the two ways it was created — POST /orgs
	// (tenancy.Service.CreateOrg) or the first-run wizard
	// (setup.Service.Complete) — exactly like production wiring
	// (internal/app/app.go) binds both to the same hostsSvc.EnsureDefaultZone.
	tenancySvc.SetOrgCreatedHook(hostsSvc.EnsureDefaultZone)
	setupSvc.SetOrgCreatedHook(hostsSvc.EnsureDefaultZone)
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
	playbooksSvc := playbooks.NewService(playbooksStore, reposSvc)
	templatesSvc := templates.NewService(templateStore, playbooksSvc, inventorySvc, hostsSvc, credsSvc, crypto.NewBox(testMasterKey))
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
	s.Queue = jobsQueue
	s.Deps = api.Deps{
		Sessions: s.Sessions, Auth: auth.NewService(auth.NewSQLStore(appQ), s.Sessions, auth.AnyEnabled(totp, passkeys)), TOTP: totp, Passkeys: passkeys,
		Tokens:   auth.NewTokens(auth.NewSQLTokenStore(d), time.Now),
		Tenancy:  tenancySvc,
		Audit:    auditRec,
		ClientIP: clientip.New(nil),
		Setup:    setupSvc,
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
		Playbooks:   playbooksSvc,
		Templates:   templatesSvc,
		Jobs:        jobsSvc,
		Schedules:   schedulesSvc,
		Triggers:    triggersSvc,
		Workers:     workersSvc,
		// What an enrollment snippet names; tests that need the worker
		// endpoint disabled clear it through an option.
		WorkerEndpoint: api.WorkerEndpoint{Addr: "localhost:8444", ServerName: "pulsix-manager"},
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

// Client returns an HTTP client with a cookie jar.
func (s *Server) Client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
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

// SeedOrg inserts an org with ownerID as owner, and — like every real org
// creation path (tenancy.Service.CreateOrg, setup.Service.Complete) —
// gives it its "default" zone: this bypasses tenancy.Service.CreateOrg
// itself (so tests get a fixed, predictable slug instead of one it would
// generate), but a seeded org should still carry the same invariant every
// production org does, since host_handlers_test.go and others rely on it.
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
