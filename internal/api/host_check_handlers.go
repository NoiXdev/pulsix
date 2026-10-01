package api

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/items"
	"github.com/noixdev/pulsix/internal/rules"
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
	// State, Since and Flapping are the rule engine's own verdict on this
	// tuple (v0.3b-2 Task 11), additive to the v0.3b-1 fields above: every
	// one of them keeps reporting exactly what it always did.
	//
	// State is deliberately nullable rather than defaulting to "ok": a
	// state is a statement about something observed (the design's own
	// principle, v0.3b-2 spec), so a tuple check_states carries no row for
	// — never evaluated, or its check no longer resolves an interval, a
	// deleted or disabled check whose row Task 8's evaluator leaves
	// untouched rather than deletes — reports null, not a fabricated
	// health nobody established. No omitempty: the field is always
	// present, explicitly null rather than silently missing, so the SPA
	// can render "not yet evaluated" without guessing whether the server
	// simply left the key out.
	State *string `json:"state" enum:"ok,warn,crit,unknown" doc:"null when this tuple has never been evaluated, or its check no longer resolves an interval"`
	// Since is absent exactly when State is null.
	Since *time.Time `json:"since,omitempty" doc:"When the current state began; absent when state is null"`
	// Flapping is always present, false (never true) exactly when State is
	// null — there is nothing to flap on a tuple with no state.
	Flapping bool `json:"flapping" doc:"This tuple changed state five or more times in the last 30 minutes"`
	// EvaluatedAt and StateStale are backlog 3.36: samples kept arriving
	// (Stale above stays false) is not the same question as "is the rule
	// engine still judging them" — a ticker that stopped, or a batch that
	// keeps missing its deadline, leaves the last verdict exactly as it was
	// the moment evaluation stopped, with nothing before this pair to say
	// so. EvaluatedAt is absent exactly when State is null, the same shape
	// Since already has, for display ("judged 4 minutes ago"); StateStale
	// mirrors Stale's own name and computation — three of the check's own
	// intervals — but against EvaluatedAt rather than the reading's own At,
	// and like Flapping it is always present, false (never true) exactly
	// when State is null. Computed here, not in the SPA: only the server
	// knows the check's resolved interval, and re-deriving the three-
	// interval rule client-side would be a second copy of a rule that
	// already lives in Go (toHostCheckDTO's own Stale computation below).
	EvaluatedAt *time.Time `json:"evaluated_at,omitempty" doc:"When the rule engine last judged this tuple; absent when state is null"`
	StateStale  bool       `json:"state_stale" doc:"Evaluated more than three of the check's own intervals ago; mirrors stale, but for the verdict rather than the reading"`
	// Ignored is v0.3b-3's own addition, additive like State/Since/Flapping
	// above: every existing field keeps its name and meaning. It reports
	// whether an item decision currently keeps this tuple from being
	// judged, resolved through items.Decide — the exact function the
	// evaluation ticker runs — rather than read off State: Apply deletes
	// an ignored tuple's check_states row (its own doc comment says why),
	// so a null State means both "never evaluated" and "being ignored",
	// two different statements this field tells apart. Measurement
	// continues regardless of this flag — ignoring never touches what the
	// agent is asked to collect. Always false for an item-less metric
	// (cpu, memory): there is nothing to ignore.
	Ignored bool `json:"ignored" doc:"An item decision currently keeps this tuple from being judged; measurement continues regardless"`
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
		// ClockOffsetSeconds is how far this host's clock ran behind the
		// manager's when its last readings were ingested — positive means
		// behind, so its readings look older than they are.
		//
		// It is here because `stale` alone cannot be acted on. A host
		// four minutes behind has every reading accepted (ingest's
		// plausibility window is an hour wide, for replay) and every
		// reading badged stale (three of the check's intervals, 180 s for
		// the seed checks), for ever, while the agent is connected and
		// perfectly healthy. Without this an operator cannot tell that
		// from a host that stopped reporting, and the remedies — NTP
		// there, look at the agent here — have nothing in common.
		//
		// Absent when this manager process has no measurement: it is
		// taken at ingest and kept in memory (internal/checks/skew.go),
		// so an agent whose stream lives on another manager, or one that
		// has not reported since this process started, reports nothing
		// rather than a made-up zero.
		ClockOffsetSeconds *float64 `json:"clock_offset_seconds,omitempty"`
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
		statesByTuple, err := currentTupleStates(ctx, deps, scope.OrgID, hostID)
		if err != nil {
			// currentTupleStates already maps and logs its own two failure
			// modes (checks.Service.ResolveHost versus rules.StateStore) under
			// their own names — see its own doc comment — so the error it
			// returns is already the response this route should give, not a
			// raw error for mapChecksErr to relabel as a checks failure.
			return nil, err
		}
		ignored, err := hostIgnoredItems(ctx, deps, scope.OrgID, hostID, current)
		if err != nil {
			return nil, err
		}

		out := &hostChecksOutput{}
		out.Body.HostID = hostID.String()
		out.Body.Monitored = host.Monitored
		if offset, ok := deps.Checks.ClockOffset(hostID); ok {
			seconds := offset.Offset.Seconds()
			out.Body.ClockOffsetSeconds = &seconds
		}
		out.Body.Checks = make([]hostCheckDTO, 0, len(effective))
		byKey := currentByKey(current)
		now := time.Now()
		for _, d := range effective {
			out.Body.Checks = append(out.Body.Checks, toHostCheckDTO(d, stillAsked(d, byKey[d.Key]), groupNames, host.Name, now, statesByTuple, ignored))
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
//
// A current row has three shapes, not two, and the third is the one that
// used to fall through here as "numeric, go ahead": a collection error
// carries NEITHER value nor label, so testing only for a label let a
// discrete metric reach the series query the moment its last reading
// failed. Two ordinary ingests on the seeded service check — a state
// reading, then a collection failure — were enough, and the request came
// back 500 (filed as backlog 3.29, closed by this change). An error row
// says nothing about the metric's type, so this cannot decide from it;
// what it CAN do is say so explicitly rather than by omission, and let
// the store answer. The store
// filters NULL values out of the series (see the SeriesForHost query), so
// the answer is an empty series for a discrete metric and the real
// history for a numeric one whose latest reading happened to fail — which
// is exactly the case the host page's graph affordance survives.
func requireNumericMetric(current []checks.Current, checkKey, item, metric string) error {
	for _, c := range current {
		if c.CheckKey != checkKey || c.Item != item || c.Metric != metric {
			continue
		}
		switch {
		case c.Label != nil:
			return huma.Error400BadRequest(fmt.Sprintf(
				"metric %q of check %q is a discrete value (%q), not a number; only numeric metrics have a graph",
				metric, checkKey, *c.Label))
		case c.Value != nil:
			return nil // numeric, and known to be
		default:
			// Neither: collection failed and the row records only that.
			// Undecidable here, and deliberately not refused — see above.
			return nil
		}
	}
	return huma.Error404NotFound(fmt.Sprintf("host has no metric %q for check %q", metric, checkKey))
}

// currentByKey groups a host's latest values by the check key that
// produced them, each group sorted by item then metric so the GUI renders
// a stable order. Values whose key is not in the effective set — a check
// deleted or rebound since it last reported — are simply never looked up:
// this view answers "what runs on this host and what is it saying", not
// "everything this host has ever reported".
//
// The same question one level down is stillAsked's: a key can still be
// effective while one of its items no longer is.
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

// stillAsked drops the current values whose item d no longer asks the host
// for — a mount taken off a disk check's list, a unit taken off a service
// check's (backlog 3.28). Nothing deletes a check_current row, deliberately
// (checks.Current's doc comment and migration 00044 both say why), so
// without this the dropped item sits on the host page for ever and is
// eventually badged stale — which reads as "the agent stopped reporting
// this" when the truth is "you stopped asking for it".
//
// A definition that bounds no items keeps every value: checks.AsksForItem
// carries that rule, including the disk check with no mounts list, which
// means every filesystem the host finds. Only the current view is
// filtered; GET /hosts/{id}/metrics still serves the dropped item's
// samples, because a graph of a mount dropped last week is a real graph.
func stillAsked(d checks.Definition, values []checks.Current) []checks.Current {
	kept := values[:0:0]
	for _, v := range values {
		if d.AsksForItem(v.Item) {
			kept = append(kept, v)
		}
	}
	return kept
}

func toHostCheckDTO(d checks.Definition, values []checks.Current, groupNames map[string]string, hostName string, now time.Time, states map[rules.Tuple]rules.TupleState, ignored map[ignoredKey]bool) hostCheckDTO {
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
		if st, ok := states[rules.Tuple{CheckKey: d.Key, Item: c.Item, Metric: c.Metric}]; ok {
			state := string(st.State)
			since, evaluatedAt := st.Since, st.EvaluatedAt
			v.State, v.Since, v.Flapping = &state, &since, st.Flapping
			v.EvaluatedAt = &evaluatedAt
			// Same three-interval rule as Stale above, against the moment
			// the verdict was formed rather than the moment the reading
			// arrived: a stopped evaluator still has fresh samples flowing
			// in (Stale stays false) but stops advancing EvaluatedAt.
			v.StateStale = now.Sub(evaluatedAt) > time.Duration(staleIntervals)*d.Interval
		}
		if c.Item != "" {
			v.Ignored = ignored[ignoredKey{CheckKey: d.Key, Item: c.Item}]
		}
		dto.Values[i] = v
	}
	return dto
}

