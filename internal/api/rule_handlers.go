package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/rules"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// The Rules tab (v0.3b-2 spec, "API and permissions"): rule CRUD, the
// validate-only route the rule editor drives before it ever saves anything,
// a read-only metric catalogue so the editor's picker cannot drift from
// what internal/rules actually knows how to evaluate, and one host's
// effective view — who decided each tuple, and who they shadowed. Same
// permissions as the check definitions of v0.3b-1 (checks.read /
// checks.write): what triggers a state on a host is part of operating that
// host, not a separate concern with its own bar.

// ruleFormDTO mirrors rules.Form on the wire, both for reading a form-mode
// rule back (toRuleDTO) and for writing one (ruleBody.Form).
type ruleFormDTO struct {
	Metric     string   `json:"metric"`
	Op         string   `json:"op" doc:"Numeric: > >= < <= == !=; discrete: == != in"`
	Value      float64  `json:"value,omitempty" doc:"Numeric metrics"`
	Labels     []string `json:"labels,omitempty" doc:"Discrete metrics"`
	ClearOp    string   `json:"clear_op,omitempty" doc:"Empty when the rule has no clear condition"`
	ClearValue float64  `json:"clear_value,omitempty"`
}

// ruleDTO is the read shape of a rule. Form is set exactly when Mode is
// "form" and Expression exactly when Mode is "expression" — a rule is
// never both, the same split rules.RuleInput itself enforces on the way in.
type ruleDTO struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Enabled    bool         `json:"enabled"`
	Origin     string       `json:"origin" enum:"seed,user"`
	Scope      string       `json:"scope" enum:"org,group,host,item"`
	GroupID    string       `json:"group_id,omitempty"`
	GroupName  string       `json:"group_name,omitempty" doc:"The bound group's name, for a group-scoped rule; absent otherwise, and absent for a group this organization can no longer read"`
	HostID     string       `json:"host_id,omitempty"`
	HostName   string       `json:"host_name,omitempty" doc:"The bound host's name, for a host- or item-scoped rule; absent otherwise, and absent for a host this organization can no longer read"`
	Item       string       `json:"item,omitempty"`
	CheckKey   string       `json:"check_key" doc:"Derived from metric; the check this rule's metric belongs to"`
	Metric     string       `json:"metric"`
	Severity   string       `json:"severity" enum:"warn,crit"`
	Mode       string       `json:"mode" enum:"form,expression"`
	Form       *ruleFormDTO `json:"form,omitempty" doc:"Set when mode is form"`
	Expression string       `json:"expression,omitempty" doc:"Set when mode is expression"`
	Source     string       `json:"source" doc:"A rendered summary in form mode, the caller's own text in expression mode"`
	ForSeconds int          `json:"for_seconds" doc:"How long a deterioration must hold before it counts"`
	CreatedAt  time.Time    `json:"created_at"`
	UpdatedAt  time.Time    `json:"updated_at"`
}

// ruleBody is the create/update/validate write shape. It deliberately
// carries no enum, minimum or maxLength constraints of its own beyond the
// two uuid formats: internal/rules validates every one of these fields
// already (name length, scope/binding consistency, known metric, severity,
// duration bounds, the tree itself), and duplicating those bounds here
// would make the same bad input answer 422 with huma's generic wording on
// one field and the service's own specific message on the next — the same
// tradeoff checkDefinitionBody documents for the checks routes.
type ruleBody struct {
	Name       string       `json:"name"`
	Enabled    *bool        `json:"enabled,omitempty" doc:"Defaults to true"`
	Scope      string       `json:"scope" doc:"One of org, group, host, item"`
	GroupID    string       `json:"group_id,omitempty" format:"uuid" doc:"Required for scope=group, forbidden otherwise"`
	HostID     string       `json:"host_id,omitempty" format:"uuid" doc:"Required for scope=host or scope=item, forbidden otherwise"`
	Item       string       `json:"item,omitempty" doc:"Required for scope=item, forbidden otherwise"`
	Metric     string       `json:"metric" doc:"One of the metrics GET /rules/metrics lists"`
	Severity   string       `json:"severity" doc:"warn or crit"`
	Mode       string       `json:"mode" doc:"form or expression"`
	Form       *ruleFormDTO `json:"form,omitempty" doc:"Required for mode=form, forbidden otherwise"`
	Expression string       `json:"expression,omitempty" doc:"Required for mode=expression, forbidden otherwise"`
	ForSeconds int          `json:"for_seconds,omitempty" doc:"How long a deterioration must hold before it counts, 0-86400 seconds"`
}

