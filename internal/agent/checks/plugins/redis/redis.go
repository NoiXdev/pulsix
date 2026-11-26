// Package redis is the agent's Redis check plugin (Redis 5 and later, and
// Valkey or any server that speaks the protocol): reachability, memory
// against its limit, clients, the replication role, link and lag, and the
// eviction rate, read from INFO over one connection per round. It is a
// checks.SecretPlugin: where the server is and how to log in come from an
// optional secret in agent.yaml (config.go), never from the manager; with no
// secret it reads 127.0.0.1:6379 without logging in. It is also a
// checks.StatefulPlugin: the eviction rate needs the previous counter, so
// every check key collects with its own instance. The wire protocol is a
// minimal RESP2 client of its own (resp.go), with every read bounded.
// Errors are classified into fixed texts (classify.go) and never carry a
// server reply, so no credential, user or host reaches an observation.
package redis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
)

const metricUp = "redis.up"

// hostMetrics are the metrics besides up. All but the eviction rate are
// reported on every successful round, as a value or as an error when INFO
// lacks what they need, so one odd field never hides the others; the
// eviction rate is absent on the first round and after a reset. All get the
// failure on a failed round.
var hostMetrics = []string{
	"redis.memory_used_bytes", "redis.memory_max_bytes", "redis.memory_pct", "redis.clients",
	"redis.role", "redis.replication_link", "redis.replication_lag_seconds", "redis.evicted_keys_per_s",
}

type plugin struct {
	// timeout overrides dbcheck.CollectTimeout; zero means the default.
	timeout time.Duration
	now     func() time.Time
	// defaultAddr overrides the address used without a secret, which is
	// 127.0.0.1:6379 from the fields' defaults (tests).
	defaultAddr string
	// evicted turns evicted_keys into a rate. One instance per check key,
	// and the scheduler collects an instance from one goroutine only.
	evicted dbcheck.Counter
	// uptime is the previous round's; a lower one is a restart.
	uptime uint64
}

// New returns the redis plugin. It is a checks.StatefulPlugin prototype: the
// scheduler collects only the instances NewInstance hands out, one per check
// key.
func New() checks.Plugin { return &plugin{now: time.Now} }

// NewInstance returns a plugin with the same settings and no baseline.
func (p *plugin) NewInstance() checks.Plugin {
	return &plugin{timeout: p.timeout, now: p.now, defaultAddr: p.defaultAddr}
}

func (*plugin) Name() string { return "redis" }

// SecretErrorMetric is where the scheduler reports a missing secret.
func (*plugin) SecretErrorMetric() string { return metricUp }

// Collect is CollectWithSecret without a secret: the local default server.
func (p *plugin) Collect(ctx context.Context, params map[string]any) ([]checks.Observation, error) {
	return p.CollectWithSecret(ctx, params, nil)
}

func errObs(err error) []checks.Observation {
	return []checks.Observation{checks.ErrObservation("", metricUp, err)}
}

// CollectWithSecret connects, reads and reports. A caller context that ends
// returns nothing but its error (the Plugin contract); the plugin's own
// deadline is a server that did not answer in time.
func (p *plugin) CollectWithSecret(ctx context.Context, _ map[string]any, fields map[string]string) ([]checks.Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, err := parseSettings(fields)
	if err != nil {
		return errObs(fmt.Errorf("redis: %w", err)), nil
	}
	if fields == nil && p.defaultAddr != "" {
		s.addr = p.defaultAddr
	}
	if s, err = s.withTLS(); err != nil {
		return errObs(fmt.Errorf("redis: %w", err)), nil
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
		return dbcheck.Down("redis", metricUp, hostMetrics, reason), nil
	}

	c, err := dial(cctx, s)
	if err != nil {
		return down(err)
	}
	defer c.Close()
	if s.password != "" {
		if err := c.auth(s.user, s.password); err != nil {
			return down(err)
		}
	}
	kv, err := c.info()
	if err != nil {
		return down(err)
	}
	if perr := ctx.Err(); perr != nil {
		return nil, perr
	}
	return p.observe(kv), nil
}

// uintField reads a non-negative integer INFO field.
func uintField(kv map[string]string, name string) (uint64, bool) {
	n, err := strconv.ParseUint(kv[name], 10, 64)
	return n, err == nil
}

func notReported(field string) error { return errors.New("redis: " + field + " is not reported") }

// observe maps INFO to observations and moves the eviction baseline.
func (p *plugin) observe(kv map[string]string) []checks.Observation {
	obs := make([]checks.Observation, 0, 9)
	obs = append(obs, checks.ValueObservation("", metricUp, 1))
	obs = append(obs, p.memory(kv)...)
	if n, ok := uintField(kv, "connected_clients"); ok {
		obs = append(obs, checks.ValueObservation("", "redis.clients", float64(n)))
	} else {
		obs = append(obs, checks.ErrObservation("", "redis.clients", notReported("connected_clients")))
	}
	obs = append(obs, replication(kv)...)
	obs = append(obs, p.evictions(kv))
	return dropNil(obs)
}

