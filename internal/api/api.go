// Package api builds the HTTP handler for the Pulsix manager.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/accounts"
	"github.com/noixdev/pulsix/internal/agentenroll"
	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/alertroutes"
	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/calendar"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/clientip"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/items"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/mail"
	"github.com/noixdev/pulsix/internal/maintenance"
	"github.com/noixdev/pulsix/internal/mcp"
	"github.com/noixdev/pulsix/internal/monitorsettings"
	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/oncall"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/preferences"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/rules"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/screenlock"
	"github.com/noixdev/pulsix/internal/setup"
	"github.com/noixdev/pulsix/internal/shared"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/tenancy"
	"github.com/noixdev/pulsix/internal/triggers"
	"github.com/noixdev/pulsix/internal/web"
	"github.com/noixdev/pulsix/internal/workers"
)

// Deps carries everything the handlers need. Handlers call services only.
type Deps struct {
	Sessions *auth.Sessions
	Auth     AuthService
	TOTP     *auth.TOTP
	// ScreenLock is the session lock and its PIN (backlog 3.93); the gates
	// read a session's lock from the principal, /auth/me reads the PIN state.
	ScreenLock *screenlock.Service
	// Preferences is the user's own appearance settings (backlog 3.97–3.100).
	Preferences *preferences.Service
	Passkeys    *auth.Passkeys
	Tokens      *auth.Tokens
	// PersonalTokens is personal tokens (backlog 3.100): what one may do in
	// one organization (its grant's cap and the grant's use), the owner's
	// management of them and an organization's view of their grants.
	PersonalTokens PersonalTokenService
	// Enrolment composes confirming a second factor with completing the
	// session it was pending for, in one transaction (backlog 1.26).
	Enrolment *auth.Enrolment
	Tenancy   *tenancy.Service
	// Accounts is account administration: changing one's own password
	// (backlog 7.74).
	Accounts *accounts.Service
	Audit    *audit.Recorder
	// ClientIP resolves request source addresses behind
	// PULSIX_TRUSTED_PROXIES; required.
	ClientIP *clientip.Resolver
	Setup    *setup.Service
	Hosts    *hosts.Service
	// Credentials is the organization's encrypted secret store (SSH keys,
	// passwords, Git deploy keys and tokens); see registerCredentialRoutes.
	Credentials *credentials.Service
	// OrgSecrets is the organization's secret store for the checks the
	// server runs (backlog 3.13d); see registerOrgSecretRoutes. An
	// interface without Fields: no handler can open a value.
	OrgSecrets OrgSecretStore
	// SSH stores jump hosts, zone defaults and host SSH settings; see
	// registerSSHRoutes.
	SSH *sshconf.Service
	// Inventory manages inventory groups, their schemas, values and
	// memberships; see registerInventoryRoutes / registerInventoryMemberRoutes.
	Inventory *inventory.Service
	// Renderer turns Inventory's stored state into an Ansible dynamic
	// inventory (masked for the preview endpoints); see
	// registerInventoryMemberRoutes.
	Renderer *inventory.Renderer
	// Repos is the git repository registry; see registerRepoRoutes.
	Repos *repos.Service
	// Shared is the installation-owned definition/projection mechanism
	// (spec v0.2f, decisions 1, 2, 4, 5, 7, 8): an instance admin's own CRUD
	// over all three kinds of definition — repositories, job templates and
	// check definitions — each projected into every organization as an
	// ordinary repos.Repo, templates.Template or checks.Definition (their
	// own SharedRepoID/SharedTemplateID/SharedDefinitionID marker). See
	// registerSharedRepoRoutes, registerSharedTemplateRoutes and
	// registerSharedCheckRoutes (shared_handlers.go) — every route any of
	// the three wires is instance-admin-only; nothing org-scoped calls this
	// field.
	Shared *shared.Service
	// Playbooks stores repo and editor playbooks; see registerPlaybookRoutes.
	Playbooks *playbooks.Service
	// Templates is the job-template registry (a run's playbook, scope,
	// limit, extra vars and default credential); see
	// registerTemplateRoutes.
	Templates *templates.Service
	// Jobs is the org-scoped job queue; the connection-test trigger route
	// enqueues through it (registerHostopsRoutes).
	Jobs *jobs.Service
	// Assistant is the per-organization assistant access switch (spec
	// 2026-11-16 assistant access).
	Assistant AssistantSettings
	// RunRequests are the run requests an assistant makes and a person
	// approves (spec 2026-11-16 assistant access).
	RunRequests *runrequests.Service
	// Chain reads a run's follow-ups (job chaining) for the run detail's
	// chain block.
	Chain ChainReader
	// ChainDismisser dismisses a run's pending follow-ups; see
	// registerChainRoutes.
	ChainDismisser ChainDismisser
	// Schedules fires job templates on a timetable; see
	// registerScheduleRoutes.
	Schedules *schedules.Service
	// Triggers is the webhook trigger registry and receive pipeline. The
	// two public hook routes call it directly; see registerHookRoutes.
	// Its own org-scoped CRUD routes are registered by a later task.
	Triggers *triggers.Service
	// Workers is the worker registry (Workers page, enrollment tokens).
	Workers *workers.Service
	// Agents is the host-agent registry (Agents page, host Agent card,
	// enrollment tokens); v0.3a, spec §8.
	Agents *agents.Service
	// AgentEnroll previews and confirms the hosts a person's run of a
	// template with agent enrollment enrols (spec 2026-11-25 B).
	AgentEnroll *agentenroll.Service
	// Checks is the monitoring check pipeline (v0.3b-1, spec
	// docs/superpowers/specs/2026-09-27-v0.3b-checks-design.md): check
	// definitions, monitoring groups, the per-host effective set and the
	// stored samples behind the graphs. See registerCheckRoutes,
	// registerMonitoringGroupRoutes and registerHostCheckRoutes.
	Checks *checks.Service
	// Vantages reads the vantage points of the synthetic checks probed
	// from several places (backlog 3.13a): the vantage table on the host
	// page and one vantage's graph. See registerHostCheckRoutes.
	Vantages VantageReader
	// Rules is the rule engine's own surface (v0.3b-2 spec, "API and
	// permissions"): rule CRUD, the validate-only route the editor drives,
	// and — together with Checks (for a host's group membership and
	// current values) — the per-host effective view. See registerRuleRoutes.
	Rules *rules.Service
	// RuleStates is the rule engine's read-only side (v0.3b-2 Task 11): the
	// evaluator's own store (rules.EvalSQLStore) is the only writer to
	// check_states and check_host_states, so the API depends on the
	// narrower rules.StateStore instead of the write-capable one — the
	// same split Rules/RuleStates draws that rules.StateStore's own doc
	// comment describes. Backs the state/since/flapping fields of
	// GET /hosts/{id}/checks (registerHostCheckRoutes) and the whole of
	// GET /fleet (registerFleetRoutes).
	RuleStates rules.StateStore
	// Items is discovery and item decisions' own surface (v0.3b-3, spec
	// docs/superpowers/specs/2026-10-01-v0.3b-3-discovery-design.md): item
	// CRUD reads, decision CRUD, and the preview a pattern editor drives
	// before anyone has chosen ignore or watch. See registerItemRoutes and
	// GET /hosts/{id}/checks (registerHostCheckRoutes), which resolves
	// `ignored` through the same items.Decide the evaluation ticker runs.
	Items *items.Service
	// EvalDirty marks a host's evaluation cursor dirty outside the
	// evaluator's own write path — see EvalDirtyMarker's own doc comment
	// (item_handlers.go) for why the item-decisions routes need this at
	// all. rules.DirtyMarker (a value type) satisfies it in production.
	EvalDirty EvalDirtyMarker
	// MonitorSettings is the organization's monitoring settings (backlog
	// 3.57: client_absent_days); see registerMonitoringSettingsRoutes.
	MonitorSettings *monitorsettings.Service
	// AgentPush delivers a host's freshly resolved check configuration to
	// whatever agent is currently connected for it, after a check
	// definition or monitoring group write commits. See CheckConfigPusher.
	AgentPush CheckConfigPusher
	// Notify manages notification channels, their delivery log, schedule
	// notification rules and the weekly digest (spec 2026-09-18 §5, §7).
	Notify *notify.Service
	// Maintenance manages maintenance windows (spec 2026-09-18 §6).
	Maintenance *maintenance.Service
	// Calendar lays out schedule occurrences and maintenance windows
	// (spec 2026-09-18 §8).
	Calendar *calendar.Service
	// Alerts is the alert lifecycle's own read/acknowledge/snooze surface
	// (v0.4a design doc, "API and permissions"): List, Get and the four
	// operator actions. The same instance also backs the evaluation
	// ticker's AlertSink in production (internal/app.Build) — nothing
	// here drives that side, only the six routes registerAlertRoutes
	// wires.
	Alerts *alerts.Service
	// AlertRoutes is the bridge between monitoring and delivery (v0.4b
	// design doc): which alert reaches which notification channel. The
	// same instance also backs the follower in production
	// (internal/app.Build) — nothing here drives that side, only the six
	// routes registerAlertRouteRoutes wires (CRUD plus the per-host union
	// view, GET /orgs/{org}/hosts/{id}/alert-routes, backlog 4.1b).
	AlertRoutes *alertroutes.Service
	// Oncall is the on-call rotation surface (v0.4c design doc, "API and
	// permissions"): rotation CRUD and the roster view a rotation's edit
	// screen needs. The same instance also backs the follower's rotation
	// lookup in production (internal/app.Build) — nothing here drives that
	// side, only the six routes registerOncallRoutes wires.
	Oncall *oncall.Service
	// Backups is the instance backup service (backlog 7.2): settings, the
	// backup list, "Back up now", download and delete. See
	// registerBackupRoutes and registerBackupDownloadRoute.
	Backups BackupService
	// AuthRateLimit is how many failed authentication attempts one client
	// address may make per minute on each rate-limited auth endpoint
	// (login, TOTP verify, passkey login finish, invite accept) —
	// PULSIX_AUTH_RATE_LIMIT, wired by app.Build from config.Load, which
	// defaults it to config.DefaultAuthRateLimit. 0 disables the limiter
	// (for tests); production leaves the default or higher. See
	// withAuthRateLimit (auth_rate_limit.go).
	AuthRateLimit int
	// MFAOptional is PULSIX_MFA_OPTIONAL (demo and test instances only): an
	// account without a second factor signs in with its password alone,
	// and a fresh password is its step-up (mfa_optional.go). An account
	// with a factor is unaffected. app.Build builds Auth and Setup with the
	// same switch.
	MFAOptional bool
	// WorkerEndpoint is what remote workers dial; empty Addr disables enrollment.
	WorkerEndpoint WorkerEndpoint
	// PublicURL is this manager's own externally reachable base URL
	// (PULSIX_PUBLIC_URL). The agent install script is templated with it,
	// so a host that pipes GET /install.sh into a shell downloads the
	// agent binary from the manager it is being installed against; empty
	// (only tests and a half-wired Deps) makes that route answer 503
	// instead of serving a script with nowhere to download from.
	PublicURL string
	// AgentBinaries is the agent distribution GET /agents/bin/... serves:
	// agentdist.Files(), which is nil in a build that did not run
	// `make agent-binaries` (the committed placeholder dist carries no
	// binary). nil makes the binary route answer 503 naming that build
	// step — the manager, the SPA and every other route work regardless.
	AgentBinaries fs.FS
	// Mailer is the concrete SMTP sender, not the narrower tenancy.Mailer
	// interface tenancy.Service is built with: only this package's own
	// POST /admin/settings/smtp/test handler needs it, to run a live
	// Test() against whatever settings are currently stored, so its own
	// error (not just tenancy.Service's Send) is what the wizard's SMTP
	// test surfaces.
	Mailer *mail.SMTP
	Secure bool // set Secure on cookies (true behind HTTPS)
	// Version is the manager's build version, reported on GET /healthz
	// and GET /setup/status. Left empty by every existing caller except
	// app.Build (which sets it from config.Config.Version, itself set by
	// cmd/manager/main.go's ldflags-stamped `version` variable) —
	// managerVersion defaults an empty value to "dev", so no caller needs
	// to know about this field just to keep building a working Deps.
	Version string
	// WaitSleep replaces the pause between two reads of a long-polled job or
	// run request (wait=); nil sleeps for real. Tests drive the wait with it.
	WaitSleep func(ctx context.Context, d time.Duration) error
}

