package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/assert"
)

// currentLabels is the checks store's one-tuple read the alert detail
// needs, on the alerts executor's savepoint.
type currentLabels interface {
	CurrentLabel(ctx context.Context, tx pgx.Tx, orgID, hostID uuid.UUID, checkKey, item, metric string) (string, error)
}

// hostCheckSet is the checks service's resolution of one host's checks —
// the same call the rule evaluation's load makes (rulesHostChecks).
type hostCheckSet interface {
	ResolveHostWithLocal(ctx context.Context, orgID, hostID uuid.UUID) (checks.ResolvedSet, error)
}

// alertDetails implements alerts.DetailSource over the checks store and
// service (backlog 3.13b, spec "4. Alert detail"). internal/alerts never
// imports internal/checks; the edge is here, in the wiring, as it is for
// rules.HostChecks.
type alertDetails struct {
	labels currentLabels
	checks hostCheckSet
}

// NewAlertDetails wires the adapter Build hands alerts.Service.
func NewAlertDetails(store *checks.SQLStore, svc *checks.Service) alerts.DetailSource {
	return alertDetails{labels: store, checks: svc}
}

// Lookup implements alerts.DetailSource: one Apply's lookups, which
// resolve the host's checks at most once however many assertions changed
// in the evaluation — the resolution reads the organization's definitions
// and groups and expands their selectors, all while the evaluator holds
// the host's cursor lock.
func (a alertDetails) Lookup() alerts.DetailLookup {
	return &hostDetails{alertDetails: a, resolved: map[hostKey]resolvedHost{}}
}

// hostDetails is one Apply's lookups, with the host's resolved checks
// memoised. Every subject of one Apply belongs to one host; the memo is
// keyed by organization and host anyway, so a caller that broke that
// promise would get a fresh resolution, never another host's checks. A
// failed resolution is memoised too: asking again inside the same
// evaluation would only repeat the cost.
type hostDetails struct {
	alertDetails
	resolved map[hostKey]resolvedHost
}

type hostKey struct{ org, host uuid.UUID }

type resolvedHost struct {
	set checks.ResolvedSet
	err error
}

// resolve is ResolveHostWithLocal, memoised per host.
func (h *hostDetails) resolve(ctx context.Context, orgID, hostID uuid.UUID) (checks.ResolvedSet, error) {
	k := hostKey{orgID, hostID}
	r, ok := h.resolved[k]
	if !ok {
		r.set, r.err = h.checks.ResolveHostWithLocal(db.WithOrgScope(ctx, orgID), orgID, hostID)
		h.resolved[k] = r
	}
	return r.set, r.err
}

// assertionPlugin is the plugin whose assertion verdict metric is
// <plugin>.assertion (http since backlog 3.13b; tcp, dns and tls since
// 3.87); false for any other metric.
func assertionPlugin(metric string) (assert.Dialect, bool) {
	plugin, ok := strings.CutSuffix(metric, ".assertion")
	if !ok {
		return assert.Dialect{}, false
	}
	return assert.DialectOf(plugin)
}

// Detail implements alerts.DetailLookup:
//
//   - <plugin>.up of a synthetic plugin: the current <plugin>.failure label
//     of the same host, check key and item, unless it is none;
//   - <plugin>.assertion of a plugin with assertions (http, tcp, dns,
//     tls): "<expression> — saw <actual>", the entry's expression from the
//     host's resolved check of that key and plugin, parsed in the plugin's
//     dialect, and the current <plugin>.assertion_actual of the same item,
//     either part left out when it is not there;
//   - anything else: nothing.
//
// The current values are read on tx, inside the executor's savepoint, so
// they are the values this tick evaluated. The check definitions resolve on an
// org-scoped context of their own, the way the evaluation's load resolves
// them (rulesHostChecks.ContextForHost): the resolution reads through
// several stores that rely on the organization binding, which the
// evaluator's unscoped transaction does not carry. Every part comes from
// the descriptor's fixed vocabulary, a cleaned probe label or the
// operator's own check definition — never from a library's error text.
func (h *hostDetails) Detail(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, sub alerts.Subject) (string, error) {
	if d, ok := assertionPlugin(sub.Metric); ok {
		return h.assertion(ctx, tx, orgID, sub, d)
	}
	plugin, rest, ok := strings.Cut(sub.Metric, ".")
	if !ok || rest != "up" || !checks.IsSynthetic(plugin) {
		return "", nil
	}
	label, err := h.labels.CurrentLabel(ctx, tx, orgID, sub.HostID, sub.CheckKey, sub.Item, plugin+".failure")
	if err != nil || label == probe.FailureNone {
		return "", err
	}
	return label, nil
}

// assertion is Detail for <plugin>.assertion of d's plugin.
func (h *hostDetails) assertion(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, sub alerts.Subject, d assert.Dialect) (string, error) {
	actual, err := h.labels.CurrentLabel(ctx, tx, orgID, sub.HostID, sub.CheckKey, sub.Item, d.Plugin()+".assertion_actual")
	if err != nil {
		return "", err
	}
	set, err := h.resolve(ctx, orgID, sub.HostID)
	if err != nil {
		return "", fmt.Errorf("resolve the host's checks: %w", err)
	}
	expr := assertionExpression(set, d, sub.CheckKey, sub.Item)
	switch {
	case expr != "" && actual != "":
		return expr + " — saw " + actual, nil
	case actual != "":
		return "saw " + actual, nil
	}
	return expr, nil
}

// assertionExpression is the canonical expression of the assertions entry
// named name in the check checkKey of set of d's plugin, "" when there is
// none or it does not parse in d.
func assertionExpression(set checks.ResolvedSet, d assert.Dialect, checkKey, name string) string {
	for _, c := range set.All() {
		if c.Key != checkKey || c.Plugin != d.Plugin() {
			continue
		}
		for _, entry := range stringEntries(c.Params["assertions"]) {
			if assert.EntryName(entry) != name {
				continue
			}
			if parsed, err := d.Parse(entry); err == nil {
				return parsed.Expression()
			}
			return ""
		}
	}
	return ""
}

// stringEntries reads a list param: []any as decoded from JSON, or
// []string from a Go caller; anything else, or a non-string entry, is
// skipped.
func stringEntries(v any) []string {
	switch list := v.(type) {
	case []string:
		return list
	case []any:
		out := make([]string, 0, len(list))
		for _, e := range list {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