type ruleListInput struct {
	Org   string `path:"org"`
	Scope string `query:"scope" doc:"Filter by scope: org, group, host or item"`
	Check string `query:"check" doc:"Filter by check key"`
	Host  string `query:"host" doc:"Filter by host id"`
}

type ruleCreateInput struct {
	Org  string `path:"org"`
	Body ruleBody
}

type ruleUpdateInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body ruleBody
}

type ruleValidateInput struct {
	Org  string `path:"org"`
	Body ruleBody
}

type ruleListOutput struct {
	Body struct {
		Rules []ruleDTO `json:"rules"`
	}
}

type ruleOutput struct {
	Body struct {
		Rule ruleDTO `json:"rule"`
	}
}

// ruleCheckedDTO mirrors rules.Checked on the wire.
type ruleCheckedDTO struct {
	OK        bool   `json:"ok"`
	Pos       int    `json:"pos,omitempty" doc:"Byte offset of a parse or type-check failure, for the editor's caret"`
	Message   string `json:"message,omitempty"`
	FormReady bool   `json:"form_ready" doc:"Whether the compiled tree still fits the form editor's subset"`
	Source    string `json:"source,omitempty" doc:"What Create/Update would store: a rendered summary in form mode, the caller's own text in expression mode"`
}

type ruleCheckedOutput struct {
	Body ruleCheckedDTO
}

// ruleMetricDTO is one entry of the metric catalogue GET /rules/metrics
// serves — the controller's own addition beyond the plan's route table
// (task-10 pre-flight scan): the rule editor's metric picker has no other
// honest source, and a list hand-copied into the SPA would silently drift
// from rules.Catalogue() the moment a plugin gained a metric.
type ruleMetricDTO struct {
	Name     string `json:"name"`
	CheckKey string `json:"check_key"`
	Kind     string `json:"kind" enum:"numeric,discrete"`
}

type ruleMetricsOutput struct {
	Body struct {
		Metrics []ruleMetricDTO `json:"metrics"`
	}
}

// effectiveRuleDTO is one (tuple, severity) decision of the effective view:
// who won, who they shadowed, and whether the tuple is still one the host
// actually reports.
type effectiveRuleDTO struct {
	CheckKey string    `json:"check_key"`
	Item     string    `json:"item,omitempty"`
	Metric   string    `json:"metric"`
	Severity string    `json:"severity" enum:"warn,crit"`
	Winner   ruleDTO   `json:"winner"`
	Shadowed []ruleDTO `json:"shadowed"`
	Observed bool      `json:"observed" doc:"False when an item-scoped rule names an item the host no longer reports"`
}

type effectiveRulesOutput struct {
	Body struct {
		Effective []effectiveRuleDTO `json:"effective"`
	}
}

// toRuleFormDTO renders f for the read routes.
func toRuleFormDTO(f rules.Form) ruleFormDTO {
	return ruleFormDTO{Metric: f.Metric, Op: f.Op, Value: f.Value, Labels: f.Labels, ClearOp: f.ClearOp, ClearValue: f.ClearValue}
}

