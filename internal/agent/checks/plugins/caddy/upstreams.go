package caddy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck"
)

// Upstream health (backlog 3.11b): with the upstreams_url parameter the
// plugin also reads the admin API's /reverse_proxy/upstreams list and
// reports each upstream as an item, its health joined from the metrics
// page's caddy_reverse_proxy_upstreams_healthy gauge by address. Only the
// numbers and the addresses (as items) are read from the pages.
const (
	metricAdminUp     = "caddy.admin_up"
	metricUnhealthy   = "caddy.upstreams_unhealthy"
	metricUnreported  = "caddy.upstreams_unreported"
	metricHealthy     = "caddy.upstream_healthy"
	upstreamPrefix    = "upstream:"
	famHealthy        = "caddy_reverse_proxy_upstreams_healthy"
	reasonNotReported = "not reported by the server"

	// maxUpstreams bounds the items one round reports, first by address;
	// the rest count in caddy.upstreams_unreported.
	maxUpstreams = 64
)

// upstreamMetrics are an upstream item's metrics; all get the admin API's
// failure on an item the instance knows.
var upstreamMetrics = []string{metricHealthy, "caddy.upstream_requests", "caddy.upstream_fails"}

// upstreamStatus is one entry of the admin API's list: requests in flight
// to the upstream and the failures it currently counts (within
// fail_duration). address is required.
type upstreamStatus struct {
	Address     *string `json:"address"`
	NumRequests int64   `json:"num_requests"`
	Fails       int64   `json:"fails"`
}

// parseUpstreams reads the admin API's list: a JSON array of objects, each
// with a non-empty address. Anything else is webcheck.ErrFormat, never the
// decoder's text.
func parseUpstreams(body []byte) ([]upstreamStatus, error) {
	if t := bytes.TrimLeft(body, " \t\r\n"); len(t) == 0 || t[0] != '[' {
		return nil, webcheck.ErrFormat
	}
	var list []upstreamStatus
	if json.Unmarshal(body, &list) != nil {
		return nil, webcheck.ErrFormat
	}
	for _, u := range list {
		if u.Address == nil || *u.Address == "" {
			return nil, webcheck.ErrFormat
		}
	}
	return list, nil
}

// parseHealthy reads the health gauge from a metrics page parseMetrics
// accepted: upstream address to healthy, the first series of an address
// winning. A series whose labels do not parse, that has no upstream label
// or whose value is not 0 or 1 is ignored: the gauge never fails a page.
func parseHealthy(body []byte) map[string]bool {
	out := map[string]bool{}
	rest := string(body)
	for rest != "" {
		line, tail, _ := strings.Cut(rest, "\n")
		rest = tail
		line = strings.TrimSpace(line)
		after, ok := strings.CutPrefix(line, famHealthy)
		if !ok || !strings.HasPrefix(after, "{") || len(line) > maxLine {
			continue
		}
		var addr string
		var have bool
		after, err := parseLabelSet(after, func(label, val string) {
			if label == "upstream" {
				addr, have = val, true
			}
		})
		if err != nil || !have {
			continue
		}
		v, err := parseValue(after)
		if _, seen := out[addr]; err != nil || seen || (v != 0 && v != 1) {
			continue
		}
		out[addr] = v == 1
	}
	return out
}