// managerVersion is the build version the public routes report: Deps.Version,
// or "dev" when it is unset.
func managerVersion(deps Deps) string {
	if deps.Version == "" {
		return "dev"
	}
	return deps.Version
}

// scalarVersion and scalarIntegrity pin the third-party script the API
// explorer loads. Both were captured by fetching the published file for
// this exact version and hashing it, not invented:
//
//	curl -s https://cdn.jsdelivr.net/npm/@scalar/api-reference@1.68.0 | openssl dgst -sha384 -binary | openssl base64 -A
//
// jsdelivr serves npm packages with an immutable, version-pinned URL, so
// this hash will keep matching that URL forever; bumping scalarVersion
// requires recomputing scalarIntegrity the same way.
const (
	scalarVersion   = "1.68.0"
	scalarIntegrity = "sha384-ayGz8N+NChlUEfR0zr5Zy3T6Q4lhcdiASJNoshS6+vxV56ZE300qfWNBjj9pqsLN"
)

type healthOutput struct {
	Body struct {
		Status string `json:"status" example:"ok"`
	}
}

// validate reports which required fields are missing. The auth routes are
// registered unconditionally below and dereference Sessions/Auth on every
// request, so a Deps that omits either must fail loudly at construction
// time instead of turning into a nil-pointer panic (recovered into an
// opaque 500) on the first real request.
func (d Deps) validate() error {
	var missing []string
	if d.Sessions == nil {
		missing = append(missing, "Sessions")
	}
	if d.Auth == nil {
		missing = append(missing, "Auth")
	}
	if d.TOTP == nil {
		missing = append(missing, "TOTP")
	}
	if d.Passkeys == nil {
		missing = append(missing, "Passkeys")
	}
	if d.Tokens == nil {
		missing = append(missing, "Tokens")
	}
	if d.Preferences == nil {
		missing = append(missing, "Preferences")
	}
	if d.PersonalTokens == nil {
		missing = append(missing, "PersonalTokens")
	}
	if d.Assistant == nil {
		missing = append(missing, "Assistant")
	}
	if d.RunRequests == nil {
		missing = append(missing, "RunRequests")
	}
	if d.Enrolment == nil {
		missing = append(missing, "Enrolment")
	}
	if d.Tenancy == nil {
		missing = append(missing, "Tenancy")
	}
	if d.Accounts == nil {
		missing = append(missing, "Accounts")
	}
	if d.Audit == nil {
		missing = append(missing, "Audit")
	}
	if d.ClientIP == nil {
		missing = append(missing, "ClientIP")
	}
	if d.Setup == nil {
		missing = append(missing, "Setup")
	}
	if d.Mailer == nil {
		missing = append(missing, "Mailer")
	}
	if d.Hosts == nil {
		missing = append(missing, "Hosts")
	}
	if d.Credentials == nil {
		missing = append(missing, "Credentials")
	}
	if d.OrgSecrets == nil {
		missing = append(missing, "OrgSecrets")
	}
	if d.SSH == nil {
		missing = append(missing, "SSH")
	}
	if d.Inventory == nil {
		missing = append(missing, "Inventory")
	}
	if d.Renderer == nil {
		missing = append(missing, "Renderer")
	}
	if d.Repos == nil {
		missing = append(missing, "Repos")
	}
	if d.Playbooks == nil {
		missing = append(missing, "Playbooks")
	}
	if d.Templates == nil {
		missing = append(missing, "Templates")
	}
	if d.Jobs == nil {
		missing = append(missing, "Jobs")
	}
	if d.Chain == nil {
		missing = append(missing, "Chain")
	}
	if d.ChainDismisser == nil {
		missing = append(missing, "ChainDismisser")
	}
	if d.Schedules == nil {
		missing = append(missing, "Schedules")
	}
	if d.Triggers == nil {
		missing = append(missing, "Triggers")
	}
	if d.Workers == nil {
		missing = append(missing, "Workers")
	}
	if d.Agents == nil {
		missing = append(missing, "Agents")
	}
	if d.AgentEnroll == nil {
		missing = append(missing, "AgentEnroll")
	}
	if d.Checks == nil {
		missing = append(missing, "Checks")
	}
	if d.Rules == nil {
		missing = append(missing, "Rules")
	}
	if d.RuleStates == nil {
		missing = append(missing, "RuleStates")
	}
	if d.Items == nil {
		missing = append(missing, "Items")
	}
	if d.EvalDirty == nil {
		missing = append(missing, "EvalDirty")
	}
	if d.MonitorSettings == nil {
		missing = append(missing, "MonitorSettings")
	}
	if d.Vantages == nil {
		missing = append(missing, "Vantages")
	}
	if d.AgentPush == nil {
		missing = append(missing, "AgentPush")
	}
	if d.Notify == nil {
		missing = append(missing, "Notify")
	}
	if d.Maintenance == nil {
		missing = append(missing, "Maintenance")
	}
	if d.Calendar == nil {
		missing = append(missing, "Calendar")
	}
	if d.Alerts == nil {
		missing = append(missing, "Alerts")
	}
	if d.AlertRoutes == nil {
		missing = append(missing, "AlertRoutes")
	}
	if d.Oncall == nil {
		missing = append(missing, "Oncall")
	}
	if d.Backups == nil {
		missing = append(missing, "Backups")
	}
	if len(missing) > 0 {
		return errors.New("api.New: Deps missing required field(s): " + strings.Join(missing, ", "))
	}
	return nil
}

