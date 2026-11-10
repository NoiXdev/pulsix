// Package app wires every Pulsix manager dependency into a single
// http.Handler. It exists so cmd/manager/main.go can stay a thin
// entry point (parse config, build the app, run an http.Server, shut down
// on SIGTERM) while the wiring itself — and the row-level-security guard
// that makes it safe — lives somewhere scripts/coverage-gate.sh actually
// measures; cmd/... is excluded from the coverage floor precisely because
// it is expected to carry no logic worth testing.
package app

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"

	"github.com/noixdev/pulsix/internal/accounts"
	"github.com/noixdev/pulsix/internal/agentapi"
	"github.com/noixdev/pulsix/internal/agentdist"
	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/alertroutes"
	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/api"
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
	"github.com/noixdev/pulsix/internal/grpcsrv"
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
	"github.com/noixdev/pulsix/internal/schedule"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/setup"
	"github.com/noixdev/pulsix/internal/shared"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/tenancy"
	"github.com/noixdev/pulsix/internal/triggers"
	"github.com/noixdev/pulsix/internal/workers"
)

// appRole is the least-privilege PostgreSQL role every request is served
// through — see assertCannotBypassRLS's own doc comment for why this
// matters and how it is verified, not merely assumed.
const appRole = "pulsix_app"

// App is a fully wired manager: an http.Handler, the optional worker gRPC
// server (nil unless cfg.GRPCListen was set), plus what main.go needs to
// shut both down cleanly.
type App struct {
	Handler    http.Handler
	GRPC       *grpc.Server
	GRPCListen string
	db         *db.DB
	// stopReaper, stopRetest and stopTicker stop the job queue's reaper,
	// the connection re-test scheduler and the schedule ticker, each
	// waiting until its goroutine has returned (startLoop); Close calls
	// them before it closes the pool.
	stopReaper func()
	stopRetest func()
	stopTicker func()
	// stopAuthReaper stops the expired-ceremony and expired-session reaper
	// (backlog 1.21), waiting until its goroutine has returned.
	stopAuthReaper func()
	// stopDispatch stops the notification dispatcher (spec 2026-09-18
	// §5), waiting for a batch in flight.
	stopDispatch func()
	// stopDigest stops the weekly digest ticker.
	stopDigest func()
	// stopEval stops the rule evaluation ticker (v0.3b-2), waiting for a
	// batch in flight — so Close never closes the pool under an
	// evaluation that is still writing states.
	stopEval func()
	// stopAlertNotify stops the alert follower (v0.4b). Like every other
	// loop here it cancels and then waits for the goroutine to return
	// (startLoop), but what that wait buys is narrower than it looks: the
	// tick runs on the very context startLoop cancelled, so a tick in
	// flight does not get to finish — its tx.Commit(ctx) fails on the
	// cancelled context and the deferred rollback (on a context without
	// cancel) undoes the whole tick.
	//
	// That is safe, and it is the design's own "Failure ends at the
	// enqueue": the tick's ledger decisions and its outbox rows share one
	// transaction, so they roll back together. Nothing was recorded as
	// decided, so the forward pass's anti-join still finds exactly those
	// events on the next manager's next tick. The wait is therefore for
	// the goroutine, not for the commit — it keeps Close from closing the
	// pool under a transaction that is still being written, which is why
	// the loop must be stopped before a.db.Close() below, not why no
	// alert is lost.
	stopAlertNotify func()
	// stopEmbedded cancels the embedded worker's goroutine (nil unless
	// cfg.EmbeddedWorker was set); Close calls it.
	stopEmbedded func()
	// backups runs scheduled and manual backups (backlog 7.2);
	// stopBackupTicker stops its schedule. Close stops the ticker, then the
	// service, which waits for a manual backup still running.
	backups          *backup.Service
	stopBackupTicker func()
	// items is the discovery and decisions service (v0.3b-3). Wired here
	// because its constructor's check-key set is derived from
	// rules.Catalogue() (ItemCheckKeys, rules_ports.go), and this package
	// is the only one that may import both internal/rules and
	// internal/items. The same instance also backs api.Deps.Items
	// (registerItemRoutes); this field itself is kept only because nothing
	// else in this package needs to read it back out of App.
	items *items.Service
	// background is the account service's work that outlives a request:
	// the reset links an accepted forgot-password request still has to
	// mail. Close waits for it, bounded by backgroundDrainTimeout, once the
	// HTTP server has drained and before the pool closes.
	background interface {
		Shutdown(ctx context.Context) error
	}
}

// backgroundDrainTimeout bounds how long Close waits for background work
// (App.background): as long as the HTTP server's own drain may take. A
// package var so a test can shrink it.
var backgroundDrainTimeout = 10 * time.Second

// Close releases the application's database pool and stops the reaper. It
// does not touch the migration pool Build opens: that one is already closed
// before Build returns, successfully or not — see Build's own comment on
// why.
//
// It first waits for the background work requests left behind
// (App.background), bounded by backgroundDrainTimeout. That must come
// after the HTTP server has stopped and drained — cmd/manager defers Close
// until serve has returned, which is after http.Server.Shutdown — so no
// request still adds work, and before the pool closes under it.
func (a *App) Close() {
	if a.background != nil {
		ctx, cancel := context.WithTimeout(context.Background(), backgroundDrainTimeout)
		if err := a.background.Shutdown(ctx); err != nil {
			slog.Warn("background work did not finish before shutdown", "err", err, "timeout", backgroundDrainTimeout)
		}
		cancel()
	}
	if a.stopReaper != nil {
		a.stopReaper()
	}
	if a.stopRetest != nil {
		a.stopRetest()
	}
	if a.stopAuthReaper != nil {
		a.stopAuthReaper()
	}
	if a.stopTicker != nil {
		a.stopTicker()
	}
	if a.stopDispatch != nil {
		a.stopDispatch()
	}
	if a.stopDigest != nil {
		a.stopDigest()
	}
	if a.stopEval != nil {
		a.stopEval()
	}
	if a.stopAlertNotify != nil {
		a.stopAlertNotify()
	}
	if a.stopEmbedded != nil {
		a.stopEmbedded()
	}
	if a.stopBackupTicker != nil {
		a.stopBackupTicker()
	}
	if a.backups != nil {
		a.backups.Stop()
	}
	a.db.Close()
}