// toRuleDTO renders r for the read routes. A form-mode rule's trigger/clear
// pair always round-trips through rules.AsForm — that is Compile's own
// contract (form.go) — so Form is populated whenever Mode is "form"; the
// `!ok` branch is defensive, never expected to be hit for a rule this
// service itself built.
func toRuleDTO(r rules.Rule) ruleDTO {
	dto := ruleDTO{
		ID: r.ID.String(), Name: r.Name, Enabled: r.Enabled, Origin: r.Origin,
		Scope: string(r.Scope), Item: r.Item, CheckKey: r.CheckKey, Metric: r.Metric,
		Severity: string(r.Severity), Mode: r.Mode, Source: r.Source,
		ForSeconds: int(r.For / time.Second), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if r.GroupID != nil {
		dto.GroupID = r.GroupID.String()
	}
	if r.HostID != nil {
		dto.HostID = r.HostID.String()
	}
	switch r.Mode {
	case "form":
		if f, ok := rules.AsForm(r.Trigger, r.Clear); ok {
			form := toRuleFormDTO(f)
			dto.Form = &form
		}
	case "expression":
		dto.Expression = r.Source
	}
	return dto
}

// ruleHostNames resolves, for a whole page of rules, the names of the
// hosts the host- and item-scoped ones name — the same shape
// check_handlers.go's checkHostNames resolves for checkDefinitionDTO's own
// HostName, one store read for the page (deps.Hosts.ListHostsByIDs)
// rather than one per rule. A failure to read is logged and answered with
// no names: host_name is a label on a binding the operator can still see
// by id, and losing the label is a far smaller failure than a 500 on the
// whole list.
func ruleHostNames(ctx context.Context, deps Deps, orgID uuid.UUID, list []rules.Rule) map[uuid.UUID]string {
	ids := make([]uuid.UUID, 0, len(list))
	for _, r := range list {
		if r.HostID != nil {
			ids = append(ids, *r.HostID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	found, err := deps.Hosts.ListHostsByIDs(ctx, orgID, ids)
	if err != nil {
		slog.ErrorContext(ctx, "resolve host names for rules", "org_id", orgID, "err", err)
		return nil
	}
	names := make(map[uuid.UUID]string, len(found))
	for _, h := range found {
		names[h.ID] = h.Name
	}
	return names
}

// ruleGroupNames resolves, for a whole page of rules, the names of the
// monitoring groups the group-scoped ones name. Unlike ruleHostNames this
// reads every group in the organization in one call
// (deps.Checks.ListGroups) rather than the ones a batch of ids names: an
// organization's monitoring groups are a bounded, small set (unlike its
// hosts), and every other reader of them in this codebase — the fleet
// filter, the host page's group bundle — already reads the whole list the
// same way, so a rule's own group name is one more use of that same read,
// not a second shape of lookup.
func ruleGroupNames(ctx context.Context, deps Deps, orgID uuid.UUID) map[uuid.UUID]string {
	found, err := deps.Checks.ListGroups(ctx, orgID)
	if err != nil {
		slog.ErrorContext(ctx, "resolve group names for rules", "org_id", orgID, "err", err)
		return nil
	}
	names := make(map[uuid.UUID]string, len(found))
	for _, g := range found {
		names[g.ID] = g.Name
	}
	return names
}

// ruleHostName is ruleHostNames for a route that answers with a single
// rule: one lookup rather than a whole-page batch. Empty wherever the host
// cannot be read — a host deleted between the two reads, say — which is
// exactly what the omitempty field means.
func ruleHostName(ctx context.Context, deps Deps, orgID uuid.UUID, r rules.Rule) string {
	if r.HostID == nil {
		return ""
	}
	h, err := deps.Hosts.GetHost(ctx, orgID, *r.HostID)
	if err != nil {
		slog.DebugContext(ctx, "resolve host name for a rule", "org_id", orgID, "host_id", *r.HostID, "err", err)
		return ""
	}
	return h.Name
}

// ruleGroupName is ruleGroupNames for a route that answers with a single
// rule.
func ruleGroupName(ctx context.Context, deps Deps, orgID uuid.UUID, r rules.Rule) string {
	if r.GroupID == nil {
		return ""
	}
	g, err := deps.Checks.GetGroup(ctx, orgID, *r.GroupID)
	if err != nil {
		slog.DebugContext(ctx, "resolve group name for a rule", "org_id", orgID, "group_id", *r.GroupID, "err", err)
		return ""
	}
	return g.Name
}

func toCheckedDTO(c rules.Checked) ruleCheckedDTO {
	return ruleCheckedDTO{OK: c.OK, Pos: c.Pos, Message: c.Message, FormReady: c.FormReady, Source: c.Source}
}

// toRuleInput decodes a request body into the service's own input shape.
// Only the two ids are parsed here (a malformed uuid is a transport
// concern, exactly like toDefinitionInput in check_handlers.go); everything
// else is handed over exactly as it arrived, for internal/rules to accept
// or refuse.
func toRuleInput(b ruleBody) (rules.RuleInput, error) {
	groupID, err := optionalUUID(b.GroupID, "group id")
	if err != nil {
		return rules.RuleInput{}, err
	}
	hostID, err := optionalUUID(b.HostID, "host id")
	if err != nil {
		return rules.RuleInput{}, err
	}
	enabled := true
	if b.Enabled != nil {
		enabled = *b.Enabled
	}
	var form *rules.Form
	if b.Form != nil {
		form = &rules.Form{
			Metric: b.Form.Metric, Op: b.Form.Op, Value: b.Form.Value,
			Labels: b.Form.Labels, ClearOp: b.Form.ClearOp, ClearValue: b.Form.ClearValue,
		}
	}
	return rules.RuleInput{
		Name: b.Name, Enabled: enabled, Scope: rules.Scope(b.Scope),
		GroupID: groupID, HostID: hostID, Item: b.Item,
		Metric: b.Metric, Severity: rules.Severity(b.Severity), Mode: b.Mode,
		Form: form, Expression: b.Expression,
		For: time.Duration(b.ForSeconds) * time.Second,
	}, nil
}

// toRuleFilter decodes the list route's query parameters into the service's
// own Filter shape.
func toRuleFilter(in *ruleListInput) (rules.Filter, error) {
	hostID, err := optionalUUID(in.Host, "host id")
	if err != nil {
		return rules.Filter{}, err
	}
	return rules.Filter{Scope: rules.Scope(in.Scope), CheckKey: in.Check, HostID: hostID}, nil
}

// toGroupRanks adapts checks.Service.GroupsForHost's result to what
// rules.Resolve needs — the same three fields internal/app's own adapter
// (rules_ports.go, NewRulesHostChecks) extracts for the evaluation ticker.
// Mirrored here rather than shared: internal/api cannot import internal/app
// (which itself imports internal/api's Deps-adjacent wiring types), and the
// conversion is three fields wide.
func toGroupRanks(groups []checks.Group) []rules.GroupRank {
	out := make([]rules.GroupRank, len(groups))
	for i, g := range groups {
		out[i] = rules.GroupRank{ID: g.ID, Priority: g.Priority, CreatedAt: g.CreatedAt}
	}
	return out
}

// toTuples adapts a host's current values to the tuples rules.Resolve
// judges against, keeping only the ones whose check still resolves an
// interval. checks.Current is already one row per (check_key, item,
// metric) — the store's own primary key — so no deduplication is needed.
//
// The filter is the same one currentTupleStates applies to the host page's
// states (host_check_handlers.go), from the same source, and for the same
// reason: a check_current row outlives the definition that produced it
// (backlog 3.28), and a tuple whose check was disabled or deleted is one
// the evaluator stopped judging from that moment on. Listing a winner and
// its shadowed rules for such a tuple here while the host page does not
// show the tuple at all would be two reads of one host disagreeing about
// which of its tuples exist.
func toTuples(current []checks.Current, resolved map[string]bool) []rules.Tuple {
	out := make([]rules.Tuple, 0, len(current))
	for _, c := range current {
		if !resolved[c.CheckKey] {
			continue
		}
		out = append(out, rules.Tuple{CheckKey: c.CheckKey, Item: c.Item, Metric: c.Metric})
	}
	return out
}

func toEffectiveDTO(e rules.Effective) effectiveRuleDTO {
	shadowed := make([]ruleDTO, len(e.Shadowed))
	for i, r := range e.Shadowed {
		shadowed[i] = toRuleDTO(r)
	}
	return effectiveRuleDTO{
		CheckKey: e.Tuple.CheckKey, Item: e.Tuple.Item, Metric: e.Tuple.Metric,
		Severity: string(e.Severity), Winner: toRuleDTO(e.Winner), Shadowed: shadowed, Observed: e.Observed,
	}
}

func metricKindString(k rules.MetricKind) string {
	switch k {
	case rules.MetricNumeric:
		return "numeric"
	case rules.MetricDiscrete:
		return "discrete"
	default:
		return "unknown"
	}
}

// registerRuleRoutes wires the rule surface: the metric catalogue, CRUD,
// the validate-only route, and one host's effective view.
func registerRuleRoutes(hapi huma.API, deps Deps) {
	// Registered ahead of GET /rules/{id} so nothing depends on a router
	// that happens to try registration order before path specificity for
	// "metrics" vs. "{id}" — chi itself prefers the static segment either
	// way, but this keeps the file's own reading order honest about intent
	// (the controller's own ruling, task-10 pre-flight scan).
	huma.Get(hapi, "/api/v1/orgs/{org}/rules/metrics", func(ctx context.Context, in *checkOrgInput) (*ruleMetricsOutput, error) {
		if _, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksRead); err != nil {
			return nil, err
		}
		cat := rules.Catalogue()
		names := make([]string, 0, len(cat))
		for name := range cat {
			names = append(names, name)
		}
		sort.Strings(names)
		out := &ruleMetricsOutput{}
		out.Body.Metrics = make([]ruleMetricDTO, len(names))
		for i, name := range names {
			entry := cat[name]
			out.Body.Metrics[i] = ruleMetricDTO{Name: name, CheckKey: entry.CheckKey, Kind: metricKindString(entry.Kind)}
		}
		return out, nil
	})

	huma.Get(hapi, "/api/v1/orgs/{org}/rules", func(ctx context.Context, in *ruleListInput) (*ruleListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksRead)
		if err != nil {
			return nil, err
		}
		f, err := toRuleFilter(in)
		if err != nil {
			return nil, err
		}
		list, err := deps.Rules.List(ctx, scope.OrgID, f)
		if err != nil {
			return nil, mapRulesErr(ctx, err, 0)
		}
		// One batched lookup for the whole page, not one per rule — the
		// same shape checkHostNames uses for checkDefinitionDTO (backlog
		// 3.30). hostNames is scoped to the ids this page actually names;
		// groupNames reads the organization's whole (small, bounded) group
		// list, the same read every other group-name reader in this
		// codebase already does.
		hostNames := ruleHostNames(ctx, deps, scope.OrgID, list)
		groupNames := ruleGroupNames(ctx, deps, scope.OrgID)
		out := &ruleListOutput{}
		out.Body.Rules = make([]ruleDTO, len(list))
		for i, r := range list {
			out.Body.Rules[i] = toRuleDTO(r)
			if r.HostID != nil {
				out.Body.Rules[i].HostName = hostNames[*r.HostID]
			}
			if r.GroupID != nil {
				out.Body.Rules[i].GroupName = groupNames[*r.GroupID]
			}
		}
		return out, nil
	})

	huma.Get(hapi, "/api/v1/orgs/{org}/rules/{id}", func(ctx context.Context, in *checkIDInput) (*ruleOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksRead)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "rule id")
		if err != nil {
			return nil, err
		}
		r, err := deps.Rules.Get(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapRulesErr(ctx, err, 0)
		}
		out := &ruleOutput{}
		out.Body.Rule = toRuleDTO(r)
		out.Body.Rule.HostName = ruleHostName(ctx, deps, scope.OrgID, r)
		out.Body.Rule.GroupName = ruleGroupName(ctx, deps, scope.OrgID, r)
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/rules", func(ctx context.Context, in *ruleCreateInput) (*ruleOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksWrite)
		if err != nil {
			return nil, err
		}
		input, err := toRuleInput(in.Body)
		if err != nil {
			return nil, err
		}
		// Checked before it is written: Create's own error does not carry a
		// byte position (Service.Create discards it — see rules.Service's
		// buildRule), but Check runs the identical validation and hands it
		// back through Checked.Pos, so the editor's caret still lands
		// correctly on a rule that fails at create time, not only on one
		// checked live while typing.
		if checked, cerr := deps.Rules.Check(ctx, scope.OrgID, input); cerr != nil {
			return nil, mapRulesErr(ctx, cerr, checked.Pos)
		}
		r, err := deps.Rules.Create(ctx, scope.OrgID, input)
		if err != nil {
			return nil, mapRulesErr(ctx, err, 0)
		}
		setAuditObject(ctx, r.ID.String(), fmt.Sprintf("created rule %q (%s)", r.Name, r.Metric))
		out := &ruleOutput{}
		out.Body.Rule = toRuleDTO(r)
		out.Body.Rule.HostName = ruleHostName(ctx, deps, scope.OrgID, r)
		out.Body.Rule.GroupName = ruleGroupName(ctx, deps, scope.OrgID, r)
		return out, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createRule", "rule"))

	huma.Put(hapi, "/api/v1/orgs/{org}/rules/{id}", func(ctx context.Context, in *ruleUpdateInput) (*ruleOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "rule id")
		if err != nil {
			return nil, err
		}
		input, err := toRuleInput(in.Body)
		if err != nil {
			return nil, err
		}
		// Existence before deep validation, exactly like check_handlers.go's
		// own PUT: a malformed body against another organization's id (or
		// one that never existed) must answer the uniform 404, not 422 —
		// nothing about *what* the caller sent is relevant once *where*
		// they sent it does not exist.
		if _, err := deps.Rules.Get(ctx, scope.OrgID, id); err != nil {
			return nil, mapRulesErr(ctx, err, 0)
		}
		if checked, cerr := deps.Rules.Check(ctx, scope.OrgID, input); cerr != nil {
			return nil, mapRulesErr(ctx, cerr, checked.Pos)
		}
		r, err := deps.Rules.Update(ctx, scope.OrgID, id, input)
		if err != nil {
			return nil, mapRulesErr(ctx, err, 0)
		}
		setAuditObject(ctx, r.ID.String(), fmt.Sprintf("updated rule %q (%s)", r.Name, r.Metric))
		out := &ruleOutput{}
		out.Body.Rule = toRuleDTO(r)
		out.Body.Rule.HostName = ruleHostName(ctx, deps, scope.OrgID, r)
		out.Body.Rule.GroupName = ruleGroupName(ctx, deps, scope.OrgID, r)
		return out, nil
	}, withAudit("updateRule", "rule"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/rules/{id}", func(ctx context.Context, in *checkIDInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "rule id")
		if err != nil {
			return nil, err
		}
		// Read before deleting, for the same reason check_handlers.go's
		// delete does: the row is gone once Delete returns, and it also
		// makes a delete of another organization's id answer 404 before
		// anything is attempted.
		before, err := deps.Rules.Get(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapRulesErr(ctx, err, 0)
		}
		if err := deps.Rules.Delete(ctx, scope.OrgID, id); err != nil {
			return nil, mapRulesErr(ctx, err, 0)
		}
		setAuditObject(ctx, id.String(), fmt.Sprintf("deleted rule %q (%s)", before.Name, before.Metric))
		return &okOutput{}, nil
	}, withAudit("deleteRule", "rule"))

	huma.Post(hapi, "/api/v1/orgs/{org}/rules/validate", func(ctx context.Context, in *ruleValidateInput) (*ruleCheckedOutput, error) {
		// checks.read, not checks.write: validate exposes nothing a reader
		// cannot already reach — it answers a question about text the
		// caller just submitted, the same bar the metric catalogue route
		// above already clears, and exactly the permission
		// /monitoring-groups/preview and /schedules/preview use for the
		// same read-shaped-POST reason (monitoring_group_handlers.go,
		// schedule_handlers.go). Gating it behind the write permission
		// would leave three different treatments of the same route shape
		// in one API.
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksRead)
		if err != nil {
			return nil, err
		}
		input, err := toRuleInput(in.Body)
		if err != nil {
			return nil, err
		}
		// Persists nothing — it calls Check, never Create — a test proves
		// it (a create attempt right after a validate call still sees an
		// empty list). Not audited: this is a read shaped as a POST only
		// because the rule is a request body, exactly like
		// POST /monitoring-groups/preview and POST /schedules/preview
		// (both allowlisted in audit_coverage_test.go for the same reason)
		// — the editor calls this on a debounce, so auditing it would bury
		// the three real rule mutations (create, update, delete) under
		// keystroke noise. See auditAllowlist's own entry for this route.
		checked, err := deps.Rules.Check(ctx, scope.OrgID, input)
		if err != nil {
			return nil, mapRulesErr(ctx, err, checked.Pos)
		}
		out := &ruleCheckedOutput{}
		out.Body = toCheckedDTO(checked)
		return out, nil
	})

	huma.Get(hapi, "/api/v1/orgs/{org}/hosts/{id}/effective-rules", func(ctx context.Context, in *checkIDInput) (*effectiveRulesOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksRead)
		if err != nil {
			return nil, err
		}
		hostID, err := parseUUID(in.ID, "host id")
		if err != nil {
			return nil, err
		}
		// The host itself, first: an id from another organization must read
		// as "no such host" rather than an empty effective set, exactly like
		// GET /hosts/{id}/checks (host_check_handlers.go).
		if _, err := deps.Hosts.GetHost(ctx, scope.OrgID, hostID); err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		all, err := deps.Rules.List(ctx, scope.OrgID, rules.Filter{})
		if err != nil {
			return nil, mapRulesErr(ctx, err, 0)
		}
		groups, err := deps.Checks.GroupsForHost(ctx, scope.OrgID, hostID)
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		current, err := deps.Checks.CurrentForHost(ctx, scope.OrgID, hostID)
		if err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		resolved, err := resolvedCheckKeys(ctx, deps, scope.OrgID, hostID)
		if err != nil {
			return nil, err
		}
		host := rules.HostContext{HostID: hostID, Groups: toGroupRanks(groups), Tuples: toTuples(current, resolved)}
		effective := rules.Resolve(all, host)
		out := &effectiveRulesOutput{}
		out.Body.Effective = make([]effectiveRuleDTO, len(effective))
		for i, e := range effective {
			out.Body.Effective[i] = toEffectiveDTO(e)
		}
		return out, nil
	})
}

