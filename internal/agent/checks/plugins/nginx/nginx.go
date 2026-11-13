// Package nginx is the agent's nginx check plugin: it reads the server's
// stub_status page (any nginx with the module) and reports reachability, the
// connection states and the request, accept and drop rates. Where the page
// is comes from the url parameter; an optional agent.yaml secret holds HTTP
// basic-authentication credentials. It is a checks.StatefulPlugin (the rates
// need the previous counters, so every check key collects with its own
// instance) and a checks.SecretPlugin. The request is made by the shared
// webcheck helper, whose failure texts are fixed strings: no URL, header,
// body, user or password reaches an observation.
package nginx

import (
	"context"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck"
)

const (
	metricUp   = "nginx.up"
	defaultURL = "http://127.0.0.1/nginx_status"
)

// hostMetrics are the metrics besides up; all get the failure on a failed
// round. The three rates are absent in the first round and after a reset.
var hostMetrics = []string{
	"nginx.connections_active", "nginx.connections_reading", "nginx.connections_writing", "nginx.connections_waiting",
	"nginx.requests_per_s", "nginx.connections_accepted_per_s", "nginx.connections_dropped_per_s",
}

// webPage is what webcheck.Collect needs to know of this plugin.
var webPage = webcheck.Page{Name: "nginx", Up: metricUp, Others: hostMetrics}

type plugin struct {
	now func() time.Time
	// url is the page the counters belong to; another one starts over. One
	// instance per check key, collected from one goroutine only.
	url                        string
	lastAccepts, lastRequests  uint64
	requests, accepts, dropped dbcheck.Counter
}

// New returns the nginx plugin. It is a checks.StatefulPlugin prototype: the
// scheduler collects only the instances NewInstance hands out, one per check
// key.
func New() checks.Plugin { return &plugin{now: time.Now} }

// NewInstance returns a plugin with the same settings and no baseline.
func (p *plugin) NewInstance() checks.Plugin { return &plugin{now: p.now} }

func (*plugin) Name() string { return "nginx" }

// SecretErrorMetric is where the scheduler reports a missing secret.
func (*plugin) SecretErrorMetric() string { return metricUp }

// Collect is CollectWithSecret without a secret: no authentication.
func (p *plugin) Collect(ctx context.Context, params map[string]any) ([]checks.Observation, error) {
	return p.CollectWithSecret(ctx, params, nil)
}

// CollectWithSecret fetches, parses and reports through webcheck.Collect: a
// caller context that ends returns nothing but its error (the Plugin
// contract); the plugin's own deadline is a server that did not answer in
// time.
func (p *plugin) CollectWithSecret(ctx context.Context, params map[string]any, fields map[string]string) ([]checks.Observation, error) {
	url := statusURL(params)
	return webcheck.Collect(ctx, webPage, url, params, fields, parseStatus,
		func(s status) []checks.Observation { return p.observe(url, s) })
}

// statusURL is the page to fetch: the url parameter, else the local default.
func statusURL(params map[string]any) string { return webcheck.URLParam(params, "url", defaultURL) }

// observe maps the page to observations and moves the rate baselines.
func (p *plugin) observe(url string, s status) []checks.Observation {
	// A restart can leave accepts - handled where it was, so any fall
	// restarts all three baselines rather than reporting a 0 rate.
	if url != p.url || s.accepts < p.lastAccepts || s.requests < p.lastRequests {
		p.url = url
		p.requests, p.accepts, p.dropped = dbcheck.Counter{}, dbcheck.Counter{}, dbcheck.Counter{}
	}
	p.lastAccepts, p.lastRequests = s.accepts, s.requests
	obs := []checks.Observation{
		checks.ValueObservation("", metricUp, 1),
		checks.ValueObservation("", "nginx.connections_active", float64(s.active)),
		checks.ValueObservation("", "nginx.connections_reading", float64(s.reading)),
		checks.ValueObservation("", "nginx.connections_writing", float64(s.writing)),
		checks.ValueObservation("", "nginx.connections_waiting", float64(s.waiting)),
	}
	at := p.now()
	for _, r := range []struct {
		metric string
		c      *dbcheck.Counter
		v      uint64
	}{
		{"nginx.requests_per_s", &p.requests, s.requests},
		{"nginx.connections_accepted_per_s", &p.accepts, s.accepts},
		{"nginx.connections_dropped_per_s", &p.dropped, s.dropped()},
	} {
		if rate, ok := r.c.Rate(at, r.v); ok {
			obs = append(obs, checks.ValueObservation("", r.metric, rate))
		}
	}
	return obs
}

var (
	_ checks.StatefulPlugin = (*plugin)(nil)
	_ checks.SecretPlugin   = (*plugin)(nil)
)