// Build constructs every dependency in order — database (running
// migrations, then reopening as the least-privilege role and verifying it
// really cannot bypass row-level security), the master-key box, sessions,
// every service, and finally the API handler — and returns a ready-to-serve
// App. It is the one place production wiring happens; cmd/manager/main.go
// only calls it.
func Build(ctx context.Context, cfg config.Config, opts ...Option) (*App, error) {
	o := buildOptions{scheduleNow: time.Now}
	for _, opt := range opts {
		opt(&o)
	}
	// Migrations need DDL privileges appRole does not have, so they run
	// through the plain superuser/owner connection named by
	// cfg.DatabaseURL. That connection is closed as soon as migrations are
	// done — nothing else in this function, or in the App it returns, ever
	// uses it — precisely so that a bug anywhere downstream can never
	// accidentally query through a connection RLS does not apply to.
	//
	// Every session a manager opens, this one included, carries the
	// application_name pulsix-manager: restore recognizes a running manager
	// by it and refuses to replace the database under one.
	dbURL, err := db.WithApplicationName(cfg.DatabaseURL, managerApplicationName)
	if err != nil {
		return nil, fmt.Errorf("database url: %w", err)
	}
	migrationDB, err := db.Open(ctx, dbURL)
	if err != nil {
		return nil, fmt.Errorf("open migration database connection: %w", err)
	}
	// A database migrated by a newer Pulsix is refused before anything runs
	// against it: goose would silently skip it (backlog 7.3).
	if err := migrationDB.CheckSchemaNotNewer(ctx); err != nil {
		migrationDB.Close()
		return nil, err
	}
	migrateErr := migrationDB.Migrate(ctx)
	migrationDB.Close()
	if migrateErr != nil {
		return nil, fmt.Errorf("run migrations: %w", migrateErr)
	}

	// Every request from here on is served through appRole instead: its
	// AfterConnect hook issues SET ROLE on every pooled connection, which
	// is what actually makes the row-level security policies from Task 4
	// onward apply at all (see db.OpenAsRole's own doc comment — the login
	// role in cfg.DatabaseURL may be a superuser or the schema owner, both
	// of which bypass RLS unconditionally, FORCE ROW LEVEL SECURITY or
	// not).
	appDB, err := db.OpenAsRole(ctx, dbURL, appRole)
	if err != nil {
		return nil, fmt.Errorf("open application database connection: %w", err)
	}
	if err := assertCannotBypassRLS(ctx, appDB); err != nil {
		appDB.Close()
		return nil, err
	}

	box := crypto.NewBox(cfg.MasterKey)
	// Every store is built on appDB itself — never on appDB.Pool() — so each
	// statement runs inside whatever organization the request context binds
	// (db.WithOrgScope, set for every org-scoped route by the API's
	// orgScopeMiddleware). That is what makes the org_isolation policies
	// enforce the tenant boundary independently of each query's own WHERE
	// clause; the store constructors taking *db.DB rather than a pool or a
	// *gen.Queries is what keeps a future store from being wired past it.
	q := gen.New(appDB)
	sessions := auth.NewSessions(q, time.Now)

	totp := auth.NewTOTP(auth.NewSQLTOTPStore(appDB), box, "Pulsix", time.Now)
	passkeys, err := auth.NewPasskeys(auth.NewSQLPasskeyStore(q), auth.NewSQLStore(q), sessions, appDB, cfg.RPID(), "Pulsix", cfg.Origin(), time.Now)
	if err != nil {
		appDB.Close()
		return nil, fmt.Errorf("build passkey service: %w", err)
	}
	// Finishing a second factor writes the credential and the session
	// upgrade together or not at all, and only on a session that is still
	// live (backlogs 1.26, 7.109).
	enrolment := auth.NewEnrolment(totp, passkeys, sessions, appDB)
	tokens := auth.NewTokens(auth.NewSQLTokenStore(appDB), time.Now)
	authSvc := auth.NewService(auth.NewSQLStore(q), sessions, auth.AnyEnabled(totp, passkeys), time.Now)

	setupToken := setup.Token(cfg.MasterKey)
	setupSvc := setup.NewService(setup.NewSQLStore(appDB), sessions, box, time.Now, setupToken)
	// While nobody has completed setup, every start logs the token the
	// wizard asks for, so only whoever can read this log can claim a
	// fresh instance.
	logSetupToken(ctx, slog.Default(), setupSvc, setupToken, cfg.PublicURL)
	// mail.NewSMTP resolves its settings through setupSvc.LoadSMTP on every
	// call, so a settings change made through PUT /admin/settings/smtp
	// takes effect on the very next send — there is no separate mailer to
	// rebuild or process to restart.
	mailer := mail.NewSMTP(setupSvc.LoadSMTP)
	tenancySvc := tenancy.NewService(tenancy.NewSQLStore(appDB), mailer, cfg.PublicURL, time.Now)
	auditRec := audit.NewRecorder(audit.NewSQLStore(appDB), time.Now)
	hostsSvc := hosts.NewService(hosts.NewSQLStore(appDB), time.Now)
	// The monitoring check pipeline (v0.3b-1): SeedForOrg is wired into the
	// hook right below; WithMatcher/WithHostRoster (checks_ports.go) are
	// what let ResolveHost/ResolveOrg run at all — a Service built without
	// them refuses to resolve (checks.Service's own fail-closed posture),
	// which is why both go in here rather than the later, API-surface task
	// that reads from this same instance.
	checksSvc := checks.NewService(checks.NewSQLStore(appDB),
		checks.WithMatcher(NewChecksHostMatcher(hostsSvc)),
		checks.WithHostRoster(NewChecksHostRoster(hostsSvc)))
	// The rule engine's starter set (v0.3b-2, Task 5): rulesSvc needs no
	// host ports of its own — SeedForOrg only ever writes org-scoped rows —
	// so it is built here for the sole purpose of the hook right below,
	// exactly the shape a later task (the API surface) will build its own,
	// separately wired instance from.
	rulesSvc := rules.NewService(rules.NewSQLStore(appDB))
	// The API's own read side onto the evaluator's tables (v0.3b-2 Task
	// 11): a second, separately wired *rules.SQLStore, exactly as the
	// comment above anticipated — cheap to build (it holds nothing but the
	// two query handles), and kept apart from rulesSvc's because
	// rules.StateStore is deliberately the narrower, read-only interface
	// (see its own doc comment).
	ruleStates := rules.NewSQLStore(appDB)
	// Installation-owned repository definitions (v0.2f): sharedSvc needs no
	// org-scoped ports of its own (every method but ProjectInto runs on an
	// unscoped ctx by design — shared.SQLStore's own doc comment). Built
	// here for the organization-created hook right below, exactly the
	// shape rulesSvc above already set: a new organization's own projection
	// of every existing repository definition is what spec decision 4 calls
	// "the same seam rules.SeedForOrg already uses". The same instance also
	// backs api.Deps.Shared (Task 4's instance-admin CRUD over definitions,
	// registerSharedRepoRoutes) — one Service, no Principal or org state of
	// its own to make sharing it across both callers unsafe.
	sharedSvc := shared.NewService(shared.NewSQLStore(appDB))
	// Discovery and item decisions (v0.3b-3): one store, two lifecycles —
	// itemsStore is also what the evaluation ticker's own EvalSQLStore
	// discovers items and resolves decisions through (see evalTicker
	// below), and itemsSvc is the validating, previewing surface wired into
	// the API below (Items, api.Deps). Both are built here because this is
	// the only package allowed to import both internal/rules (for
	// ItemCheckKeys, just below) and internal/items.
	itemsStore := items.NewSQLStore(appDB)
	itemsSvc := items.NewService(itemsStore, ItemCheckKeys)
	// Every organization gets its "default" zone, its "Linux base" check
	// definitions, its five-row starter rule set and a projection of every
	// installation-owned repository, job template AND check definition
	// (v0.2f, spec decision 4) the moment it is created, through whichever of the
	// two paths created it — POST /orgs (tenancy.Service.CreateOrg) or the
	// first-run wizard (setup.Service.Complete) — both bound to the same
	// six hooks, chained (ChainOrgCreatedHooks, checks_ports.go) since
	// SetOrgCreatedHook only ever holds one, so neither path can produce an
	// organization the other would have given a zone, a check seed set, a
	// rule seed set or any of the three definition kinds' own projections.
	// rulesSvc.SeedForOrg runs after checksSvc.SeedForOrg so that its rules
	// can always assume the checks their metrics belong to already exist;
	// sharedSvc.ProjectAllInto (repositories) runs before
	// sharedSvc.ProjectAllTemplatesInto (job templates) so a brand-new
	// organization already has its own projection of a shared repository to
	// join against when a shared template's playbook_id is resolved — not
	// required for correctness (InsertProjectedTemplate's own query LEFT
	// JOINs and tolerates no match at all, leaving playbook_id NULL), but
	// the natural order all the same, and one this hook can give for free.
	// sharedSvc.ProjectAllCheckDefinitionsInto (Task 7, check definitions)
	// runs last, after checksSvc.SeedForOrg has already written the
	// organization's "Linux base" seed set: a shared check definition using
	// one of that set's own reserved keys (cpu, memory, disk, service) can
	// never be CREATED in the first place — Service.CreateCheckDefinition's
	// own pre-flight collision check already finds it colliding in EVERY
	// existing organization the moment the provider tries, since every
	// organization has carried those four keys at the default ('org', nil,
	// nil) scope since its own creation — so this ordering never actually
	// needs to race against that seed set for a real definition; it is
	// simply the correct order instead of an arbitrary one, since
	// checksSvc.SeedForOrg could in principle be wrong about what "already
	// exists" means for a NEW organization's check definitions.
	// See tenancy.OrgCreatedHook's own doc comment for why this runs as a
	// sequential step after each service's own writes commit, rather than
	// inside either one's transaction — sharedSvc.ProjectAllInto's own doc
	// comment says the same thing again from its side: a brand-new
	// organization created while a definition is mid-edit still lands on
	// either the whole old value or the whole new one, never a mixture,
	// because shared.Service.UpdateRepo's own propagation is atomic per
	// definition (shared.SQLStore.UpdateAndPropagate) — that guarantee comes
	// from the mechanism a read goes through, not from this hook running
	// inside any transaction of its own, which it does not. The same holds
	// for shared.Service.UpdateTemplate's own UpdateTemplateAndPropagate and
	// shared.Service.UpdateCheckDefinition's own UpdateCheckDefinitionAndPropagate.
	orgCreated := ChainOrgCreatedHooks(
		hostsSvc.EnsureDefaultZone, checksSvc.SeedForOrg, rulesSvc.SeedForOrg,
		sharedSvc.ProjectAllInto, sharedSvc.ProjectAllTemplatesInto, sharedSvc.ProjectAllCheckDefinitionsInto,
	)
	tenancySvc.SetOrgCreatedHook(orgCreated)
	setupSvc.SetOrgCreatedHook(orgCreated)

	// The hook above closes most of the gap, but not all of it: it runs
	// after the organization's own insert has already committed, so a
	// failure there — a crash, a dropped connection, hostsSvc's own store
	// erroring — leaves a real organization with no default zone, and a
	// caller cannot repair it merely by retrying org creation (the slug is
	// already taken, so that retry fails before the hook is ever reached
	// again). This reconciliation pass is what actually closes it: it finds
	// every such organization and gives it its zone, healing on the very
	// next restart what the hook alone cannot. It runs here, decoupled from
	// tenancy just like the hook itself is (see tenancy.OrgCreatedHook's own
	// doc comment) — hostsSvc.ReconcileDefaultZones only ever reads the
	// hosts/zones and organizations tables directly, never importing
	// tenancy.
	//
	// A failure here must never stop the manager from starting for a
	// reason unrelated to the database being reachable: by this point
	// appDB has already been opened and queried successfully (assertCannotBypassRLS,
	// just above), so the database is known reachable, and any error this
	// pass reports is necessarily something narrower — one or more
	// organizations that still could not be repaired. That is logged, not
	// escalated into a startup failure; the next restart tries again.
	if repaired, err := hostsSvc.ReconcileDefaultZones(ctx); err != nil {
		slog.ErrorContext(ctx, "reconcile default zones: some organizations could not be repaired", "repaired", repaired, "err", err)
	} else if repaired > 0 {
		slog.InfoContext(ctx, "reconciled organizations that were missing their default zone", "repaired", repaired)
	}

	credsSvc := credentials.NewService(credentials.NewSQLStore(appDB), box, time.Now, rand.Reader)
	sshSvc := sshconf.NewService(sshconf.NewSQLStore(appDB), credsSvc, hostsSvc)
	invStore := inventory.NewSQLStore(appDB)
	inventorySvc := inventory.NewService(invStore, hostsSvc, sshSvc, box)
	renderer := inventory.NewRenderer(invStore, hostsSvc, sshSvc, credsSvc, box)

	// The job queue and its org-scoped service are built before the API
	// handler: the repository registry's sync action enqueues through the
	// jobs service, and the queue reports finished sync_repo runs back
	// through it (SetSyncSink below). The reaper goroutine itself starts
	// further down, reusing the same queue. The template lookup adapter
	// rides the templates and playbooks stores, both constructible before
	// their services (the service graph is circular here: jobs needs the
	// template source, playbooks needs repos, repos needs jobs — the stores
	// break the cycle while keeping one source of SQL for the table).
	templateStore := templates.NewSQLStore(appDB)
	playbooksStore := playbooks.NewSQLStore(appDB)
	templateLookup := NewTemplateLookup(templateStore, playbooksStore, box)
	jobsQueue := jobs.NewSQLQueue(appDB, box, renderer, credsSvc, templateLookup, sshSvc, time.Now, rand.Reader)
	resolver := jobs.NewScopeResolver(inventorySvc, inventorySvc, hostsSvc)
	jobsSvc := jobs.NewService(jobs.NewSQLStore(appDB), jobsQueue, templateLookup, resolver, hostsSvc, hostsSvc, box, time.Now, rand.Reader)
	reposSvc := repos.NewService(repos.NewSQLStore(appDB), credsSvc, jobsSvc)
	playbooksSvc := playbooks.NewService(playbooksStore, reposSvc, sharedSvc)
	templatesSvc := templates.NewService(templateStore, playbooksSvc, inventorySvc, hostsSvc, credsSvc, box)
	// The schedules service (Task 6): fires job templates on a timetable
	// through the same org-scoped jobs.Service every other trigger uses.
	// *templates.Service already satisfies TemplateChecker, so it is
	// passed directly — no adapter needed.
	schedulesSvc := schedules.NewService(schedules.NewSQLStore(appDB), templatesSvc, jobsSvc, auditRec, time.Now, rand.Reader, slog.Default())
	// The triggers service (Task 10): the webhook receive pipeline behind
	// POST /hooks/jobs/{id} and POST /hooks/repos/{id}. IPLimit is the
	// per-source-address limit (spec §9), in process like the reaper and
	// ticker above — with several managers, each counts on its own.
	triggersSvc := triggers.NewService(triggers.Deps{
		Store: triggers.NewSQLStore(appDB), Templates: templatesSvc, Repos: reposSvc, Box: box,
		Runs: jobsSvc, Audit: auditRec, PublicURL: cfg.PublicURL, IPLimit: clientip.NewLimiter(triggers.HookIPLimit, time.Minute, time.Now),
		Now: time.Now, Rand: rand.Reader, Log: slog.Default(),
	})
	jobsQueue.SetSyncSink(jobsSvc)
	// The variable sources a successful sync read (backlog 7.75) are
	// stored by the repository registry, inside the finish transaction.
	jobsQueue.SetSourceSink(reposSvc)
	// Once a sync stored new sources (after its finish committed), and
	// once an editor playbook is saved, the templates in automatic mode
	// take them (backlog 7.75, plan decisions 6 and 9).
	jobsQueue.SetSourceApplier(templatesSvc)
	playbooksSvc.SetInlineApplier(templatesSvc)
	// Repo playbooks claim their repository through the same service: the
	// adapter narrows repos.Service to the queue's RepoSource port.
	jobsQueue.SetRepoSource(repoRefSource{reposSvc})
	// A repo playbook's vault identities (v0.2e, spec decision 2) resolve
	// through repos.ResolveVaultIdentities, reached via the vaultResolver
	// adapter because the queue's own package cannot import internal/repos
	// (jobs.VaultResolver's own comment explains the cycle).
	jobsQueue.SetVaultResolver(vaultResolver{})
	// The run events' SSE fan-out (Task 12): the jobs service owns the
	// in-process subscriber registry the run view streams from, and the
	// queue's append path — the gRPC EventRecorder port lands on it —
	// feeds it. Same post-construction circularity as SetSyncSink above.
	jobsQueue.SetEventBroker(jobsSvc.EventsBroker())
	// Host keys and test history: the claim reads the pins from the hosts
	// service, and a finished connection_test job records its outcome
	// through it (org-scoped, so the wiring rides the same service).
	jobsQueue.SetKeyPins(hostsSvc)
	jobsQueue.SetTestSink(hostsSvc)
	// Credential rotation (Task 11): the credentials service enqueues its
	// rotation group through the jobs service (the narrow
	// credentials.RotationEnqueuer port), and a completed rotate_key group
	// finalizes back through the queue's RotationFinisher port — sealing
	// the pending new key onto the credential or leaving it untouched.
	credsSvc.SetRotationEnqueuer(jobsSvc)
	jobsQueue.SetRotationFinisher(credsSvc)

	// The instance CA is built even without a gRPC listener: enrollment
	// tokens carry its fingerprint (spec 2026-09-18 §4).
	caInst, err := ca.New(ca.NewSQLStore(appDB), box, ca.Config{CommonName: caCommonName, ListenHost: cfg.CAListenHost()})
	if err != nil {
		appDB.Close()
		return nil, fmt.Errorf("open CA: %w", err)
	}
	if err := caInst.Ensure(ctx); err != nil {
		appDB.Close()
		return nil, fmt.Errorf("ensure CA: %w", err)
	}
	workersSvc := workers.NewService(workers.NewSQLStore(appDB), workers.WithSigner(caInst), workers.WithZones(hostsSvc))
	streams := grpcsrv.NewStreamRegistry()
	// A revocation ends the worker's open streams and requeues its jobs.
	workersSvc.SetRevokeHooks(streams, workerJobRequeuer{q: jobsQueue, maxAttempts: cfg.JobMaxAttempts})

	// The agent registry and its gRPC service (v0.3a): enrollment tokens,
	// the host claim (hosts.Service is the narrow provisioner), the
	// certificate lifecycle and the persistent agent stream. The agent
	// stream registry doubles as the revoke hook — a revoked agent's open
	// streams end with its registry rows. The agents service is the
	// FactsSink itself (agents.Service.RecordFacts): facts merge into
	// hosts.facts and re-apply the os/arch/virt automatic tags.
	agentsSvc := agents.NewService(agents.NewSQLStore(appDB), agents.WithProvisioner(NewAgentHostProvisioner(hostsSvc)),
		// Every enrollment token carries this manager's CA fingerprint:
		// it is what the agent pins its very first connection against.
		agents.WithCA(caInst))
	agentStreams := agentapi.NewStreamRegistry()
	agentsSvc.SetRevokeHook(agentStreams)
	agentSvc, err := agentapi.New(agentapi.Deps{
		CA: caInst, Agents: agentsSvc, Audit: systemAuditor{auditRec},
		Streams: agentStreams, Facts: agentsSvc, Checks: checksSvc,
		Clock: time.Now, Version: cfg.Version, Log: slog.Default(),
	})
	if err != nil {
		appDB.Close()
		return nil, fmt.Errorf("build agent service: %w", err)
	}

	// Notifications (spec §5): every outbound request goes through the
	// outbound client, which refuses internal destinations unless
	// PULSIX_NOTIFY_ALLOW_CIDRS admits them. The sender is shared by the
	// dispatcher below and the synchronous "Send test".
	notifySender := notify.NewChannelSender(box, mailer,
		outbound.NewClient(outbound.NewPolicy(cfg.NotifyAllowCIDRs, cfg.NotifyNAT64Prefixes), outbound.DefaultTimeout, nil), time.Now)
	notifyStore := notify.NewSQLStore(appDB)
	notifySvc := notify.NewService(notify.Deps{
		Channels: notifyStore, Deliveries: notifyStore, Rules: notifyStore, Digests: notifyStore, Box: box, Mail: mailer, Sender: notifySender,
		PublicURL: cfg.PublicURL, Now: time.Now, Rand: rand.Reader, Log: slog.Default(),
	})

	// Maintenance windows (spec §6): group windows resolve through the same
	// scope resolver runs use; the schedules' maintenance guard asks them.
	maintenanceSvc := maintenance.NewService(maintenance.NewSQLStore(appDB), NewGroupHosts(resolver), time.Now, rand.Reader, slog.Default())
	schedulesSvc.SetMaintenance(maintenanceSvc)

	// alerts.Service (v0.4a): built here, ahead of api.New, so the same
	// instance backs both the API's own read/acknowledge/snooze surface
	// (Deps.Alerts, registerAlertRoutes) and the evaluation ticker's
	// required AlertSink below — one wire, not two to keep in sync.
	alertsSvc := alerts.NewService(alerts.NewSQLStore(appDB), time.Now)

	// alertroutes.Service (v0.4b Task 10): built here, ahead of api.New, on
	// the same *alertroutes.Store the follower below drives directly
	// (NewFollower takes the concrete store, not this Service) — one store,
	// not two, the same sharing alertsSvc's own doc comment above explains
	// for alerts. GroupsForHost comes from checksSvc through the adapter
	// alertroutes_ports.go builds, exactly what the follower uses for the
	// same lookup.
	alertRoutesStore := alertroutes.NewSQLStore(appDB)
	// alertsSvc's own notification-hold port (v0.4c Task 11, alerts.NotifyLedger):
	// *alertroutes.Store already satisfies it (NotifyStatusForAlert,
	// ledger_sql.go) without an adapter, since both packages agree on the
	// signature. Wired here, right after the store exists, rather than
	// passed into alerts.NewService above — alertsSvc is built before this
	// store exists at all, and SetNotifyLedger (the same post-construction
	// shape schedulesSvc.SetMaintenance takes a few lines up) is what lets
	// construction order stay "alerts first, routes second" without a
	// forward reference.
	alertsSvc.SetNotifyLedger(alertRoutesStore)
	// The on-call store the follower resolves a route's rotation through
	// (alertroutes.RotationReader). Built here beside the route store for
	// the same reason: one store on the shared pool, handed to whoever
	// needs it, rather than a second instance per consumer.
	oncallStore := oncall.NewSQLStore(appDB)
	newUUIDv7 := func() uuid.UUID {
		id, err := uuid.NewV7FromReader(rand.Reader)
		if err != nil {
			panic(err) //nolint:forbidigo // entropy failure is unrecoverable, like uuid.Must
		}
		return id
	}
	alertRoutesSvc := alertroutes.NewService(alertRoutesStore, NewAlertRouteGroups(checksSvc), time.Now, newUUIDv7)
	// oncall.Service (v0.4c Task 8): the API's own rotation CRUD and roster
	// surface, built on the same store the follower above resolves a
	// route's rotation through directly (NewFollower takes the concrete
	// store, not this Service) — one store, not two, alertRoutesSvc's own
	// doc comment above explains the same sharing for alertRoutesStore.
	oncallSvc := oncall.NewService(oncallStore, time.Now, newUUIDv7)

	// Backups (backlog 7.2): the service dumps through appDB itself; its
	// schedule starts below, once nothing can fail any more.
	backupsSvc := newBackupService(appDB, box, cfg.MasterKey, mailer, cfg.Version)

	accountsSvc := accounts.NewService(accounts.NewSQLStore(appDB), mailer, cfg.PublicURL, time.Now, rand.Reader)
	handler, err := api.New(api.Deps{
		Sessions: sessions, Auth: authSvc, TOTP: totp, Passkeys: passkeys, Tokens: tokens, Enrolment: enrolment,
		Tenancy: tenancySvc, Accounts: accountsSvc,
		Audit: auditRec, ClientIP: clientip.New(cfg.TrustedProxies), Setup: setupSvc, Mailer: mailer, Hosts: hostsSvc, Secure: publicURLIsHTTPS(cfg.PublicURL),
		Backups: backupsSvc,
		// Backlog 1.20: the auth endpoints' per-address limiter size
		// (PULSIX_AUTH_RATE_LIMIT; 0 from config disables it).
		AuthRateLimit: cfg.AuthRateLimit,
		Credentials:   credsSvc, SSH: sshSvc,
		Inventory: inventorySvc, Renderer: renderer,
		Repos: reposSvc, Shared: sharedSvc, Playbooks: playbooksSvc, Templates: templatesSvc,
		Jobs: jobsSvc, Schedules: schedulesSvc, Triggers: triggersSvc, Version: cfg.Version,
		Workers: workersSvc, WorkerEndpoint: api.WorkerEndpoint{Addr: cfg.GRPCPublicAddr, ServerName: cfg.CAListenHost()},
		// The Agents page and the host Agent card read the same registry
		// the gRPC enrollment surface writes (agentapi above).
		Agents: agentsSvc,
		// The check pipeline's own surface (v0.3b-1): the same
		// checks.Service the resolver and the agent stream already run on,
		// plus the write-path push hook — after a definition or monitoring
		// group changes, the API asks the agent service to hand the
		// affected hosts their freshly resolved set (api.CheckConfigPusher,
		// satisfied by *agentapi.Service.PushHosts).
		Checks: checksSvc, AgentPush: agentSvc,
		// The rule engine's own surface (v0.3b-2 Task 10): the same
		// rulesSvc instance the org-created hook above seeds with, and
		// checksSvc for the effective view's group membership and current
		// values (registerRuleRoutes).
		Rules: rulesSvc,
		// The fleet overview and the host page's tuple states (v0.3b-2 Task
		// 11): the separately wired read-only store above.
		RuleStates: ruleStates,
		// Discovery and item decisions (v0.3b-3 Task 6): the same instance
		// the organization-created hook graph does not touch (items has no
		// seed set), built above alongside itemsStore. EvalDirty marks a
		// decision's affected host(s) dirty the moment a mutating route
		// commits, through the identical upsert internal/checks' ingest
		// path uses — a separate, cheap wrapper on appDB (rules.DirtyMarker
		// carries no port the ticker's own EvalSQLStore needs for this one
		// statement).
		Items:     itemsSvc,
		EvalDirty: rules.NewDirtyMarker(appDB),
		// The public agent distribution: the install script is templated
		// with this manager's own public URL, and the binaries it hands
		// out are the ones embedded at build time — nil in a build made
		// without `make agent-binaries`, which the route reports.
		PublicURL: cfg.PublicURL, AgentBinaries: agentdist.Files(),
		Notify:      notifySvc,
		Maintenance: maintenanceSvc,
		Calendar:    calendar.NewService(schedulesSvc, jobsSvc, maintenanceSvc, time.Now),
		Alerts:      alertsSvc,
		AlertRoutes: alertRoutesSvc,
		Oncall:      oncallSvc,
	})
	if err != nil {
		appDB.Close()
		return nil, fmt.Errorf("build api handler: %w", err)
	}

	// The worker gRPC endpoint is opt-in via PULSIX_GRPC_LISTEN; its ports
	// are the real job queue from Task 5 (jobs_ports.go adapts it to the
	// grpcsrv interfaces). It shares workersSvc and the stream registry
	// built above; the certificate lookup and Touch behind the interceptor
	// run outside any org scope (workers.Store). v0.3a mounts the agent
	// service on the same listener; the interceptor resolves agent
	// certificates through agentsSvc (agents.Store, org-unscoped serial
	// lookups).
	//
	// The reaper runs regardless of the gRPC listener: enqueued jobs need
	// their stale claims swept even before a worker ever connects. It is
	// stopped in Close.
	reaperStop := startLoop(jobs.NewReaper(jobsQueue, 0, cfg.JobMaxAttempts).Run)

	// The daily connection re-test: one pass per interval (default 1h),
	// staggered per host, under its own advisory lock. Stopped in Close.
	retestStop := startLoop(schedule.NewRetest(hostsSvc, jobsSvc, schedule.NewAdvisoryLocker(appDB.Pool(), schedule.AdvisoryKeyRetest), cfg.RetestInterval, nil, time.Now, slog.Default()).Run)

	// The expired-ceremony and expired-session reaper (backlog 1.21): one
	// DELETE pass per interval (default 1h) under its own advisory key,
	// bounding the two unauthenticated/expired write paths' growth.
	// Stopped in Close.
	authReaperStop := startLoop(auth.NewReaper(q, schedule.NewAdvisoryLocker(appDB.Pool(), auth.AdvisoryKeyAuthReaper), o.authReaperInterval, time.Now, slog.Default()).Run)

	// The schedule ticker (spec §2): one per manager; SKIP LOCKED makes
	// several safe. Stopped in Close and on every early return below;
	// stopping waits for a batch still in flight (startLoop).
	// The notification outbox (spec §7): a finished scheduled run group is
	// reported from inside its terminal transaction, a skipped, missed or
	// failed decision from inside the ticker's row savepoint.
	outbox := notify.NewOutbox(appDB, cfg.PublicURL, time.Now, rand.Reader, slog.Default())
	jobsQueue.SetRunGroupSink(outbox)
	dueStore := schedules.NewDueSQLStore(appDB, rand.Reader, slog.Default())
	dueStore.SetOutbox(outbox)
	tickerStop := startLoop(schedules.NewTicker(schedulesSvc, dueStore, cfg.ScheduleTick, o.scheduleNow, nil, slog.Default()).Run)

	// The notification dispatcher (spec §5): one per manager; the claim's
	// SKIP LOCKED and lease make several safe. Stopped in Close and on
	// every early return below.
	dispatchStop := startLoop(notify.NewDispatcher(notify.NewDeliverySQLStore(appDB), notifySender, notify.DefaultDispatchInterval, time.Now, nil, slog.Default()).Run)

	// The weekly digest (spec §7): one ticker per manager, SKIP LOCKED
	// like the schedule ticker; schedulesSvc builds the week's summary.
	digestStop := startLoop(notify.NewDigestTicker(notify.NewDigestSQLStore(appDB, slog.Default(), auditRec), schedulesSvc, outbox, notify.DefaultDigestTick, time.Now, nil, slog.Default()).Run)

	// The rule evaluation ticker (v0.3b-2 spec, "Trigger"): one per
	// manager, every 15 s, claiming dirty-or-due hosts with FOR UPDATE SKIP
	// LOCKED exactly like the schedule ticker above — which is what makes
	// several managers safe without a claim protocol of its own. It runs
	// unscoped, across every organization; its store reads a host's checks
	// and group membership back through checksSvc (rules_ports.go), the
	// same resolver the agent's own configuration push uses. Stopped in
	// Close and on every early return below; stopping waits for a batch
	// still in flight (startLoop).
	//
	// alertsSvc (built above, ahead of api.New) is the ticker's required
	// AlertSink: every host's evaluation now feeds its states, changes and
	// roll-up into the alert lifecycle on the same per-host transaction,
	// and the same ticker sweeps orphaned alerts once per tick. There is
	// no second wire to forget — NewTicker panics at construction if this
	// argument is ever left out.
	evalTicker := rules.NewTicker(rules.NewEvalSQLStore(appDB, NewRulesHostChecks(checksSvc), itemsStore, slog.Default()), alertsSvc)
	evalStop := startLoop(func(ctx context.Context) { _ = evalTicker.Run(ctx) })

	// The alert follower (v0.4b spec, "The follower"): one tick per
	// organization per minute, each inside a single transaction under that
	// organization's own advisory lock — which is what makes several
	// managers safe here, the way SKIP LOCKED does for the two tickers
	// above. It walks alert_events, resolves every event against the
	// organization's routes, and hands the outbox above one batch per
	// channel; the ledger write and the enqueue share that transaction, so
	// an event is never recorded as reported without the delivery that
	// reports it. Host→group membership comes from the same checksSvc the
	// rule engine resolves through (alertroutes_ports.go). Stopped in
	// Close and on every early return below.
	alertFollower := alertroutes.NewFollower(appDB, alertRoutesStore, NewAlertRouteGroups(checksSvc),
		oncallStore, outbox, cfg.PublicURL, time.Now, slog.Default())
	alertNotifyStop := startLoop(func(ctx context.Context) { alertFollower.Run(ctx, alertroutes.DefaultFollowInterval) })

	var grpcServer *grpc.Server
	var stopEmbedded func()
	if cfg.GRPCListen != "" {
		grpcServer, err = grpcsrv.New(ctx, grpcsrv.Deps{
			CA: caInst, Workers: workersSvc,
			Agents: agentsSvc, AgentService: agentSvc,
			Claims: jobsClaimer{jobsQueue}, Events: jobsEventRecorder{jobsQueue}, Results: jobsResultRecorder{jobsQueue},
			Audit: systemAuditor{auditRec}, Repos: repoIDLister{reposSvc}, Clock: time.Now, Streams: streams, Log: slog.Default(),
			Version: cfg.Version,
		})
		if err != nil {
			reaperStop()
			retestStop()
			authReaperStop()
			tickerStop()
			dispatchStop()
			digestStop()
			evalStop()
			alertNotifyStop()
			appDB.Close()
			return nil, fmt.Errorf("build gRPC server: %w", err)
		}

		// The embedded worker (PULSIX_EMBEDDED_WORKER): config.Load
		// already refuses EmbeddedWorker without GRPCListen, so this only
		// guards a Config built directly in a test. It dials the gRPC
		// endpoint's own port over loopback — cmd/manager's serve() binds
		// exactly cfg.GRPCListen after Build returns, so the port derived
		// here (":8444" → "8444") is the one that will actually be
		// listening; the dial is lazy (see worker.Dial's own doc comment),
		// so it tolerates the brief window before that bind happens.
		if cfg.EmbeddedWorker {
			_, port, splitErr := net.SplitHostPort(cfg.GRPCListen)
			if splitErr != nil {
				reaperStop()
				retestStop()
				authReaperStop()
				tickerStop()
				dispatchStop()
				digestStop()
				evalStop()
				alertNotifyStop()
				appDB.Close()
				return nil, fmt.Errorf("PULSIX_EMBEDDED_WORKER: resolve port from PULSIX_GRPC_LISTEN %q: %w", cfg.GRPCListen, splitErr)
			}
			warnEmbeddedWorker(slog.Default())
			stopEmbedded = startEmbeddedWorker(appDB, caInst, workersSvc, net.JoinHostPort("127.0.0.1", port), cfg.CAListenHost(), slog.Default())
		}
	}

	// The backup schedule: one ticker per manager; the attempt row's claim
	// on its slot makes several safe. An attempt still recorded as running
	// while no backup holds the lock was left by a crash or a restore.
	if err := backupsSvc.AbandonStale(ctx); err != nil {
		slog.ErrorContext(ctx, "abandon stale backup attempts", "err", err)
	}
	backupTickerStop := startLoop(backup.NewTicker(backupsSvc, backup.DefaultTick).Run)

	return &App{
		Handler: handler, GRPC: grpcServer, GRPCListen: cfg.GRPCListen, db: appDB,
		stopReaper: reaperStop, stopRetest: retestStop, stopAuthReaper: authReaperStop, stopTicker: tickerStop, stopDispatch: dispatchStop, stopDigest: digestStop, stopEval: evalStop, stopAlertNotify: alertNotifyStop, stopEmbedded: stopEmbedded,
		items: itemsSvc, backups: backupsSvc, stopBackupTicker: backupTickerStop, background: accountsSvc,
	}, nil
}

