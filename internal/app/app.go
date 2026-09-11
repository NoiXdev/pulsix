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
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/noixdev/pulsix/internal/api"
	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/config"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/mail"
	"github.com/noixdev/pulsix/internal/setup"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// appRole is the least-privilege PostgreSQL role every request is served
// through — see assertCannotBypassRLS's own doc comment for why this
// matters and how it is verified, not merely assumed.
const appRole = "pulsix_app"

// App is a fully wired manager: an http.Handler plus what main.go needs to
// shut it down cleanly.
type App struct {
	Handler http.Handler
	db      *db.DB
}

// Close releases the application's database pool. It does not touch the
// migration pool Build opens: that one is already closed before Build
// returns, successfully or not — see Build's own comment on why.
func (a *App) Close() { a.db.Close() }

// Build constructs every dependency in order — database (running
// migrations, then reopening as the least-privilege role and verifying it
// really cannot bypass row-level security), the master-key box, sessions,
// every service, and finally the API handler — and returns a ready-to-serve
// App. It is the one place production wiring happens; cmd/manager/main.go
// only calls it.
func Build(ctx context.Context, cfg config.Config) (*App, error) {
	// Migrations need DDL privileges appRole does not have, so they run
	// through the plain superuser/owner connection named by
	// cfg.DatabaseURL. That connection is closed as soon as migrations are
	// done — nothing else in this function, or in the App it returns, ever
	// uses it — precisely so that a bug anywhere downstream can never
	// accidentally query through a connection RLS does not apply to.
	migrationDB, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("open migration database connection: %w", err)
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
	appDB, err := db.OpenAsRole(ctx, cfg.DatabaseURL, appRole)
	if err != nil {
		return nil, fmt.Errorf("open application database connection: %w", err)
	}
	if err := assertCannotBypassRLS(ctx, appDB); err != nil {
		appDB.Close()
		return nil, err
	}

	box := crypto.NewBox(cfg.MasterKey)
	q := gen.New(appDB.Pool())
	sessions := auth.NewSessions(q, time.Now)

	totp := auth.NewTOTP(auth.NewSQLTOTPStore(appDB.Pool()), box, "Pulsix", time.Now)
	passkeys, err := auth.NewPasskeys(auth.NewSQLPasskeyStore(q), auth.NewSQLStore(q), sessions, cfg.RPID(), "Pulsix", cfg.Origin(), time.Now)
	if err != nil {
		appDB.Close()
		return nil, fmt.Errorf("build passkey service: %w", err)
	}
	tokens := auth.NewTokens(auth.NewSQLTokenStore(q), time.Now)
	authSvc := auth.NewService(auth.NewSQLStore(q), sessions, auth.AnyEnabled(totp, passkeys))

	setupSvc := setup.NewService(setup.NewSQLStore(appDB.Pool()), sessions, box, time.Now)
	// mail.NewSMTP resolves its settings through setupSvc.LoadSMTP on every
	// call, so a settings change made through PUT /admin/settings/smtp
	// takes effect on the very next send — there is no separate mailer to
	// rebuild or process to restart.
	mailer := mail.NewSMTP(setupSvc.LoadSMTP)
	tenancySvc := tenancy.NewService(tenancy.NewSQLStore(q), mailer, cfg.PublicURL, time.Now)
	auditRec := audit.NewRecorder(audit.NewSQLStore(q), time.Now)
	hostsSvc := hosts.NewService(hosts.NewSQLStore(q, appDB.Pool()), time.Now)
	// Every organization gets its "default" zone the moment it is created,
	// through whichever of the two paths created it — POST /orgs
	// (tenancy.Service.CreateOrg) or the first-run wizard
	// (setup.Service.Complete) — both bound to the same
	// hostsSvc.EnsureDefaultZone here, so neither path can produce an
	// organization the other would have given a zone. See
	// tenancy.OrgCreatedHook's own doc comment for why this runs as a
	// sequential step after each service's own writes commit, rather than
	// inside either one's transaction.
	tenancySvc.SetOrgCreatedHook(hostsSvc.EnsureDefaultZone)
	setupSvc.SetOrgCreatedHook(hostsSvc.EnsureDefaultZone)

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

	handler, err := api.New(api.Deps{
		DB: appDB, Sessions: sessions, Auth: authSvc, TOTP: totp, Passkeys: passkeys, Tokens: tokens,
		Tenancy: tenancySvc, Audit: auditRec, Setup: setupSvc, Mailer: mailer, Hosts: hostsSvc, Secure: publicURLIsHTTPS(cfg.PublicURL),
		Version: cfg.Version,
	})
	if err != nil {
		appDB.Close()
		return nil, fmt.Errorf("build api handler: %w", err)
	}

	return &App{Handler: handler, db: appDB}, nil
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