// dropNil removes the zero observations evictions returns when there is no
// rate yet.
func dropNil(obs []checks.Observation) []checks.Observation {
	out := obs[:0]
	for _, o := range obs {
		if o.Metric != "" {
			out = append(out, o)
		}
	}
	return out
}

// memory reports used, the limit and the percentage. The limit is
// maxmemory, or the machine's memory when maxmemory is 0 (unlimited).
func (p *plugin) memory(kv map[string]string) []checks.Observation {
	used, haveUsed := uintField(kv, "used_memory")
	limit, haveLimit := uintField(kv, "maxmemory")
	if !haveLimit || limit == 0 {
		limit, haveLimit = uintField(kv, "total_system_memory")
		haveLimit = haveLimit && limit > 0
	}
	var obs []checks.Observation
	if haveUsed {
		obs = append(obs, checks.ValueObservation("", "redis.memory_used_bytes", float64(used)))
	} else {
		obs = append(obs, checks.ErrObservation("", "redis.memory_used_bytes", notReported("used_memory")))
	}
	if haveLimit {
		obs = append(obs, checks.ValueObservation("", "redis.memory_max_bytes", float64(limit)))
	} else {
		obs = append(obs, checks.ErrObservation("", "redis.memory_max_bytes", errMemoryLimit))
	}
	switch {
	case !haveUsed:
		obs = append(obs, checks.ErrObservation("", "redis.memory_pct", notReported("used_memory")))
	case !haveLimit:
		obs = append(obs, checks.ErrObservation("", "redis.memory_pct", errMemoryLimit))
	default:
		obs = append(obs, checks.ValueObservation("", "redis.memory_pct", float64(used)/float64(limit)*100))
	}
	return obs
}

var errMemoryLimit = errors.New("redis: memory limit unknown")

// replication reports the role, the link and the lag. A primary has no
// link and no lag; a replica's link is up only when INFO says so, and its
// lag is the seconds since it last heard from the primary, which only means
// something while the link is up.
func replication(kv map[string]string) []checks.Observation {
	var role string
	switch kv["role"] {
	case "master":
		role = "primary"
	case "slave":
		role = "replica"
	default:
		err := errors.New("redis: role is not primary or replica")
		return []checks.Observation{
			checks.ErrObservation("", "redis.role", err),
			checks.ErrObservation("", "redis.replication_link", err),
			checks.ErrObservation("", "redis.replication_lag_seconds", err),
		}
	}
	obs := []checks.Observation{checks.LabelObservation("", "redis.role", role)}
	if role == "primary" {
		return append(obs,
			checks.LabelObservation("", "redis.replication_link", "none"),
			checks.ValueObservation("", "redis.replication_lag_seconds", 0))
	}
	if kv["master_link_status"] != "up" {
		return append(obs,
			checks.LabelObservation("", "redis.replication_link", "down"),
			checks.ErrObservation("", "redis.replication_lag_seconds", errors.New("redis: replication link is down")))
	}
	obs = append(obs, checks.LabelObservation("", "redis.replication_link", "up"))
	ago, err := strconv.ParseInt(kv["master_last_io_seconds_ago"], 10, 64)
	if err != nil || ago < 0 {
		return append(obs, checks.ErrObservation("", "redis.replication_lag_seconds", errors.New("redis: replication lag is unknown")))
	}
	return append(obs, checks.ValueObservation("", "redis.replication_lag_seconds", float64(ago)))
}

// evictions turns evicted_keys into a rate. A fall in uptime is a restart:
// the counter began again, possibly past its old value, so the baseline
// restarts. It returns the zero observation when there is no rate yet.
func (p *plugin) evictions(kv map[string]string) checks.Observation {
	if up, ok := uintField(kv, "uptime_in_seconds"); ok {
		if up < p.uptime {
			p.evicted = dbcheck.Counter{}
		}
		p.uptime = up
	}
	n, ok := uintField(kv, "evicted_keys")
	if !ok {
		return checks.ErrObservation("", "redis.evicted_keys_per_s", notReported("evicted_keys"))
	}
	if rate, ok := p.evicted.Rate(p.now(), n); ok {
		return checks.ValueObservation("", "redis.evicted_keys_per_s", rate)
	}
	return checks.Observation{}
}

var (
	_ checks.StatefulPlugin = (*plugin)(nil)
	_ checks.SecretPlugin   = (*plugin)(nil)
)