// mapRulesErr maps a rules.Service error to its HTTP status.
//
// ErrInvalidInput maps to 422. Its message names a byte offset in words
// ("at byte <n>") when the failure is a parse or type-check error, and
// Location is "body.expression" in that case, plain "body" otherwise (a
// scope/binding mismatch, a seeded rule's fixed field, or any other failure
// that carries no offset at all). This is deliberately just a message, not
// a machine-readable position: the editor's caret comes from the validate
// route's own response (Checked.Pos, a real integer field) — Task 12's
// editor validates on a debounce and disables Save while Checked.OK is
// false, so a 422 out of Create or Update is only the edge case where a
// validate call and a save raced each other, and a human-readable message
// is enough for that. huma's ErrorDetail.Value is not used for the offset
// at all: redactErrorValues (error_values.go) nils it unconditionally on
// every response (it exists to stop a schema violation from echoing a
// password or key back to the caller), so a byte offset placed there would
// always serialize as null.
//
// ErrNotFound maps to the uniform 404 every other domain service in this
// codebase maps to, including a cross-org id — rules.ErrNotFound's own doc
// comment says the two must read identically.
func mapRulesErr(ctx context.Context, err error, pos int) error {
	switch {
	case errors.Is(err, rules.ErrInvalidInput):
		msg := err.Error()
		loc := "body"
		if pos > 0 {
			loc = "body.expression"
			msg = fmt.Sprintf("%s (at byte %d)", msg, pos)
		}
		return huma.Error422UnprocessableEntity(msg, &huma.ErrorDetail{Message: msg, Location: loc})
	case errors.Is(err, rules.ErrNotFound):
		return huma.Error404NotFound("not found")
	}
	slog.ErrorContext(ctx, "unmapped rules error", "err", err)
	return huma.Error500InternalServerError("internal error")
}