// collectWithUpstreams fetches the metrics page and the upstream list
// concurrently in one webcheck.Round, under its caller-context contract.
// The two fail apart: the metrics page down is caddy.up 0 as ever (and the
// health its reason), the list down is caddy.admin_up 0.
func (p *plugin) collectWithUpstreams(ctx context.Context, url, upstreamsURL string, params map[string]any, fields map[string]string) ([]checks.Observation, error) {
	out, err := webcheck.Round(ctx, []string{url, upstreamsURL}, 0, params, fields,
		func(body []byte) ([]byte, error) { return body, nil })
	if err != nil {
		return nil, err
	}
	var obs []checks.Observation
	var healthy map[string]bool
	healthReason := out[0].Reason
	if healthReason == "" {
		if m, err := parseMetrics(out[0].Page); err != nil {
			healthReason = err.Error()
		} else {
			obs = p.observe(url, m)
			healthy = parseHealthy(out[0].Page)
		}
	}
	if healthReason != "" {
		obs = dbcheck.Down(webPage.Name, webPage.Up, webPage.Others, healthReason)
	}
	reason := out[1].Reason
	var ups []upstreamStatus
	if reason == "" {
		if ups, err = parseUpstreams(out[1].Page); err != nil {
			reason = err.Error()
		}
	}
	if reason != "" {
		return append(obs, p.adminDown(reason)...), nil
	}
	return append(obs, p.observeUpstreams(ups, healthy, healthReason)...), nil
}

// observeUpstreams reports the list: admin_up, the upstreams within the
// cap (each address once, the first entry winning) and the count of the
// rest, and how many reported upstreams read unhealthy. healthReason is
// the metrics page's failure, "" when it was read.
func (p *plugin) observeUpstreams(list []upstreamStatus, healthy map[string]bool, healthReason string) []checks.Observation {
	byAddr := make(map[string]upstreamStatus, len(list))
	var addrs []string
	for _, u := range list {
		if _, seen := byAddr[*u.Address]; !seen {
			byAddr[*u.Address] = u
			addrs = append(addrs, *u.Address)
		}
	}
	slices.Sort(addrs)
	obs := []checks.Observation{checks.ValueObservation("", metricAdminUp, 1), {}, {}}
	left, unhealthy, known := 0, 0, 0
	next := map[string]bool{}
	for _, addr := range addrs {
		item := upstreamPrefix + addr
		if len(next) == maxUpstreams || !checks.ReportableItem(item) {
			left++
			continue
		}
		next[item] = true
		u := byAddr[addr]
		obs = append(obs,
			checks.ValueObservation(item, "caddy.upstream_requests", float64(max(u.NumRequests, 0))),
			checks.ValueObservation(item, "caddy.upstream_fails", float64(max(u.Fails, 0))))
		h, ok := healthy[addr]
		switch {
		case healthReason != "":
			obs = append(obs, checks.ErrObservation(item, metricHealthy, errors.New("caddy: "+healthReason)))
		case !ok:
			obs = append(obs, checks.ErrObservation(item, metricHealthy, errors.New("caddy: "+reasonNotReported)))
		default:
			known++
			v := 0.0
			if h {
				v = 1
			} else {
				unhealthy++
			}
			obs = append(obs, checks.ValueObservation(item, metricHealthy, v))
		}
	}
	p.upstreams = next
	obs[1] = checks.ValueObservation("", metricUnreported, float64(left))
	switch {
	case healthReason != "":
		obs[2] = checks.ErrObservation("", metricUnhealthy, errors.New("caddy: "+healthReason))
	case known == 0 && len(next) > 0:
		obs[2] = checks.ErrObservation("", metricUnhealthy, errors.New("caddy: "+reasonNotReported))
	default:
		obs[2] = checks.ValueObservation("", metricUnhealthy, float64(unhealthy))
	}
	return obs
}

// adminDown is a failed list: admin_up 0 with the reason on its two
// siblings, and the reason on every metric of every upstream the instance
// reported in its last good round.
func (p *plugin) adminDown(reason string) []checks.Observation {
	obs := dbcheck.Down("caddy", metricAdminUp, []string{metricUnhealthy, metricUnreported}, reason)
	err := errors.New("caddy: " + reason)
	items := make([]string, 0, len(p.upstreams))
	for item := range p.upstreams {
		items = append(items, item)
	}
	slices.Sort(items)
	for _, item := range items {
		for _, m := range upstreamMetrics {
			obs = append(obs, checks.ErrObservation(item, m, err))
		}
	}
	return obs
}
