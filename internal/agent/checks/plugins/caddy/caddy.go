// Package caddy is the agent's Caddy check plugin: it reads Caddy's metrics
// in the Prometheus text format (the admin endpoint's /metrics or a metrics
// handler on a site) and reports reachability, the requests in flight and
// the request and 5xx rates. Where the page is comes from the url
// parameter; an optional agent.yaml secret holds HTTP basic-authentication
// credentials. It is a checks.StatefulPlugin (the rates need the previous
// counters, so every check key collects with its own instance) and a
// checks.SecretPlugin. The request is made by the shared webcheck helper,
// whose failure texts are fixed strings: no URL, header, body, user or
// password reaches an observation.
package caddy

import (
	"context"
	"errors"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck"
)

const (
	metricUp   = "caddy.up"
	defaultURL = "http://127.0.0.1:2019/metrics"

	reasonDisabled = "HTTP metrics are not enabled in Caddy"
)

// hostMetrics are the metrics besides up; all get the failure on a failed
// round. Each is an error observation when its family is not on the page,
// and the two rates are absent in the first round and after a reset.
var hostMetrics = []string{"caddy.requests_in_flight", "caddy.requests_per_s", "caddy.errors_5xx_per_s"}

// webPage is what webcheck.Collect needs to know of this plugin.
var webPage = webcheck.Page{Name: "caddy", Up: metricUp, Others: hostMetrics}

type plugin struct {
	now func() time.Time
	// url is the page the counters belong to; another one starts over. One
	// instance per check key, collected from one goroutine only.
	url               string
	requests, errs5xx dbcheck.Counter
}

// New returns the caddy plugin. It is a checks.StatefulPlugin prototype: the
// scheduler collects only the instances NewInstance hands out, one per check
// key.
func New() checks.Plugin { return &plugin{now: time.Now} }

// NewInstance returns a plugin with the same settings and no baseline.
func (p *plugin) NewInstance() checks.Plugin { return &plugin{now: p.now} }

func (*plugin) Name() string { return "caddy" }

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
	return webcheck.Collect(ctx, webPage, url, params, fields, parseMetrics,
		func(m metrics) []checks.Observation { return p.observe(url, m) })
}

// statusURL is the page to fetch: the url parameter, else the admin
// endpoint's metrics.
func statusURL(params map[string]any) string {
	return webcheck.URLParam(params, "url", defaultURL)
}

// observe maps the page to observations and moves the rate baselines. A
// family that is missing restarts its baseline, and a counter that falls (a
// restart, a reload dropping a handler) restarts itself in dbcheck.Counter.
func (p *plugin) observe(url string, m metrics) []checks.Observation {
	if url != p.url {
		p.url = url
		p.requests, p.errs5xx = dbcheck.Counter{}, dbcheck.Counter{}
	}
	disabled := func(metric string) checks.Observation {
		return checks.ErrObservation("", metric, errors.New("caddy: "+reasonDisabled))
	}
	obs := []checks.Observation{checks.ValueObservation("", metricUp, 1)}
	if m.haveInflight {
		obs = append(obs, checks.ValueObservation("", "caddy.requests_in_flight", m.inflight))
	} else {
		obs = append(obs, disabled("caddy.requests_in_flight"))
	}
	at := p.now()
	obs = append(obs, p.rate("caddy.requests_per_s", &p.requests, at, m.requests, m.haveRequests, disabled)...)
	obs = append(obs, p.rate("caddy.errors_5xx_per_s", &p.errs5xx, at, m.errs5xx, m.have5xx, disabled)...)
	return obs
}

func (*plugin) rate(metric string, c *dbcheck.Counter, at time.Time, v uint64, have bool,
	disabled func(string) checks.Observation) []checks.Observation {
	if !have {
		*c = dbcheck.Counter{}
		return []checks.Observation{disabled(metric)}
	}
	if rate, ok := c.Rate(at, v); ok {
		return []checks.Observation{checks.ValueObservation("", metric, rate)}
	}
	return nil
}

var (
	_ checks.StatefulPlugin = (*plugin)(nil)
	_ checks.SecretPlugin   = (*plugin)(nil)
)
