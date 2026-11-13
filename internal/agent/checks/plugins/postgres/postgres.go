// Package postgres is the agent's PostgreSQL check plugin: reachability,
// connections against the maximum, replication lag, the oldest open
// transaction and database sizes, read with one connection per round. It is
// a checks.SecretPlugin: where the server is and how to log in come from a
// secret in agent.yaml (config.go), never from the manager. Errors are
// classified into fixed texts (classify.go) and never carry a driver
// message, so no credential, user or host reaches an observation.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
)

const (
	metricUp = "pg.up"
	// metricUnreported counts the databases a round leaves out: those beyond
	// the cap and those whose name the item contract refuses. 0 when none.
	metricUnreported = "pg.databases_unreported"

	// maxDatabases is how many databases one round reports.
	maxDatabases = 256
	// databaseRowLimit bounds the rows read: the cap, and some more to name
	// what was left out. A server with thousands costs nothing more.
	databaseRowLimit = maxDatabases + 64
	// maxNamedSkipped is how many left-out databases the advisory error names.
	maxNamedSkipped = 10
)

// hostMetrics are the item-"" metrics besides up: all of them are reported
// on every successful round (a standby promoted keeps reporting its lag as
// 0; the unreported count is 0 when nothing is left out) and all get the
// failure on a failed one.
var hostMetrics = []string{
	"pg.connections", "pg.max_connections", "pg.connections_pct", "pg.in_recovery",
	"pg.replication_lag_seconds", "pg.replication_lag_bytes", "pg.oldest_xact_seconds",
	metricUnreported,
}

type plugin struct {
	connect func(ctx context.Context, s connSettings) (session, error)
	// timeout overrides dbcheck.CollectTimeout; zero means the default.
	timeout time.Duration
}

// New returns the postgres plugin wired to the pgx driver.
func New() checks.Plugin { return plugin{connect: connect} }

func (plugin) Name() string { return "postgres" }

// SecretErrorMetric is where the scheduler reports a missing secret.
func (plugin) SecretErrorMetric() string { return metricUp }

// Collect is CollectWithSecret without a secret, which postgres cannot run
// without. The manager requires the parameter, so only a manager bug gets
// here.
func (p plugin) Collect(ctx context.Context, params map[string]any) ([]checks.Observation, error) {
	return p.CollectWithSecret(ctx, params, nil)
}

func errObs(err error) []checks.Observation {
	return []checks.Observation{checks.ErrObservation("", metricUp, err)}
}

