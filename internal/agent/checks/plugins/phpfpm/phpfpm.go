// Package phpfpm is the agent's PHP-FPM check plugin: it reads each listed
// pool's status page (?json, served by the web server in front of FPM) and
// reports, per pool, reachability, the processes, the listen queue and the
// request, max-children and slow-request rates, plus on item "" how many
// listed pools are down. Each entry of the urls parameter is one pool and
// its item, as written; an optional agent.yaml secret holds HTTP
// basic-authentication credentials, sent to every pool. It is a
// checks.StatefulPlugin (the rates need each pool's previous counters, so
// every check key collects with its own instance) and a
// checks.SecretPlugin. The requests are made by the shared webcheck
// helper, whose failure texts are fixed strings: no URL, header, body,
// user or password reaches an observation's text.
package phpfpm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck"
)

const (
	metricUp        = "phpfpm.up"
	metricPoolsDown = "phpfpm.pools_down"
	defaultURL      = "http://127.0.0.1/status"
	// maxPools is the descriptor's bound on urls; the manager refuses a
	// longer list, and the agent never fetches more than this at once.
	maxPools = 16
)

// poolMetrics are a pool's metrics besides up; all get the failure when
// its page cannot be fetched or parsed. The three rates are absent in the
// pool's first round and after a reset.
var poolMetrics = []string{
	"phpfpm.processes_active", "phpfpm.processes_idle", "phpfpm.processes_total", "phpfpm.processes_busy_pct",
	"phpfpm.listen_queue", "phpfpm.requests_per_s", "phpfpm.max_children_reached_per_s", "phpfpm.slow_requests_per_s",
}

// pool is one pool's rate state: the counters' baselines and what they
// belong to.
type pool struct {
	startTime, accepted, maxChildren, slow uint64
	requests, maxChildrenRate, slowRate    dbcheck.Counter
}

type plugin struct {
	now func() time.Time
	// pools is the rate state per entry (the URL string as written). One
	// instance per check key, collected from one goroutine only.
	pools map[string]*pool
}

// New returns the phpfpm plugin. It is a checks.StatefulPlugin prototype:
// the scheduler collects only the instances NewInstance hands out, one per
// check key.
func New() checks.Plugin { return &plugin{now: time.Now} }

// NewInstance returns a plugin with the same settings and no baseline.
func (p *plugin) NewInstance() checks.Plugin { return &plugin{now: p.now} }

func (*plugin) Name() string { return "phpfpm" }

// SecretErrorMetric is where the scheduler reports a missing secret: the
// pools are items of their own, so item "" carries pools_down, which every
// round reports and so overwrites once the secret is there.
func (*plugin) SecretErrorMetric() string { return metricPoolsDown }

// Collect is CollectWithSecret without a secret: no authentication.
func (p *plugin) Collect(ctx context.Context, params map[string]any) ([]checks.Observation, error) {
	return p.CollectWithSecret(ctx, params, nil)
}

// CollectWithSecret fetches every pool's page concurrently through
// webcheck.Round and reports each pool on its own item, then pools_down on
// item "". A caller context that ends returns nothing but its error (the
// Plugin contract) and moves no baseline; the round's own deadline is a
// pool that did not answer in time.
func (p *plugin) CollectWithSecret(ctx context.Context, params map[string]any, fields map[string]string) ([]checks.Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := poolURLs(params)
	if err != nil {
		return []checks.Observation{checks.ErrObservation("", metricPoolsDown, fmt.Errorf("phpfpm: %w", err))}, nil
	}
	pages := make([]string, len(entries))
	for i, e := range entries {
		pages[i] = webcheck.WithQueryKey(e, "json")
	}
	out, err := webcheck.Round(ctx, pages, maxPage, params, fields, parseStatus)
	if err != nil {
		return nil, err
	}
	return p.observe(entries, out), nil
}

// poolURLs is the list of pools: the urls entries, each once in the order
// first written, else the local default. An entry the check-results
// contract refuses as an item cannot pass the manager's validation and is
// left out; so is anything past maxPools.
func poolURLs(params map[string]any) ([]string, error) {
	list, _, err := checks.ParamStrings(params, "urls")
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return []string{defaultURL}, nil
	}
	seen := make(map[string]bool, len(list))
	var out []string
	for _, e := range list {
		if e == "" || seen[e] || !checks.ReportableItem(e) {
			continue
		}
		seen[e] = true
		out = append(out, e)
		if len(out) == maxPools {
			break
		}
	}
	return out, nil
}

// observe maps the round to observations and moves the rate baselines of
// the pools that answered; a pool that did not keeps its baseline, and the
// state of an entry no longer listed is dropped.
func (p *plugin) observe(entries []string, out []webcheck.Outcome[status]) []checks.Observation {
	listed := make(map[string]bool, len(entries))
	for _, e := range entries {
		listed[e] = true
	}
	for e := range p.pools {
		if !listed[e] {
			delete(p.pools, e)
		}
	}
	at := p.now()
	obs := []checks.Observation{{}}
	down := 0
	for i, e := range entries {
		if out[i].Reason != "" {
			down++
			obs = append(obs, webcheck.DownItem(e, "phpfpm", metricUp, poolMetrics, out[i].Reason)...)
			continue
		}
		obs = append(obs, p.observePool(e, out[i].Page, at)...)
	}
	obs[0] = checks.ValueObservation("", metricPoolsDown, float64(down))
	return obs
}

// observePool reports one pool that answered. A new start time (a restart
// or a reload) or any counter that fell restarts all three of its rates.
func (p *plugin) observePool(item string, s status, at time.Time) []checks.Observation {
	if p.pools == nil {
		p.pools = make(map[string]*pool)
	}
	st := p.pools[item]
	if st == nil || s.startTime != st.startTime || s.accepted < st.accepted || s.maxChildren < st.maxChildren || s.slow < st.slow {
		st = &pool{}
		p.pools[item] = st
	}
	st.startTime, st.accepted, st.maxChildren, st.slow = s.startTime, s.accepted, s.maxChildren, s.slow

	obs := []checks.Observation{
		checks.ValueObservation(item, metricUp, 1),
		checks.ValueObservation(item, "phpfpm.processes_active", float64(s.active)),
		checks.ValueObservation(item, "phpfpm.processes_idle", float64(s.idle)),
		checks.ValueObservation(item, "phpfpm.processes_total", float64(s.total)),
	}
	if s.total == 0 {
		obs = append(obs, checks.ErrObservation(item, "phpfpm.processes_busy_pct", errors.New("phpfpm: "+errFormat.Error())))
	} else {
		obs = append(obs, checks.ValueObservation(item, "phpfpm.processes_busy_pct", float64(s.active)/float64(s.total)*100))
	}
	obs = append(obs, checks.ValueObservation(item, "phpfpm.listen_queue", float64(s.listenQueue)))
	for _, r := range []struct {
		metric string
		c      *dbcheck.Counter
		v      uint64
	}{
		{"phpfpm.requests_per_s", &st.requests, s.accepted},
		{"phpfpm.max_children_reached_per_s", &st.maxChildrenRate, s.maxChildren},
		{"phpfpm.slow_requests_per_s", &st.slowRate, s.slow},
	} {
		if rate, ok := r.c.Rate(at, r.v); ok {
			obs = append(obs, checks.ValueObservation(item, r.metric, rate))
		}
	}
	return obs
}

var (
	_ checks.StatefulPlugin = (*plugin)(nil)
	_ checks.SecretPlugin   = (*plugin)(nil)
)