// ignoredKey identifies one (check_key, item) pair — an item decision's own
// unit of effect, since every metric of the same item shares its fate
// (items.Target carries no metric of its own, for the same reason).
type ignoredKey struct{ CheckKey, Item string }

// hostIgnoredItems resolves, for every (check_key, item) pair hostID's
// current values carry, whether an item decision currently makes it
// ignored — through items.Decide, the exact function the evaluation
// ticker runs, over the organization's full decision list (Decide's own
// applies() narrows a scope=host decision to its own host). This is the
// read side's only way to learn "ignored" at all: Apply deletes an ignored
// tuple's check_states row (its own doc comment says why), so there is no
// state to read it off of — see internal/items' own design note, "the read
// side therefore cannot learn 'ignored' from a state".
//
// Metrics with no item (cpu, memory) have nothing to ignore and are never
// looked up — the same filter hostCheckDTO's own Ignored field documents.
func hostIgnoredItems(ctx context.Context, deps Deps, orgID, hostID uuid.UUID, current []checks.Current) (map[ignoredKey]bool, error) {
	ds, err := deps.Items.ListDecisions(ctx, orgID)
	if err != nil {
		slog.ErrorContext(ctx, "host checks: item decisions", "org_id", orgID, "host_id", hostID, "err", err)
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := make(map[ignoredKey]bool, len(current))
	for _, c := range current {
		if c.Item == "" {
			continue
		}
		k := ignoredKey{CheckKey: c.CheckKey, Item: c.Item}
		if _, ok := out[k]; ok {
			continue
		}
		out[k] = items.Decide(ds, items.Target{HostID: hostID, CheckKey: c.CheckKey, Item: c.Item}).Ignored()
	}
	return out, nil
}

// currentTupleStates loads hostID's stored tuple states and keeps only the
// ones whose check currently resolves an interval — the read-side half of
// Task 8's own evaluator contract (states.go): a tuple whose check was
// deleted or disabled is excluded from evaluation from that point on, and
// its check_states row is deliberately left exactly as the evaluator last
// wrote it (states_store.go's own doc comment says why: it is not this
// package's write to make). Left unfiltered, a check that used to be crit
// and got disabled — or deleted outright — would show that stale verdict
// on the host page for ever, indistinguishable from a live one.
//
// deps.Checks.ResolveHost is the exact same input rules.ContextForHost
// feeds the evaluator (see internal/app/rules_ports.go's own adapter): a
// disabled definition, or a key nothing binds any more, is absent from its
// Checks list, so "present here" and "the evaluator would still evaluate
// this tuple" are the same question asked of the same data. A deleted
// check's tuple never reaches this filter at all — its key is not in the
// caller's own `effective` list either, so toHostCheckDTO never looks it
// up — this only ever matters for the disabled case, where the check
// still appears (Enabled: false) but must not carry a live-looking state.
//
// The two calls below fail on behalf of two different packages, and each
// error is mapped and logged in its own name rather than both being folded
// into mapChecksErr's "unmapped checks error" at the caller: ResolveHost's
// failure really is a checks.Service fault, but StatesForHost's is
// rules.StateStore's, and logging it as a checks failure would send
// on-call triage at the wrong package for a fault this route did nothing
// to cause.
func currentTupleStates(ctx context.Context, deps Deps, orgID, hostID uuid.UUID) (map[rules.Tuple]rules.TupleState, error) {
	resolvedKeys, err := resolvedCheckKeys(ctx, deps, orgID, hostID)
	if err != nil {
		return nil, err
	}
	states, err := deps.RuleStates.StatesForHost(ctx, orgID, hostID)
	if err != nil {
		slog.ErrorContext(ctx, "host checks: rule states", "org_id", orgID, "host_id", hostID, "err", err)
		return nil, huma.Error500InternalServerError("internal error")
	}
	byTuple := make(map[rules.Tuple]rules.TupleState, len(states))
	for _, st := range states {
		if !resolvedKeys[st.Tuple.CheckKey] {
			continue
		}
		byTuple[st.Tuple] = st
	}
	return byTuple, nil
}

// resolvedCheckKeys is the set of check keys the manager still resolves an
// interval for on hostID — the one question "does this tuple still exist?"
// reduces to on the read side, and the exact input rules.ContextForHost
// feeds the evaluator (internal/app/rules_ports.go's own adapter). A
// disabled definition, or a key nothing binds any more, is absent from
// ResolveHost's list, so "present here" and "the evaluator would still
// evaluate this tuple" are the same question asked of the same data.
//
// It is a helper rather than two copies because two routes read the same
// host and must agree about which of its tuples exist: this one
// (currentTupleStates, for the states on the host page) and the effective-
// rules view (rule_handlers.go). A tuple missing from one and listed by
// the other is two reads of one host contradicting each other.
func resolvedCheckKeys(ctx context.Context, deps Deps, orgID, hostID uuid.UUID) (map[string]bool, error) {
	resolved, err := deps.Checks.ResolveHost(ctx, orgID, hostID)
	if err != nil {
		return nil, mapChecksErr(ctx, err)
	}
	keys := make(map[string]bool, len(resolved.Checks))
	for _, c := range resolved.Checks {
		keys[c.Key] = true
	}
	return keys, nil
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