// publicURLIsHTTPS reports whether cfg.PublicURL uses the https scheme, so
// Build can set the session cookie's Secure attribute to match how the
// manager is actually being served — cfg.PublicURL is already validated as
// an absolute URL by config.Load, so the parse error here is unreachable in
// practice and only guards against that assumption changing.
func publicURLIsHTTPS(publicURL string) bool {
	u, err := url.Parse(publicURL)
	return err == nil && u.Scheme == "https"
}

// assertCannotBypassRLS queries pg_roles for the role appDB actually
// connects as and refuses to let the manager start if that role is a
// superuser or carries BYPASSRLS — either attribute makes every row-level
// security policy from Task 4 onward silently inert for every query this
// pool ever runs, which would mean one tenant's data leaking into
// another's session on a misconfigured deployment. This is checked, not
// assumed: db.OpenAsRole issuing SET ROLE appRole is what production wiring
// intends, but only this query, against pg_roles.rolname = current_user
// (the *effective* role after SET ROLE, not the login role in the
// connection string), proves it actually took effect for the connections
// this pool hands out.
func assertCannotBypassRLS(ctx context.Context, appDB *db.DB) error {
	var effectiveRole string
	var isSuperuser, bypassesRLS bool
	err := appDB.Pool().QueryRow(ctx,
		`SELECT current_user, rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`,
	).Scan(&effectiveRole, &isSuperuser, &bypassesRLS)
	if err != nil {
		return fmt.Errorf("check effective database role privileges: %w", err)
	}
	if isSuperuser || bypassesRLS {
		return fmt.Errorf(
			"refusing to start: database role %q can bypass row-level security (superuser=%v, bypassrls=%v) — "+
				"PULSIX_DATABASE_URL must authenticate as a role that can SET ROLE into %q but is not itself a "+
				"superuser or BYPASSRLS, otherwise every organization's data isolation is silently inert",
			effectiveRole, isSuperuser, bypassesRLS, appRole,
		)
	}
	return nil
}
