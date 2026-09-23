// Package api builds the HTTP handler for the Pulsix manager.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/calendar"
	"github.com/noixdev/pulsix/internal/clientip"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/mail"
	"github.com/noixdev/pulsix/internal/maintenance"
	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/setup"
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
	Passkeys *auth.Passkeys
	Tokens   *auth.Tokens
	Tenancy  *tenancy.Service
	Audit    *audit.Recorder
	// ClientIP resolves request source addresses behind
	// PULSIX_TRUSTED_PROXIES; required.
	ClientIP *clientip.Resolver
	Setup    *setup.Service
	Hosts    *hosts.Service
	// Credentials is the organization's encrypted secret store (SSH keys,
	// passwords, Git deploy keys and tokens); see registerCredentialRoutes.
	Credentials *credentials.Service
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
	// Playbooks stores repo and editor playbooks; see registerPlaybookRoutes.
	Playbooks *playbooks.Service
	// Templates is the job-template registry (a run's playbook, scope,
	// limit, extra vars and default credential); see
	// registerTemplateRoutes.
	Templates *templates.Service
	// Jobs is the org-scoped job queue; the connection-test trigger route
	// enqueues through it (registerHostopsRoutes).
	Jobs *jobs.Service
	// Schedules fires job templates on a timetable; see
	// registerScheduleRoutes.
	Schedules *schedules.Service
	// Triggers is the webhook trigger registry and receive pipeline. The
	// two public hook routes call it directly; see registerHookRoutes.
	// Its own org-scoped CRUD routes are registered by a later task.
	Triggers *triggers.Service
	// Workers is the worker registry (Workers page, enrollment tokens).
	Workers *workers.Service
	// Notify manages notification channels, their delivery log, schedule
	// notification rules and the weekly digest (spec 2026-09-18 §5, §7).
	Notify *notify.Service
	// Maintenance manages maintenance windows (spec 2026-09-18 §6).
	Maintenance *maintenance.Service
	// Calendar lays out schedule occurrences and maintenance windows
	// (spec 2026-09-18 §8).
	Calendar *calendar.Service
	// AuthRateLimit is how many failed authentication attempts one client
	// address may make per minute on each rate-limited auth endpoint
	// (login, TOTP verify, passkey login finish, invite accept) —
	// PULSIX_AUTH_RATE_LIMIT, wired by app.Build from config.Load, which
	// defaults it to config.DefaultAuthRateLimit. 0 disables the limiter
	// (for tests); production leaves the default or higher. See
	// withAuthRateLimit (auth_rate_limit.go).
	AuthRateLimit int
	// WorkerEndpoint is what remote workers dial; empty Addr disables enrollment.
	WorkerEndpoint WorkerEndpoint
	// Mailer is the concrete SMTP sender, not the narrower tenancy.Mailer
	// interface tenancy.Service is built with: only this package's own
	// POST /admin/settings/smtp/test handler needs it, to run a live
	// Test() against whatever settings are currently stored, so its own
	// error (not just tenancy.Service's Send) is what the wizard's SMTP
	// test surfaces.
	Mailer *mail.SMTP
	Secure bool // set Secure on cookies (true behind HTTPS)
	// Version is the manager's build version, reported on GET /healthz.
	// Left empty by every existing caller except app.Build (which sets it
	// from config.Config.Version, itself set by cmd/manager/main.go's
	// ldflags-stamped `version` variable) — New defaults an empty value to
	// "dev" in the /healthz handler below, so no caller needs to know
	// about this field just to keep building a working Deps.
	Version string
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
	if d.Tenancy == nil {
		missing = append(missing, "Tenancy")
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
	if d.Schedules == nil {
		missing = append(missing, "Schedules")
	}
	if d.Triggers == nil {
		missing = append(missing, "Triggers")
	}
	if d.Workers == nil {
		missing = append(missing, "Workers")
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
	r.Use(middleware.RequestID, middleware.Logger, middleware.Recoverer)
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

	healthzVersion := deps.Version
	if healthzVersion == "" {
		healthzVersion = "dev"
	}
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Status  string `json:"status"`
			Version string `json:"version"`
		}{Status: "ok", Version: healthzVersion})
	})

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
	cfg.DocsPath = "" // explorer is mounted above, served by Scalar instead of huma's default docs
	cfg.Transformers = append(cfg.Transformers, redactErrorValues)
	hapi := humachi.New(r, cfg)
	registerRoutes(hapi, deps)

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