// CollectWithSecret connects, reads and reports. A caller context that
// ends returns nothing but its error (the Plugin contract); the plugin's
// own deadline is a server that did not answer in time.
func (p plugin) CollectWithSecret(ctx context.Context, params map[string]any, fields map[string]string) ([]checks.Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if fields == nil {
		return errObs(errors.New("postgres: no secret given")), nil
	}
	settings, err := parseSettings(fields)
	if err != nil {
		return errObs(fmt.Errorf("postgres: %w", err)), nil
	}
	if err := checkRootCert(settings); err != nil {
		if errors.Is(err, errNotCAFile) {
			return errObs(errors.New("postgres: secret field `sslrootcert` is not a CA file")), nil
		}
		return errObs(errors.New("postgres: secret field `sslrootcert` cannot be read")), nil
	}
	filter, _, err := checks.ParamStrings(params, "databases")
	if err != nil {
		return errObs(fmt.Errorf("postgres: %w", err)), nil
	}

	timeout := p.timeout
	if timeout <= 0 {
		timeout = dbcheck.CollectTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	down := func(err error) ([]checks.Observation, error) {
		if perr := ctx.Err(); perr != nil {
			return nil, perr
		}
		reason := classify(err)
		if cctx.Err() != nil {
			reason = "timed out"
		}
		return dbcheck.Down("postgres", metricUp, hostMetrics, reason), nil
	}

	sess, err := p.connect(cctx, settings)
	if err != nil {
		return down(err)
	}
	defer sess.Close()

	stats, err := sess.Server(cctx)
	if err != nil {
		return down(err)
	}
	dbs, total, err := sess.Databases(cctx, filter, databaseRowLimit)
	if err != nil {
		return down(err)
	}
	if perr := ctx.Err(); perr != nil {
		return nil, perr
	}
	obs, advisory := observe(stats, dbs, total, filter)
	return obs, advisory
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// observe maps the rows to observations. total is how many databases the
// query matches in all, which may exceed len(dbs).
func observe(st serverStats, dbs []database, total int, filter []string) ([]checks.Observation, error) {
	obs := make([]checks.Observation, 0, 9+len(dbs))
	errNoSessions := errors.New("postgres: no privilege to see other sessions")
	obs = append(obs, checks.ValueObservation("", metricUp, 1))
	if st.LimitedView {
		obs = append(obs, checks.ErrObservation("", "pg.connections", errNoSessions))
	} else {
		obs = append(obs, checks.ValueObservation("", "pg.connections", float64(st.Connections)))
	}
	obs = append(obs, checks.ValueObservation("", "pg.max_connections", float64(st.MaxConnections)))
	switch {
	case st.LimitedView:
		obs = append(obs, checks.ErrObservation("", "pg.connections_pct", errNoSessions))
	case st.MaxConnections > 0:
		obs = append(obs, checks.ValueObservation("", "pg.connections_pct", float64(st.Connections)/float64(st.MaxConnections)*100))
	default:
		obs = append(obs, checks.ErrObservation("", "pg.connections_pct", errors.New("postgres: max_connections is not positive")))
	}
	obs = append(obs,
		checks.ValueObservation("", "pg.in_recovery", b2f(st.InRecovery)),
		checks.ValueObservation("", "pg.replication_lag_seconds", st.LagSeconds),
		checks.ValueObservation("", "pg.replication_lag_bytes", st.LagBytes),
	)
	if st.LimitedView {
		obs = append(obs, checks.ErrObservation("", "pg.oldest_xact_seconds", errNoSessions))
	} else {
		obs = append(obs, checks.ValueObservation("", "pg.oldest_xact_seconds", st.OldestXactSeconds))
	}

	var errs []error
	seen := make(map[string]struct{}, len(dbs))
	var left []string
	reported := 0
	for _, d := range dbs {
		if !checks.ReportableItem(d.Name) {
			errs = append(errs, fmt.Errorf("postgres: database %q cannot be reported: name longer than %d bytes, not valid UTF-8 or carrying a control character", d.Name, checks.MaxItemLen))
			continue
		}
		seen[d.Name] = struct{}{}
		if reported >= maxDatabases {
			left = append(left, d.Name)
			continue
		}
		reported++
		if d.Size == nil {
			obs = append(obs, checks.ErrObservation(d.Name, "pg.database_size_bytes", errors.New("postgres: no privilege to read the size")))
			continue
		}
		obs = append(obs, checks.ValueObservation(d.Name, "pg.database_size_bytes", float64(*d.Size)))
	}
	// A listed database the server does not have.
	for _, name := range filter {
		if _, ok := seen[name]; !ok && checks.ReportableItem(name) {
			obs = append(obs, checks.ErrObservation(name, "pg.database_size_bytes", errors.New("postgres: database does not exist")))
			seen[name] = struct{}{}
		}
	}
	if len(left) > 0 {
		named := left
		more := ""
		if len(named) > maxNamedSkipped {
			named, more = named[:maxNamedSkipped], ", ..."
		}
		quoted := make([]string, len(named))
		for i, n := range named {
			quoted[i] = fmt.Sprintf("%q", n)
		}
		errs = append(errs, fmt.Errorf("postgres: only %d databases are reported; left out: %s%s", maxDatabases, strings.Join(quoted, ", "), more))
	}
	// Exact however many rows the query matched beyond its read window:
	// what the server has minus what this round reported (refused names and
	// the ones beyond the cap are both in the difference).
	obs = append(obs, checks.ValueObservation("", metricUnreported, float64(max(total-reported, 0))))
	return obs, errors.Join(errs...)
}
