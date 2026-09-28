package api

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// The host page's monitoring section (v0.3b-1, spec "API and permissions"
// and "GUI"): which checks actually apply to this host and where each came
// from, the latest value or collection error per metric, and the bucketed
// series behind a graph.

// staleIntervals is how many of a check's own intervals may pass before its
// latest value is shown as stale rather than current (spec's GUI section:
// "an entry older than three intervals"). Three, not one, because a single
// late flush — a slow agent, a reconnect replaying its ring — is normal and
// must not paint a healthy fleet amber.
const staleIntervals = 3

// The ranges a graph may ask for, and how many buckets each is
// downsampled into server-side. The bucket counts are chosen so one point
// lands on a readable grid — a minute over an hour, ten minutes over a
// day, an hour over a week — rather than to fill a particular pixel width,
// which the browser does not report and the API must not guess.
var metricRanges = map[string]struct {
	window  time.Duration
	buckets int
}{
	"1h":  {time.Hour, 60},
	"24h": {24 * time.Hour, 144},
	"7d":  {7 * 24 * time.Hour, 168},
}

// metricRangeNames lists the accepted ranges for a refusal message, in the
// order an operator sees them in the GUI.
var metricRangeNames = []string{"1h", "24h", "7d"}

type hostCheckValueDTO struct {
	Item   string    `json:"item" doc:"The mount or unit this value is for; empty for a whole-host metric"`
	Metric string    `json:"metric"`
	Value  *float64  `json:"value,omitempty" doc:"Set for a numeric metric"`
	Label  string    `json:"label,omitempty" doc:"Set for a discrete metric, e.g. a service state"`
	Error  string    `json:"error,omitempty" doc:"Set when collection failed; value and label are then both absent — check_current stores an error row with neither"`
	At     time.Time `json:"at" doc:"The agent's own timestamp for this reading"`
	Stale  bool      `json:"stale" doc:"No reading for more than three of the check's intervals"`
}

type hostCheckDTO struct {
	DefinitionID string `json:"definition_id"`
	Key          string `json:"key"`
	// Plugin is a closed set here, unlike checkDefinitionBody's own
	// write-side field (which internal/checks validates and answers for,
	// so huma must not answer first with different wording). On this read
	// path nothing is validated and the enum is what the GUI needs: it
	// decides from the plugin whether a metric of this check has a series
	// behind it at all, which the latest value cannot answer while
	// collection is failing — an error row carries neither value nor
	// label (migration 00044's CHECK).
	Plugin          string              `json:"plugin" enum:"cpu,memory,disk,service"`
	Params          map[string]any      `json:"params"`
	IntervalSeconds int                 `json:"interval_seconds"`
	Enabled         bool                `json:"enabled" doc:"false means the winning scope turned this check off; it is listed so the GUI can show where"`
	Origin          string              `json:"origin" enum:"seed,custom"`
	Source          string              `json:"source" enum:"org,group,host" doc:"The scope the winning definition is bound at"`
	SourceID        string              `json:"source_id,omitempty" doc:"The group or host it is bound to; absent for an organization-bound check"`
	SourceName      string              `json:"source_name,omitempty"`
	Values          []hostCheckValueDTO `json:"values"`
}

type hostChecksOutput struct {
	Body struct {
		HostID string `json:"host_id"`
		// Monitored is the host's own flag, reported explicitly because
		// the effective set does not depend on it: a host that is not
		// monitored still resolves its bindings to a definite winner per
		// key, but no agent ever runs them. Without this flag an
		// unmonitored host would be indistinguishable from one nobody has
		// configured a check for, and the page would say "no checks" about
		// a host that is simply not being watched.
		Monitored bool           `json:"monitored"`
		Checks    []hostCheckDTO `json:"checks"`
	}
}

type hostMetricsInput struct {
	Org    string `path:"org"`
	ID     string `path:"id"`
	Check  string `query:"check" doc:"The check key, as reported by GET /hosts/{id}/checks"`
	Item   string `query:"item" doc:"The mount or unit; omit for a whole-host metric"`
	Metric string `query:"metric"`
	Range  string `query:"range" doc:"One of 1h, 24h, 7d"`
}

type metricPointDTO struct {
	At    time.Time `json:"at"`
	Value float64   `json:"value"`
}

type hostMetricsOutput struct {
	Body struct {
		Check  string           `json:"check"`
		Item   string           `json:"item"`
		Metric string           `json:"metric"`
		Range  string           `json:"range"`
		From   time.Time        `json:"from"`
		To     time.Time        `json:"to"`
		Points []metricPointDTO `json:"points"`
	}
}

