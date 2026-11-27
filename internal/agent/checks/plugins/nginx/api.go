package nginx

import (
	"bytes"
	"encoding/json"
	"errors"
	"hash/fnv"
	"math"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck"
)

// The nginx Plus API (backlog 3.11b): with the api_url parameter the plugin
// also reads <api_url>/http/server_zones and <api_url>/http/upstreams and
// reports each server zone and upstream as an item of its own. Only the
// numbers below are read from the pages; zone and upstream names become
// items, nothing else does.
const (
	metricAPIUp         = "nginx.api_up"
	metricAPIUnreported = "nginx.api_items_unreported"

	zoneItemPrefix     = "zone:"
	upstreamItemPrefix = "upstream:"

	// maxAPIZones and maxAPIUpstreams bound the items one round reports,
	// first by name; the rest count in nginx.api_items_unreported. 64 of
	// each keep a check far below the host's 4,096 tuples.
	maxAPIZones     = 64
	maxAPIUpstreams = 64
)

// zoneMetrics and upstreamMetrics are an item's metrics; all get the API's
// failure on an item the instance knows. The rates are absent in an item's
// first round and after a reset.
var (
	zoneRates = []string{
		"nginx.zone_requests_per_s", "nginx.zone_responses_4xx_per_s", "nginx.zone_responses_5xx_per_s",
		"nginx.zone_received_bytes_per_s", "nginx.zone_sent_bytes_per_s",
	}
	zoneMetrics   = append([]string{"nginx.zone_processing"}, zoneRates...)
	upstreamRates = []string{
		"nginx.upstream_requests_per_s", "nginx.upstream_responses_5xx_per_s", "nginx.upstream_fails_per_s",
	}
	upstreamMetrics = append([]string{
		"nginx.upstream_peers", "nginx.upstream_peers_up", "nginx.upstream_peers_unavailable", "nginx.upstream_active",
	}, upstreamRates...)
)

// responses are the counts of a zone's or a peer's responses by class.
type responses struct {
	R4xx uint64 `json:"4xx"`
	R5xx uint64 `json:"5xx"`
}

// zone is one entry of /http/server_zones. requests is required, so a JSON
// object that is not the API (an error document) does not read as a zone.
type zone struct {
	Processing uint64    `json:"processing"`
	Requests   *uint64   `json:"requests"`
	Responses  responses `json:"responses"`
	Received   uint64    `json:"received"`
	Sent       uint64    `json:"sent"`
}

// peer is one server of an upstream; state is required.
type peer struct {
	ID        uint64    `json:"id"`
	Server    string    `json:"server"`
	State     *string   `json:"state"`
	Active    uint64    `json:"active"`
	Requests  uint64    `json:"requests"`
	Responses responses `json:"responses"`
	Fails     uint64    `json:"fails"`
}

// upstream is one entry of /http/upstreams; peers is required.
type upstream struct {
	Peers *[]peer `json:"peers"`
}

// parseZones reads the server zones page: a JSON object of zone objects.
func parseZones(body []byte) (map[string]zone, error) {
	var zs map[string]zone
	if err := decodeObject(body, &zs); err != nil {
		return nil, err
	}
	for _, z := range zs {
		if z.Requests == nil {
			return nil, webcheck.ErrFormat
		}
	}
	return zs, nil
}

// parseUpstreams reads the upstreams page: a JSON object of upstream
// objects, each with its list of peers.
func parseUpstreams(body []byte) (map[string]upstream, error) {
	var us map[string]upstream
	if err := decodeObject(body, &us); err != nil {
		return nil, err
	}
	for _, u := range us {
		if u.Peers == nil {
			return nil, webcheck.ErrFormat
		}
		for _, p := range *u.Peers {
			if p.State == nil {
				return nil, webcheck.ErrFormat
			}
		}
	}
	return us, nil
}

// decodeObject decodes body into dst when it is a single JSON object;
// anything else (null, an array, HTML, trailing data, a negative or
// fractional count) is webcheck.ErrFormat, never the decoder's text.
func decodeObject(body []byte, dst any) error {
	if t := bytes.TrimLeft(body, " \t\r\n"); len(t) == 0 || t[0] != '{' {
		return webcheck.ErrFormat
	}
	if json.Unmarshal(body, dst) != nil {
		return webcheck.ErrFormat
	}
	return nil
}

// apiURLs are the two pages under the API base: its path joined with the
// page's, the query kept. A base that does not parse (the manager's
// validation keeps it from happening) is handed to the fetch as it is,
// which then fails with a fixed text.
func apiURLs(base string) (zones, upstreams string) {
	u, err := url.Parse(base)
	if err != nil {
		return base, base
	}
	return u.JoinPath("http", "server_zones").String(), u.JoinPath("http", "upstreams").String()
}

// zoneState and upstreamState are an item's rate baselines; peers
// identifies the upstream's peer set the sums belong to.
type zoneState struct {
	requests, r4xx, r5xx, received, sent dbcheck.Counter
}

type upstreamState struct {
	peers                 uint64
	requests, r5xx, fails dbcheck.Counter
}