// requireMFAForExplorer gates the API explorer page and the OpenAPI spec it
// loads behind an MFA-complete session. Both sit on the same origin as the
// session cookie and, unauthenticated, hand any caller the full shape of
// the API (and, via the explorer, a working request console) — so they use
// the same requireMFAComplete check as everything except the small set of
// routes (logout, me, totp/verify, passkey login) that must stay reachable
// to a half-authenticated session. This runs as router-level middleware,
// not a huma operation, because the OpenAPI spec route is registered
// internally by huma/humachi with no handler of ours to wrap.
func requireMFAForExplorer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/explorer" || strings.HasPrefix(r.URL.Path, "/api/openapi") {
			if _, err := requireMFAComplete(r.Context()); err != nil {
				status := http.StatusUnauthorized
				var se huma.StatusError
				if errors.As(err, &se) {
					status = se.GetStatus()
				}
				http.Error(w, "login required", status)
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
	registerOrgRoutes(hapi, deps)         // POST/GET /orgs — no org in the path, no bearer credential accepted
	registerInviteAcceptRoute(hapi, deps) // POST /invites/accept — public, no principal at all
	registerAdminRoutes(hapi, deps)       // POST /admin/users/{id}/deactivate — instance admin, session only
	registerSetupRoutes(hapi, deps)       // GET /setup/status, POST /setup/complete — public
	// POST /hooks/jobs/{id}, POST /hooks/repos/{id} — public, unauthenticated
	// webhook receivers; the trigger's own secret is the credential (spec
	// §3). Registered on the root API like registerSetupRoutes above, not
	// under the org-scoped group: there is no org in either path, and no
	// session or bearer credential is accepted.
	registerHookRoutes(hapi, deps)
	registerSMTPSettingsRoutes(hapi, deps) // /admin/settings/smtp... — instance admin, session only
	// GET /me/activity spans every org the caller belongs to, so — like
	// registerOrgRoutes above — it takes no org in its path and accepts no
	// bearer credential: a token is scoped to the one org it was minted
	// for, not to "everything its owner can see".
	registerMyActivityRoute(hapi, deps)

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
	registerOrgScopedRoutes(orgScoped, deps)       // GET/PATCH /orgs/{org}
	registerMemberRoutes(orgScoped, deps)          // members + invites, all /orgs/{org}/...
	registerTokenRoutes(orgScoped, deps)           // /orgs/{org}/tokens...
	registerAuditRoutes(orgScoped, deps)           // GET /orgs/{org}/activity
	registerHostRoutes(orgScoped, deps)            // zones + hosts, all /orgs/{org}/...
	registerTagRoutes(orgScoped, deps)             // host tags + the tag selector, all /orgs/{org}/...
	registerCredentialRoutes(orgScoped, deps)      // /orgs/{org}/credentials...
	registerSSHRoutes(orgScoped, deps)             // /orgs/{org}/bastions..., zone default, host SSH
	registerInventoryRoutes(orgScoped, deps)       // /orgs/{org}/inventory/groups...
	registerInventoryMemberRoutes(orgScoped, deps) // memberships, host inventory, previews
	registerRepoRoutes(orgScoped, deps)            // /orgs/{org}/repos...
	registerPlaybookRoutes(orgScoped, deps)        // /orgs/{org}/playbooks...
	registerTemplateRoutes(orgScoped, deps)        // /orgs/{org}/job-templates...
	registerJobRoutes(orgScoped, deps)             // /orgs/{org}/jobs... + the run action on job templates
	registerAdhocRoute(orgScoped, deps)            // POST /orgs/{org}/hosts/command — the ad-hoc shell command
	registerHostopsRoutes(orgScoped, deps)         // connection test + host key, all /orgs/{org}/hosts/{id}/...
	registerScheduleRoutes(orgScoped, deps)        // /orgs/{org}/schedules...
	registerTriggerRoutes(orgScoped, deps)         // /orgs/{org}/triggers...
	registerWorkerRoutes(orgScoped, deps)          // /orgs/{org}/workers..., /orgs/{org}/worker-enrollments...
	registerNotifyChannelRoutes(orgScoped, deps)   // /orgs/{org}/notification-channels...
	registerMaintenanceRoutes(orgScoped, deps)     // /orgs/{org}/maintenance-windows...
	registerScheduleNotifyRoutes(orgScoped, deps)  // /orgs/{org}/schedules/{id}/notifications
	registerNotifyDigestRoutes(orgScoped, deps)    // /orgs/{org}/notification-digest
	registerCalendarRoutes(orgScoped, deps)        // GET /orgs/{org}/calendar
	registerOrgSettingsRoutes(orgScoped, deps)     // /orgs/{org}/settings/jobs
}