// registerHostCheckRoutes wires the per-host monitoring reads.
func registerHostCheckRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/hosts/{id}/checks", func(ctx context.Context, in *checkIDInput) (*hostChecksOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksRead)
		if err != nil {
			return nil, err
		}
		hostID, err := parseUUID(in.ID, "host id")
		if err != nil {
			return nil, err
		}
		// The host itself, first: an id from another organization must
		// read as "no such host" rather than as an empty effective set,
		// which would confirm the id exists somewhere.
		host, err := deps.Hosts.GetHost(ctx, scope.OrgID, hostID)
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		effective, err := deps.Checks.EffectiveHost(ctx, scope.OrgID, hostID)
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		current, err := deps.Checks.CurrentForHost(ctx, scope.OrgID, hostID)
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		groupNames, err := monitoringGroupNames(ctx, deps, scope.OrgID)
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}

		out := &hostChecksOutput{}
		out.Body.HostID = hostID.String()
		out.Body.Monitored = host.Monitored
		out.Body.Checks = make([]hostCheckDTO, 0, len(effective))
		byKey := currentByKey(current)
		now := time.Now()
		for _, d := range effective {
			out.Body.Checks = append(out.Body.Checks, toHostCheckDTO(d, byKey[d.Key], groupNames, host.Name, now))
		}
		return out, nil
	})

	huma.Get(hapi, "/api/v1/orgs/{org}/hosts/{id}/metrics", func(ctx context.Context, in *hostMetricsInput) (*hostMetricsOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksRead)
		if err != nil {
			return nil, err
		}
		hostID, err := parseUUID(in.ID, "host id")
		if err != nil {
			return nil, err
		}
		checkKey, metric := strings.TrimSpace(in.Check), strings.TrimSpace(in.Metric)
		if checkKey == "" || metric == "" {
			return nil, huma.Error400BadRequest("check and metric are required")
		}
		spec, ok := metricRanges[in.Range]
		if !ok {
			return nil, huma.Error400BadRequest("range must be one of " + strings.Join(metricRangeNames, ", "))
		}
		if _, err := deps.Hosts.GetHost(ctx, scope.OrgID, hostID); err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		// A graph is a line through numbers. The store's series query
		// scans value as a float, so asking it for a discrete metric —
		// a service's active/failed state — fails deep inside the scan
		// with a message about column types that says nothing an operator
		// could act on. The host's own current values are what distinguish
		// the two (exactly one of value and label is ever set), so the
		// refusal happens here, in the caller's own terms, before the
		// service is reached at all.
		current, err := deps.Checks.CurrentForHost(ctx, scope.OrgID, hostID)
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		if err := requireNumericMetric(current, checkKey, in.Item, metric); err != nil {
			return nil, err
		}

		to := time.Now()
		from := to.Add(-spec.window)
		series, err := deps.Checks.SeriesForHost(ctx, scope.OrgID, hostID, checkKey, in.Item, metric, from, to, spec.buckets)
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		out := &hostMetricsOutput{}
		out.Body.Check, out.Body.Item, out.Body.Metric = checkKey, in.Item, metric
		out.Body.Range, out.Body.From, out.Body.To = in.Range, from, to
		out.Body.Points = make([]metricPointDTO, len(series.Points))
		for i, p := range series.Points {
			out.Body.Points[i] = metricPointDTO{At: p.At, Value: p.Value}
		}
		return out, nil
	})
}

// requireNumericMetric refuses a metric this host has never reported (404 —
// there is nothing to graph, and the caller most likely mistyped a key or
// an item) and one it reports as a label (400, naming the label, since no
// spelling of the request would make that metric a series).
func requireNumericMetric(current []checks.Current, checkKey, item, metric string) error {
	for _, c := range current {
		if c.CheckKey != checkKey || c.Item != item || c.Metric != metric {
			continue
		}
		if c.Label != nil {
			return huma.Error400BadRequest(fmt.Sprintf(
				"metric %q of check %q is a discrete value (%q), not a number; only numeric metrics have a graph",
				metric, checkKey, *c.Label))
		}
		return nil
	}
	return huma.Error404NotFound(fmt.Sprintf("host has no metric %q for check %q", metric, checkKey))
}

// currentByKey groups a host's latest values by the check key that
// produced them, each group sorted by item then metric so the GUI renders
// a stable order. Values whose key is not in the effective set — a check
// deleted or rebound since it last reported — are simply never looked up:
// this view answers "what runs on this host and what is it saying", not
// "everything this host has ever reported".
func currentByKey(current []checks.Current) map[string][]checks.Current {
	byKey := map[string][]checks.Current{}
	for _, c := range current {
		byKey[c.CheckKey] = append(byKey[c.CheckKey], c)
	}
	for _, group := range byKey {
		sort.Slice(group, func(i, j int) bool {
			if group[i].Item != group[j].Item {
				return group[i].Item < group[j].Item
			}
			return group[i].Metric < group[j].Metric
		})
	}
	return byKey
}

func toHostCheckDTO(d checks.Definition, values []checks.Current, groupNames map[string]string, hostName string, now time.Time) hostCheckDTO {
	params := d.Params
	if params == nil {
		params = map[string]any{}
	}
	dto := hostCheckDTO{
		DefinitionID: d.ID.String(), Key: d.Key, Plugin: d.Plugin, Params: params,
		IntervalSeconds: int(d.Interval / time.Second), Enabled: d.Enabled, Origin: d.Origin,
		Source: string(d.BindingKind),
	}
	switch d.BindingKind {
	case checks.BindingGroup:
		if d.GroupID != nil {
			dto.SourceID = d.GroupID.String()
			dto.SourceName = groupNames[dto.SourceID]
		}
	case checks.BindingHost:
		if d.HostID != nil {
			dto.SourceID = d.HostID.String()
			dto.SourceName = hostName
		}
	case checks.BindingOrg:
		// Bound to the organization itself: no single group or host to name.
	}
	dto.Values = make([]hostCheckValueDTO, len(values))
	for i, c := range values {
		v := hostCheckValueDTO{Item: c.Item, Metric: c.Metric, Value: c.Value, Error: c.Err, At: c.At}
		if c.Label != nil {
			v.Label = *c.Label
		}
		// Staleness is measured against this check's own interval, so a
		// five-minute disk check is not called stale a minute after its
		// last reading while a ten-second CPU check is.
		v.Stale = now.Sub(c.At) > time.Duration(staleIntervals)*d.Interval
		dto.Values[i] = v
	}
	return dto
}

// monitoringGroupNames maps group id to name, for naming the scope a
// group-bound check came from ("why does this check run on this host").
func monitoringGroupNames(ctx context.Context, deps Deps, orgID uuid.UUID) (map[string]string, error) {
	groups, err := deps.Checks.ListGroups(ctx, orgID)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(groups))
	for _, g := range groups {
		names[g.ID.String()] = g.Name
	}
	return names, nil
}