// observeAPI reports the two parsed pages: api_up, the items within the
// caps and the count of the rest. It moves the baselines of the reported
// items and drops the state of every other one.
func (p *plugin) observeAPI(zones map[string]zone, ups map[string]upstream) []checks.Observation {
	at := p.now()
	obs := []checks.Observation{checks.ValueObservation("", metricAPIUp, 1), {}}
	zoneItems, zoneLeft := capItems(zoneItemPrefix, zones, maxAPIZones)
	upItems, upLeft := capItems(upstreamItemPrefix, ups, maxAPIUpstreams)
	obs[1] = checks.ValueObservation("", metricAPIUnreported, float64(zoneLeft+upLeft))

	nextZones := make(map[string]*zoneState, len(zoneItems))
	for _, name := range zoneItems {
		item := zoneItemPrefix + name
		st := p.zones[item]
		if st == nil {
			st = &zoneState{}
		}
		nextZones[item] = st
		obs = append(obs, observeZone(item, zones[name], st, at)...)
	}
	p.zones = nextZones

	nextUps := make(map[string]*upstreamState, len(upItems))
	for _, name := range upItems {
		item := upstreamItemPrefix + name
		st := p.upstreams[item]
		if st == nil {
			st = &upstreamState{}
		}
		nextUps[item] = st
		obs = append(obs, observeUpstream(item, *ups[name].Peers, st, at)...)
	}
	p.upstreams = nextUps
	return obs
}

// capItems is the names to report, in byte order: those whose item the
// check-results contract accepts, at most limit of them; left counts the
// rest.
func capItems[V any](prefix string, m map[string]V, limit int) (names []string, left int) {
	all := make([]string, 0, len(m))
	for name := range m {
		all = append(all, name)
	}
	slices.Sort(all)
	for _, name := range all {
		if len(names) == limit || !checks.ReportableItem(prefix+name) {
			left++
			continue
		}
		names = append(names, name)
	}
	return names, left
}

func observeZone(item string, z zone, st *zoneState, at time.Time) []checks.Observation {
	obs := []checks.Observation{checks.ValueObservation(item, "nginx.zone_processing", float64(z.Processing))}
	return append(obs, rates(item, at, []rate{
		{"nginx.zone_requests_per_s", &st.requests, *z.Requests},
		{"nginx.zone_responses_4xx_per_s", &st.r4xx, z.Responses.R4xx},
		{"nginx.zone_responses_5xx_per_s", &st.r5xx, z.Responses.R5xx},
		{"nginx.zone_received_bytes_per_s", &st.received, z.Received},
		{"nginx.zone_sent_bytes_per_s", &st.sent, z.Sent},
	})...)
}

// observeUpstream reports an upstream's peers by state and the sums of
// their counters. A peer set that changed restarts the rates: the sums
// jump when a peer comes or goes.
func observeUpstream(item string, peers []peer, st *upstreamState, at time.Time) []checks.Observation {
	var up, unavailable, active, requests, r5xx, fails uint64
	sig := fnv.New64a()
	for _, pr := range peers {
		switch *pr.State {
		case "up":
			up++
		case "unavail", "unhealthy":
			unavailable++
		}
		active = satAdd(active, pr.Active)
		requests = satAdd(requests, pr.Requests)
		r5xx = satAdd(r5xx, pr.Responses.R5xx)
		fails = satAdd(fails, pr.Fails)
		_, _ = sig.Write([]byte(strconv.FormatUint(pr.ID, 10) + "\x00" + pr.Server + "\x00"))
	}
	if s := sig.Sum64(); s != st.peers {
		st.peers = s
		st.requests, st.r5xx, st.fails = dbcheck.Counter{}, dbcheck.Counter{}, dbcheck.Counter{}
	}
	obs := []checks.Observation{
		checks.ValueObservation(item, "nginx.upstream_peers", float64(len(peers))),
		checks.ValueObservation(item, "nginx.upstream_peers_up", float64(up)),
		checks.ValueObservation(item, "nginx.upstream_peers_unavailable", float64(unavailable)),
		checks.ValueObservation(item, "nginx.upstream_active", float64(active)),
	}
	return append(obs, rates(item, at, []rate{
		{"nginx.upstream_requests_per_s", &st.requests, requests},
		{"nginx.upstream_responses_5xx_per_s", &st.r5xx, r5xx},
		{"nginx.upstream_fails_per_s", &st.fails, fails},
	})...)
}

type rate struct {
	metric string
	c      *dbcheck.Counter
	v      uint64
}

func rates(item string, at time.Time, rs []rate) []checks.Observation {
	var obs []checks.Observation
	for _, r := range rs {
		if v, ok := r.c.Rate(at, r.v); ok {
			obs = append(obs, checks.ValueObservation(item, r.metric, v))
		}
	}
	return obs
}

// apiDown is a failed API round: api_up 0 with the reason on its sibling,
// and the reason on every metric of every item the instance knows, whose
// baselines it keeps.
func (p *plugin) apiDown(reason string) []checks.Observation {
	obs := dbcheck.Down("nginx", metricAPIUp, []string{metricAPIUnreported}, reason)
	err := errors.New("nginx: " + reason)
	for _, k := range []struct {
		items   []string
		metrics []string
	}{
		{sortedKeys(p.zones), zoneMetrics},
		{sortedKeys(p.upstreams), upstreamMetrics},
	} {
		for _, item := range k.items {
			for _, m := range k.metrics {
				obs = append(obs, checks.ErrObservation(item, m, err))
			}
		}
	}
	return obs
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// satAdd adds without wrapping.
func satAdd(a, b uint64) uint64 {
	if a > math.MaxUint64-b {
		return math.MaxUint64
	}
	return a + b
}
