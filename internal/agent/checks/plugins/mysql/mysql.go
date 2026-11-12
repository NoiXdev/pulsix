// Package mysql is the agent's MySQL and MariaDB check plugin: reachability,
// connections against the maximum, replication state and lag, and the rate
// of slow queries, read with one connection per round. It is a
// checks.SecretPlugin: where the server is and how to log in come from a
// secret in agent.yaml (config.go), never from the manager. It is also a
// checks.StatefulPlugin: the slow-query rate needs the previous counter, so
// every check key collects with its own instance. Errors are classified into
// fixed texts (classify.go) and never carry a driver message, so no
// credential, user or host reaches an observation.
package mysql

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
)

const metricUp = "mysql.up"

// hostMetrics are the metrics besides up: connections, the maximum, the
// percentage, the replication state and lag are reported on every successful
// round; the slow-query rate on every one but the first (and the first after
// a counter reset). All get the failure on a failed round.
var hostMetrics = []string{
	"mysql.connections", "mysql.max_connections", "mysql.connections_pct",
	"mysql.replication_state", "mysql.replication_lag_seconds", "mysql.slow_queries_per_s",
}

// Replication states, in the order of how bad they are.
const (
	stateNone       = "none"
	stateRunning    = "running"
	stateConnecting = "connecting"
	stateStopped    = "stopped"
)

type plugin struct {
	connect func(ctx context.Context, s connSettings) (session, error)
	// timeout overrides dbcheck.CollectTimeout; zero means the default.
	timeout time.Duration
	// queryTimeout overrides dbcheck.QueryTimeout; zero means the default.
	queryTimeout time.Duration
	now          func() time.Time
	// slow turns Slow_queries into a rate. One instance per check key, and
	// the scheduler collects an instance from one goroutine only.
	slow dbcheck.Counter
	// uptime is the previous round's; a lower one is a restart.
	uptime uint64
}

// New returns the mysql plugin wired to the MySQL driver. It is a
// checks.StatefulPlugin prototype: the scheduler collects only the
// instances NewInstance hands out, one per check key.
func New() checks.Plugin { return &plugin{connect: connect, now: time.Now} }

// NewInstance returns a plugin with the same providers and no baseline.
func (p *plugin) NewInstance() checks.Plugin {
	return &plugin{connect: p.connect, timeout: p.timeout, queryTimeout: p.queryTimeout, now: p.now}
}

func (*plugin) Name() string { return "mysql" }

// SecretErrorMetric is where the scheduler reports a missing secret.
func (*plugin) SecretErrorMetric() string { return metricUp }

// Collect is CollectWithSecret without a secret, which mysql cannot run
// without. The manager requires the parameter, so only a manager bug gets
// here.
func (p *plugin) Collect(ctx context.Context, params map[string]any) ([]checks.Observation, error) {
	return p.CollectWithSecret(ctx, params, nil)
}

func errObs(err error) []checks.Observation {
	return []checks.Observation{checks.ErrObservation("", metricUp, err)}
}

// CollectWithSecret connects, reads and reports. A caller context that
// ends returns nothing but its error (the Plugin contract); the plugin's
// own deadline is a server that did not answer in time.
func (p *plugin) CollectWithSecret(ctx context.Context, _ map[string]any, fields map[string]string) ([]checks.Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if fields == nil {
		return errObs(errors.New("mysql: no secret given")), nil
	}
	settings, err := parseSettings(fields)
	if err != nil {
		return errObs(fmt.Errorf("mysql: %w", err)), nil
	}

	settings.queryTimeout = p.queryTimeout
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
		return dbcheck.Down("mysql", metricUp, hostMetrics, reason), nil
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
	if perr := ctx.Err(); perr != nil {
		return nil, perr
	}
	if stats.Uptime < p.uptime {
		// Restarted: the counter began again, possibly past its old value.
		p.slow = dbcheck.Counter{}
	}
	p.uptime = stats.Uptime
	rate, haveRate := p.slow.Rate(p.now(), stats.SlowQueries)
	return observe(stats, rate, haveRate), nil
}

// observe maps the server's answers to observations.
func observe(st serverStats, slowRate float64, haveRate bool) []checks.Observation {
	obs := make([]checks.Observation, 0, 7)
	obs = append(obs,
		checks.ValueObservation("", metricUp, 1),
		checks.ValueObservation("", "mysql.connections", float64(st.Connections)),
		checks.ValueObservation("", "mysql.max_connections", float64(st.MaxConnections)),
	)
	if st.MaxConnections > 0 {
		obs = append(obs, checks.ValueObservation("", "mysql.connections_pct", float64(st.Connections)/float64(st.MaxConnections)*100))
	} else {
		obs = append(obs, checks.ErrObservation("", "mysql.connections_pct", errors.New("mysql: max_connections is not positive")))
	}
	state, lag, lagErr := replication(st.Replicas)
	obs = append(obs, checks.LabelObservation("", "mysql.replication_state", state))
	if lagErr != "" {
		obs = append(obs, checks.ErrObservation("", "mysql.replication_lag_seconds", errors.New(lagErr)))
	} else {
		obs = append(obs, checks.ValueObservation("", "mysql.replication_lag_seconds", lag))
	}
	if haveRate {
		obs = append(obs, checks.ValueObservation("", "mysql.slow_queries_per_s", slowRate))
	}
	return obs
}

func threadsYes(s string) bool { return strings.EqualFold(s, "yes") }

// rank orders the states by how bad they are.
func rank(state string) int {
	switch state {
	case stateRunning:
		return 1
	case stateConnecting:
		return 2
	case stateStopped:
		return 3
	}
	return 0
}

// groupReplicationPrefix starts the names of MySQL Group Replication's
// internal channels, which are not replication from a source.
const groupReplicationPrefix = "group_replication_"

// replication reduces the channels to the worst state and the largest lag.
// lagErr is set instead of a lag when a channel reports none: a channel that
// is not running has nothing to measure; a running one with a NULL lag is
// unknown. An unknown lag is never reported as 0.
func replication(chs []replicaChannel) (state string, lag float64, lagErr string) {
	state = stateNone
	unknown := false
	for _, ch := range chs {
		if strings.HasPrefix(ch.Name, groupReplicationPrefix) {
			continue
		}
		if state == stateNone {
			state = stateRunning
		}
		s := stateStopped
		switch {
		case threadsYes(ch.IO) && threadsYes(ch.SQL):
			s = stateRunning
		case strings.EqualFold(ch.IO, "connecting") && threadsYes(ch.SQL):
			s = stateConnecting
		}
		if rank(s) > rank(state) {
			state = s
		}
		switch {
		case ch.Lag != nil:
			lag = max(lag, *ch.Lag)
		case s != stateRunning:
			lagErr = "mysql: replication is not running"
		case lagErr == "":
			unknown = true
		}
	}
	if lagErr == "" && unknown {
		lagErr = "mysql: replication lag is unknown"
	}
	return state, lag, lagErr
}

var (
	_ checks.StatefulPlugin = (*plugin)(nil)
	_ checks.SecretPlugin   = (*plugin)(nil)
)
