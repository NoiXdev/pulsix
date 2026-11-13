// Package apache is the agent's Apache httpd check plugin: it reads the
// server's mod_status page in its machine-readable form (?auto) and reports
// reachability, the busy and idle workers and the request and byte rates.
// Where the page is comes from the url parameter; an optional agent.yaml
// secret holds HTTP basic-authentication credentials. It is a
// checks.StatefulPlugin (the rates need the previous counters, so every
// check key collects with its own instance) and a checks.SecretPlugin. The
// request is made by the shared webcheck helper, whose failure texts are
// fixed strings: no URL, header, body, user or password reaches an
// observation.
package apache

import (
	"context"
	"errors"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck"
)

const (
	metricUp   = "apache.up"
	defaultURL = "http://127.0.0.1/server-status?auto"

	reasonExtended = "not reported by the server (ExtendedStatus Off)"
)

// hostMetrics are the metrics besides up; all get the failure on a failed
// round. The two rates are absent in the first round and after a reset, and
// an error without ExtendedStatus.
var hostMetrics = []string{
	"apache.workers_busy", "apache.workers_idle", "apache.workers_busy_pct",
	"apache.requests_per_s", "apache.bytes_per_s",
}

// webPage is what webcheck.Collect needs to know of this plugin.
var webPage = webcheck.Page{Name: "apache", Up: metricUp, Others: hostMetrics}

type plugin struct {
	now func() time.Time
	// url is the page the counters belong to; another one starts over. One
	// instance per check key, collected from one goroutine only.
	url                  string
	lastAcc, lastKB      uint64
	haveLast             bool
	requests, kbytesRate dbcheck.Counter
}

// New returns the apache plugin. It is a checks.StatefulPlugin prototype:
// the scheduler collects only the instances NewInstance hands out, one per
// check key.
func New() checks.Plugin { return &plugin{now: time.Now} }

// NewInstance returns a plugin with the same settings and no baseline.
func (p *plugin) NewInstance() checks.Plugin { return &plugin{now: p.now} }

func (*plugin) Name() string { return "apache" }

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

// statusURL is the page to fetch: the url parameter, else the local
// default, with the query key auto added when it is missing.
func statusURL(params map[string]any) string {
	return webcheck.WithQueryKey(webcheck.URLParam(params, "url", defaultURL), "auto")
}

// observe maps the page to observations and moves the rate baselines.
func (p *plugin) observe(url string, s status) []checks.Observation {
	// A restart (a graceful one may keep or reset the counters) leaves
	// either counter lower; any fall, another page or a page without the
	// counters restarts both baselines.
	if url != p.url || !s.extended || (p.haveLast && (s.accesses < p.lastAcc || s.kbytes < p.lastKB)) {
		p.url = url
		p.requests, p.kbytesRate = dbcheck.Counter{}, dbcheck.Counter{}
		p.haveLast = false
	}
	obs := []checks.Observation{
		checks.ValueObservation("", metricUp, 1),
		checks.ValueObservation("", "apache.workers_busy", float64(s.busy)),
		checks.ValueObservation("", "apache.workers_idle", float64(s.idle)),
	}
	if total := s.busy + s.idle; total == 0 || total < s.busy {
		obs = append(obs, checks.ErrObservation("", "apache.workers_busy_pct", errors.New("apache: "+errFormat.Error())))
	} else {
		obs = append(obs, checks.ValueObservation("", "apache.workers_busy_pct", float64(s.busy)/float64(total)*100))
	}
	if !s.extended {
		for _, m := range []string{"apache.requests_per_s", "apache.bytes_per_s"} {
			obs = append(obs, checks.ErrObservation("", m, errors.New("apache: "+reasonExtended)))
		}
		return obs
	}
	p.lastAcc, p.lastKB, p.haveLast = s.accesses, s.kbytes, true
	at := p.now()
	if rate, ok := p.requests.Rate(at, s.accesses); ok {
		obs = append(obs, checks.ValueObservation("", "apache.requests_per_s", rate))
	}
	if rate, ok := p.kbytesRate.Rate(at, s.kbytes); ok {
		obs = append(obs, checks.ValueObservation("", "apache.bytes_per_s", rate*1024))
	}
	return obs
}

var (
	_ checks.StatefulPlugin = (*plugin)(nil)
	_ checks.SecretPlugin   = (*plugin)(nil)
)