// New returns the root http.Handler: chi router + huma API under /api/v1.
// It refuses to build a handler for a half-wired Deps rather than serve
// routes that cannot work; see Deps.validate.
func New(deps Deps) (http.Handler, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}

	r := chi.NewRouter()
	// middleware.RealIP is intentionally omitted: it is deprecated in this
	// chi version because it mutates RemoteAddr from client-controlled
	// headers without a trusted-proxy allowlist (IP spoofing risk). A
	// later task can add a spoof-safe replacement once the deployment's
	// reverse-proxy trust boundary is known.
	r.Use(middleware.RequestID, requestLogger(os.Stdout), middleware.Recoverer)
	// Before anything can answer: the headers belong on every response,
	// including the ones the middleware below produces (backlog 1.29).
	r.Use(securityHeaders)
	// Before any session is looked at: a cross-origin browser mutation is
	// refused whoever it would authenticate as (backlog 7.38).
	r.Use(crossOriginProtection)
	// staticSessionSkip wraps CookieMiddleware with backlog 1.22's
	// short-circuit: requests outside the API subtree (the SPA routes and
	// embedded assets web.Handler serves as the NotFound fallback, plus
	// /healthz) skip the session lookup entirely — see session_skip.go for
	// the documented route set.
	r.Use(staticSessionSkip(auth.CookieMiddleware(deps.Sessions)))
	// auth.BearerMiddleware is deliberately NOT mounted here (Finding 1, fix
	// round 1; carried forward from Task 9). Mounting it on this router —
	// the router every route in this package attaches to — would make a
	// bearer principal (which Tokens.Resolve always marks MFAComplete:
	// true, by construction) reach every route mounted below, including the
	// account-security routes (passkey register/list/delete, TOTP
	// enrol/confirm/delete) that only gate on requireMFAComplete: a
	// viewer-scoped token could register an attacker-controlled passkey or
	// plant a TOTP secret on its owner's account and take it over
	// completely. Those routes use requireInteractiveSession
	// (auth_handlers.go), which rejects a token principal outright, so they
	// stay safe regardless — but nothing about this router-level mount
	// would be scoped to only the routes that actually need a bearer
	// credential.
	//
	// Task 10 mounts BearerMiddleware instead on a *huma.Group scoped to
	// exactly the org-scoped routes that consult
	// auth.TokenScopeFromContext (via requireOrg, authz.go) — organizations,
	// members, invites and tokens under /api/v1/orgs/{org}/... — see
	// registerRoutes below and bearerAuthMiddleware (org_handlers.go) for
	// how a *huma.Group achieves per-operation scoping without a second chi
	// router.
	r.Use(requireMFAForExplorer)

	healthzVersion := managerVersion(deps)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Status  string `json:"status"`
			Version string `json:"version"`
		}{Status: "ok", Version: healthzVersion})
	})

	// The public agent distribution (GET /install.sh, GET /install.ps1,
	// GET /agents/bin/...): outside /api on purpose — an installing host
	// fetches them with curl or PowerShell before it has any credential at
	// all. See agent_install.go.
	if err := registerInstallRoutes(r, deps); err != nil {
		return nil, err
	}

	// The backup download streams a file, which huma cannot; it takes the
	// same instance-admin session guard as the backup routes registered
	// with the API below. See backup_download.go.
	registerBackupDownloadRoute(r, deps)

	r.Get("/api/explorer", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Pulsix API</title></head><body>
<script id="api-reference" data-url="/api/openapi.json"></script>
<script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference@` + scalarVersion + `"
        integrity="` + scalarIntegrity + `"
        crossorigin="anonymous"></script></body></html>`))
	})

	cfg := huma.DefaultConfig("Pulsix API", "0.1.0")
	cfg.OpenAPIPath = "/api/openapi"
	// huma serves every type's JSON Schema here. It describes the same API
	// as the OpenAPI document, so it lives under /api where the session is
	// resolved and requireMFAForExplorer gates it; huma's default
	// "/schemas" sat outside both (backlog 7.15).
	cfg.SchemasPath = "/api/schemas"
	cfg.DocsPath = "" // explorer is mounted above, served by Scalar instead of huma's default docs
	cfg.Transformers = append(cfg.Transformers, redactErrorValues)
	hapi := humachi.New(r, cfg)
	registerRoutes(hapi, deps)

	// The assistant endpoint (spec 2026-11-16 assistant access): MCP over
	// stateless Streamable HTTP. Its tools dispatch back into r itself, so
	// every call passes the same middleware, authorization and audit as a
	// REST request.
	// An organization assistant token uses /api/mcp; a personal one names
	// its organization as /api/mcp/o/{org}, or uses /api/mcp while it holds
	// exactly one live grant (spec 2026-11-17 personal tokens, rule 7).
	assistant := mcp.New(mcp.Deps{
		Router: r, Tokens: deps.Tokens, Assistant: deps.Assistant, Orgs: orgSlugs{deps.Tenancy},
		Access: mcpAccess{deps}, Version: managerVersion(deps), PublicURL: deps.PublicURL,
	})
	r.Handle("/api/mcp", assistant)
	r.Handle(mcp.OrgPathPattern, assistant)

	// Anything chi did not match above — every SPA route (/, /login,
	// /o/{org}/hosts, ...) and any /api/... path that is not a real
	// endpoint — falls through to the built frontend. web.Handler serves
	// the matching static asset when one exists, the SPA's index.html as a
	// history-mode fallback otherwise, and still 404s an unmatched
	// /api/... path itself rather than ever handing back the index page
	// for it (see its own doc comment).
	r.NotFound(web.Handler().ServeHTTP)
	return r, nil
}

// orgSlugs adapts the tenancy service to mcp.OrgSlugs.
type orgSlugs struct{ t *tenancy.Service }

func (o orgSlugs) SlugOf(ctx context.Context, orgID uuid.UUID) (string, error) {
	org, err := o.t.GetOrgByID(ctx, orgID)
	if err != nil {
		return "", err
	}
	return org.Slug, nil
}

// requireMFAForExplorer gates the API explorer page, the OpenAPI spec it
// loads and the per-type JSON Schemas behind an MFA-complete session. Both sit on the same origin as the
// session cookie and, unauthenticated, hand any caller the full shape of
// the API (and, via the explorer, a working request console) — so they use
// the same requireMFAComplete check as everything except the small set of
// routes (logout, me, totp/verify, passkey login) that must stay reachable
// to a half-authenticated session. This runs as router-level middleware,
// not a huma operation, because the OpenAPI spec route is registered
// internally by huma/humachi with no handler of ours to wrap.
func requireMFAForExplorer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/explorer" || strings.HasPrefix(r.URL.Path, "/api/openapi") ||
			strings.HasPrefix(r.URL.Path, "/api/schemas/") {
			if _, err := requireMFAComplete(r.Context()); err != nil {
				writeGateRefusal(w, err)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// registerRoutes is extended by later tasks (one register* call per resource).
func registerRoutes(hapi huma.API, deps Deps) {
	// Mounted before any operation is registered, on the root API rather
	// than any one group, so it wraps every operation added below — see
	// auditMiddleware's own doc comment for why that global mount is
	// deliberate and safe (it is a no-op for any operation withAudit did
	// not mark).
	hapi.UseMiddleware(auditMiddleware(deps))

	huma.Get(hapi, "/api/v1/health", func(ctx context.Context, _ *struct{}) (*healthOutput, error) {
		out := &healthOutput{}
		out.Body.Status = "ok"
		return out, nil
	})
	registerAuthRoutes(hapi, deps)
	registerMFARoutes(hapi, deps)
	registerPasskeyRoutes(hapi, deps)
	registerSessionRenewRoutes(hapi, deps)
	registerScreenLockRoutes(hapi, deps)  // /auth/lock… — the session lock and its PIN, cookie sessions only
	registerOrgRoutes(hapi, deps)         // POST/GET /orgs — no org in the path, no bearer credential accepted
	registerInviteAcceptRoute(hapi, deps) // POST /invites/accept — public, no principal at all
	registerAdminRoutes(hapi, deps)       // POST /admin/users/{id}/deactivate — instance admin, session only
	registerAdminUserRoutes(hapi, deps)   // /admin/users… — the Users and account pages, instance admin, session only
	registerAdminOrgRoutes(hapi, deps)    // GET /admin/orgs — every organization, instance admin, session only
	registerSetupRoutes(hapi, deps)       // GET /setup/status, POST /setup/complete — public
	// POST /hooks/jobs/{id}, POST /hooks/repos/{id} — public, unauthenticated
	// webhook receivers; the trigger's own secret is the credential (spec
	// §3). Registered on the root API like registerSetupRoutes above, not
	// under the org-scoped group: there is no org in either path, and no
	// session or bearer credential is accepted.
	registerHookRoutes(hapi, deps)
	registerSMTPSettingsRoutes(hapi, deps)   // /admin/settings/smtp... — instance admin, session only
	registerLocaleRoutes(hapi, deps)         // PUT /auth/me/locale — the user's own GUI language
	registerPreferencesRoutes(hapi, deps)    // /auth/me/preferences — the user's own appearance settings, session only
	registerPersonalTokenRoutes(hapi, deps)  // /auth/me/tokens — the user's own personal tokens, session only
	registerPasswordRoutes(hapi, deps)       // POST /auth/password — the user's own password
	registerPasswordResetRoutes(hapi, deps)  // /auth/password/{reset-available,forgot,reset} — public
	registerLocaleSettingsRoutes(hapi, deps) // /admin/settings/locale — instance admin, session only
	registerBackupRoutes(hapi, deps)         // /admin/backups... — instance admin, session only
	// /admin/repos/definitions... — instance admin, session only (spec
	// v0.2f decision 8: no org_id, no per-org access at all).
	registerSharedRepoRoutes(hapi, deps)
	// /admin/templates/definitions... — Task 6's own application of the
	// same posture, for job templates.
	registerSharedTemplateRoutes(hapi, deps)
	// /admin/checks/definitions... — Task 7's own application of the same
	// posture, for check definitions, the third and last kind.
	registerSharedCheckRoutes(hapi, deps)
	// GET /me/activity spans every org the caller belongs to, so — like
	// registerOrgRoutes above — it takes no org in its path and accepts no
	// bearer credential: a token is scoped to the one org it was minted
	// for, not to "everything its owner can see".
	registerMyActivityRoute(hapi, deps)
	// GET /check-plugins: the static plugin descriptors, for any signed-in
	// session — no org in its path and, like the two above, no bearer
	// credential.
	registerCheckPluginRoutes(hapi)

	// Everything below takes an org in its path and resolves it through
	// requireOrg, which consults auth.TokenScopeFromContext — this is the
	// "subtree that actually consults TokenScope" BearerMiddleware is
	// scoped to (see New's comment on why it is not mounted on the root
	// router). huma.Group.UseMiddleware wraps each operation registered on
	// the group individually, so this scoping holds per-operation without a
	// second chi router or a duplicate OpenAPI document.
	//
	// orgScopeMiddleware comes second, after the bearer credential is
	// resolved: it binds the request's database scope to the {org} in the
	// path, so row-level security confines every statement the handler
	// triggers to that organization (see its own doc comment). requireOrg
	// refuses any request that did not pass through it.
	orgScoped := huma.NewGroup(hapi)
	orgScoped.UseMiddleware(bearerAuthMiddleware(deps.Tokens))
	orgScoped.UseMiddleware(orgScopeMiddleware(deps))
	registerOrgScopedRoutes(orgScoped, deps)          // GET/PATCH /orgs/{org}
	registerMemberRoutes(orgScoped, deps)             // members + invites, all /orgs/{org}/...
	registerInviteRoutes(orgScoped, deps)             // open invitations: list, revoke
	registerTokenRoutes(orgScoped, deps)              // /orgs/{org}/tokens...
	registerPersonalTokenOrgRoutes(orgScoped, deps)   // /orgs/{org}/personal-tokens..., /orgs/{org}/settings/personal-tokens
	registerAuditRoutes(orgScoped, deps)              // GET /orgs/{org}/activity
	registerHostRoutes(orgScoped, deps)               // zones + hosts, all /orgs/{org}/...
	registerTagRoutes(orgScoped, deps)                // host tags + the tag selector, all /orgs/{org}/...
	registerCredentialRoutes(orgScoped, deps)         // /orgs/{org}/credentials...
	registerOrgSecretRoutes(orgScoped, deps)          // /orgs/{org}/secrets...
	registerSSHRoutes(orgScoped, deps)                // /orgs/{org}/bastions..., zone default, host SSH
	registerInventoryRoutes(orgScoped, deps)          // /orgs/{org}/inventory/groups...
	registerInventoryMemberRoutes(orgScoped, deps)    // memberships, host inventory, previews
	registerRepoRoutes(orgScoped, deps)               // /orgs/{org}/repos...
	registerRepoPlaybookRoutes(orgScoped, deps)       // /orgs/{org}/repos/{id}/playbooks
	registerPlaybookRoutes(orgScoped, deps)           // /orgs/{org}/playbooks...
	registerTemplateRoutes(orgScoped, deps)           // /orgs/{org}/job-templates...
	registerTemplateVarsRoutes(orgScoped, deps)       // /orgs/{org}/job-templates/{id}/variables
	registerJobRoutes(orgScoped, deps)                // /orgs/{org}/jobs... + the run action on job templates
	registerEnrollmentRoutes(orgScoped, deps)         // the enrollment previews of a run and a retry
	registerAdhocRoute(orgScoped, deps)               // POST /orgs/{org}/hosts/command — the ad-hoc shell command
	registerHostopsRoutes(orgScoped, deps)            // connection test + host key, all /orgs/{org}/hosts/{id}/...
	registerScheduleRoutes(orgScoped, deps)           // /orgs/{org}/schedules...
	registerTriggerRoutes(orgScoped, deps)            // /orgs/{org}/triggers...
	registerWorkerRoutes(orgScoped, deps)             // /orgs/{org}/workers..., /orgs/{org}/worker-enrollments...
	registerAgentRoutes(orgScoped, deps)              // /orgs/{org}/agents..., /orgs/{org}/hosts/{id}/agent
	registerCheckRoutes(orgScoped, deps)              // /orgs/{org}/checks...
	registerMonitoringGroupRoutes(orgScoped, deps)    // /orgs/{org}/monitoring-groups...
	registerHostCheckRoutes(orgScoped, deps)          // GET /orgs/{org}/hosts/{id}/checks, .../metrics
	registerRuleRoutes(orgScoped, deps)               // /orgs/{org}/rules..., GET /orgs/{org}/hosts/{id}/effective-rules
	registerItemRoutes(orgScoped, deps)               // /orgs/{org}/items, /orgs/{org}/item-decisions..., GET /orgs/{org}/hosts/{id}/items
	registerItemForgetRoute(orgScoped, deps)          // DELETE /orgs/{org}/hosts/{id}/items — forget one item
	registerFleetRoutes(orgScoped, deps)              // GET /orgs/{org}/fleet
	registerNotifyChannelRoutes(orgScoped, deps)      // /orgs/{org}/notification-channels...
	registerMaintenanceRoutes(orgScoped, deps)        // /orgs/{org}/maintenance-windows...
	registerScheduleNotifyRoutes(orgScoped, deps)     // /orgs/{org}/schedules/{id}/notifications
	registerNotifyDigestRoutes(orgScoped, deps)       // /orgs/{org}/notification-digest
	registerCalendarRoutes(orgScoped, deps)           // GET /orgs/{org}/calendar
	registerOrgSettingsRoutes(orgScoped, deps)        // /orgs/{org}/settings/jobs
	registerAssistantSettingsRoutes(orgScoped, deps)  // /orgs/{org}/settings/assistant
	registerRunRequestRoutes(orgScoped, deps)         // /orgs/{org}/run-requests..., .../run-requests, .../retry-requests
	registerScheduleRequestRoutes(orgScoped, deps)    // /orgs/{org}/schedule-requests, .../schedules/{id}/update-requests, .../resume-requests
	registerTriggerRequestRoutes(orgScoped, deps)     // /orgs/{org}/trigger-requests, .../triggers/{id}/update-requests, .../rotate-requests
	registerMonitoringSettingsRoutes(orgScoped, deps) // /orgs/{org}/settings/monitoring
	registerAlertRoutes(orgScoped, deps)              // /orgs/{org}/alerts..., the host view is List's own ?host= filter
	registerAlertRouteRoutes(orgScoped, deps)         // /orgs/{org}/alert-routes..., GET /orgs/{org}/hosts/{id}/alert-routes
	registerOncallRoutes(orgScoped, deps)             // /orgs/{org}/oncall-rotations..., GET .../shifts
}
