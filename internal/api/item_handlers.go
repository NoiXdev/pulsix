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

	"github.com/noixdev/pulsix/internal/items"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// The Items surface (v0.3b-3 design, "API and permissions"): discovered
// items read-only, the decisions that say an item should not be judged,
// the preview a pattern editor drives before anyone has chosen ignore or
// watch, and — in host_check_handlers.go, additively — `ignored` on the
// existing per-host checks route. Same permission bar as checks and rules:
// reads at checks.read, decisions at checks.write, because a decision
// changes what is judged.

// EvalDirtyMarker marks a host's evaluation cursor dirty outside the
// evaluator's own write path — the identical single-row upsert
// internal/checks' ingest path issues on every accepted batch
// (checks/store_sql.go's own call to the generated MarkHostDirty), plus a
// batched sibling for the organization-scope case.
//
// The controller's own fix, not the spec's: creating, changing or deleting
// a decision did nothing to make the affected host(s) due again, so an
// operator who ignored a red item would watch it stay red until that
// host's own staleness deadline came round — minutes, in the worst case.
// "I ignored it and nothing happened" reads as broken. Marking is
// deliberately best-effort, the same posture CheckConfigPusher documents
// for its own post-write push: the decision write has already committed
// by the time this runs, and failing the whole request over a missed
// prompt-evaluation hint would turn a successful write into an error the
// operator cannot act on — the host's own next due time still re-judges it
// correctly, only later than intended.
//
// MarkHostsDirty exists for the same reason CheckConfigPusher splits
// PushHosts from a single-host call: an organization-scoped decision can
// affect hundreds of hosts, and a loop calling MarkHostDirty once per host
// inside the request would be exactly the serial-round-trip cost
// CheckConfigPusher's own doc comment warns against.
//
// rules.DirtyMarker (a value type, not a pointer — NewDirtyMarker returns
// one directly) satisfies this interface in production; declared here, with
// exactly the two methods this package calls, so internal/api never needs
// to import more of internal/rules than this.
type EvalDirtyMarker interface {
	MarkHostDirty(ctx context.Context, orgID, hostID uuid.UUID) error
	MarkHostsDirty(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) error
}

// itemDecisionDTO mirrors items.Decision on the wire. Verdict is the wire
// field "decision" — the column's own name, and the word an operator uses
// — exactly as items.Verdict's own doc comment says to keep it.
type itemDecisionDTO struct {
	ID        string    `json:"id"`
	Scope     string    `json:"scope" enum:"org,host"`
	HostID    string    `json:"host_id,omitempty" doc:"Set for scope=host, absent for scope=org"`
	CheckKey  string    `json:"check_key"`
	MatchKind string    `json:"match_kind" enum:"exact,glob"`
	Pattern   string    `json:"pattern"`
	Verdict   string    `json:"decision" enum:"ignore,watch"`
	Note      string    `json:"note,omitempty"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// itemDecisionBody is the create/update/preview write shape. Like
// ruleBody and checkDefinitionBody, it deliberately carries no enum or
// length constraints of its own beyond the uuid format: internal/items
// validates every one of these fields already, and duplicating those
// bounds here would answer the same bad input with huma's generic wording
// on one field and the service's own specific message on the next.
//
// Verdict and Note are only required by Create/Update, never by Preview
// (items.Service.Preview calls validateMatch, not buildDecision — see its
// own doc comment) — this handler must not re-impose that requirement at
// the HTTP layer, which is exactly why it is one shared body rather than a
// narrower preview-only shape.
type itemDecisionBody struct {
	Scope     string `json:"scope" doc:"org or host"`
	HostID    string `json:"host_id,omitempty" format:"uuid" doc:"Required for scope=host, forbidden otherwise"`
	CheckKey  string `json:"check_key" doc:"One of the check keys the organization's checks resolve"`
	MatchKind string `json:"match_kind" doc:"exact or glob"`
	Pattern   string `json:"pattern" doc:"A literal item name (exact) or a '*' glob (glob)"`
	Verdict   string `json:"decision,omitempty" doc:"ignore or watch; required for create/update, ignored by preview"`
	Note      string `json:"note,omitempty" doc:"Why, for the reader six months from now; at most 500 characters"`
}

// itemDTO is one discovered item plus its effective verdict — the read
// shape of items.View, resolved through items.Decide exactly like the
// evaluation ticker resolves it. HostID and HostName are set only by the
// organization-wide listing (GET /items); the per-host route
// (GET /hosts/{id}/items) already scopes by host and leaves them empty.
type itemDTO struct {
	HostID    string           `json:"host_id,omitempty" doc:"Set only on the organization-wide listing"`
	HostName  string           `json:"host_name,omitempty"`
	CheckKey  string           `json:"check_key"`
	Item      string           `json:"item"`
	FirstSeen time.Time        `json:"first_seen"`
	LastSeen  *time.Time       `json:"last_seen,omitempty" doc:"Absent when reported is false"`
	Reported  bool             `json:"reported" doc:"Whether this item is still among the host's current values"`
	Verdict   string           `json:"decision" enum:"ignore,watch" doc:"The effective verdict, resolved exactly like the evaluation ticker resolves it"`
	DecidedBy *itemDecisionDTO `json:"decided_by,omitempty" doc:"The decision that produced this verdict; absent when none matched (the default, watch)"`
}

type itemDecisionCreateInput struct {
	Org  string `path:"org"`
	Body itemDecisionBody
}

type itemDecisionUpdateInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body itemDecisionBody
}

type itemDecisionPreviewInput struct {
	Org  string `path:"org"`
	Body itemDecisionBody
}

type itemDecisionListOutput struct {
	Body struct {
		Decisions []itemDecisionDTO `json:"decisions"`
	}
}

type itemDecisionOutput struct {
	Body struct {
		Decision itemDecisionDTO `json:"decision"`
	}
}

// itemPreviewTargetDTO mirrors items.Target on the wire.
type itemPreviewTargetDTO struct {
	HostID   string `json:"host_id"`
	CheckKey string `json:"check_key"`
	Item     string `json:"item"`
}

type itemPreviewOutput struct {
	Body struct {
		Matches []itemPreviewTargetDTO `json:"matches"`
		Total   int                    `json:"total" doc:"Every matching item, not just the ones listed in matches"`
		Capped  bool                   `json:"capped" doc:"True when matches holds fewer items than total"`
	}
}

type hostItemsOutput struct {
	Body struct {
		Items []itemDTO `json:"items"`
	}
}

type orgItemsInput struct {
	Org   string `path:"org"`
	Check string `query:"check" doc:"Filter by check key"`
	Item  string `query:"item" doc:"Filter by item name; the same one-character '*' glob the decisions use"`
}

type orgItemsOutput struct {
	Body struct {
		Items []itemDTO `json:"items"`
	}
}

// toItemDecisionInput decodes a request body into the service's own input
// shape. Only HostID is parsed here (a malformed uuid is a transport
// concern, exactly like toRuleInput); everything else is handed over
// exactly as it arrived, for internal/items to accept or refuse.
func toItemDecisionInput(b itemDecisionBody) (items.DecisionInput, error) {
	hostID, err := optionalUUID(b.HostID, "host id")
	if err != nil {
		return items.DecisionInput{}, err
	}
	return items.DecisionInput{
		Scope: items.Scope(b.Scope), HostID: hostID, CheckKey: b.CheckKey,
		MatchKind: items.MatchKind(b.MatchKind), Pattern: b.Pattern,
		Verdict: items.Verdict(b.Verdict), Note: b.Note,
	}, nil
}

// toItemDecisionDTO renders d for the read routes.
func toItemDecisionDTO(d items.Decision) itemDecisionDTO {
	dto := itemDecisionDTO{
		ID: d.ID.String(), Scope: string(d.Scope), CheckKey: d.CheckKey,
		MatchKind: string(d.MatchKind), Pattern: d.Pattern, Verdict: string(d.Verdict),
		Note: d.Note, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
	if d.HostID != nil {
		dto.HostID = d.HostID.String()
	}
	if d.CreatedBy != nil {
		dto.CreatedBy = d.CreatedBy.String()
	}
	return dto
}

// toItemDTO renders v for the read routes, resolving its effective verdict
// through items.Decide — the exact function the evaluation ticker and
// GET /hosts/{id}/checks (host_check_handlers.go) both run. ds must
// contain every decision that could govern the rows handed in, and may
// contain more: Decide's own applies() narrows a scope=host decision to
// its own HostID regardless. The organization-wide listing therefore
// passes the whole organization's decisions (ListDecisions), while the
// per-host routes pass only the organization's plus that host's
// (items.Service.Decisions) — the same answer from the narrower read.
func toItemDTO(v items.View, ds []items.Decision) itemDTO {
	res := items.Decide(ds, items.Target{HostID: v.HostID, CheckKey: v.CheckKey, Item: v.Name})
	dto := itemDTO{
		CheckKey: v.CheckKey, Item: v.Name, FirstSeen: v.FirstSeen,
		Reported: v.Reported, Verdict: string(res.Verdict),
	}
	if v.Reported {
		lastSeen := v.LastSeen
		dto.LastSeen = &lastSeen
	}
	if res.Decision != nil {
		d := toItemDecisionDTO(*res.Decision)
		dto.DecidedBy = &d
	}
	return dto
}

// sortItemViews gives the item listings a stable order: by check, then
// item name, then host — so a page is reproducible across requests rather
// than reflecting whatever order the store's scan happened to return.
func sortItemViews(views []items.View) {
	sort.Slice(views, func(i, j int) bool {
		if views[i].CheckKey != views[j].CheckKey {
			return views[i].CheckKey < views[j].CheckKey
		}
		if views[i].Name != views[j].Name {
			return views[i].Name < views[j].Name
		}
		return views[i].HostID.String() < views[j].HostID.String()
	})
}

// itemHostNames resolves, for a whole page of items, the names of the
// hosts they were discovered on — the same batched-lookup shape
// rule_handlers.go's ruleHostNames uses for a page of rules, one store
// read rather than one per item. A failure to read is logged and answered
// with no names: host_name is a label on an id the operator can still see,
// and losing the label is a far smaller failure than a 500 on the whole
// listing.
func itemHostNames(ctx context.Context, deps Deps, orgID uuid.UUID, views []items.View) map[uuid.UUID]string {
	seen := map[uuid.UUID]bool{}
	ids := make([]uuid.UUID, 0, len(views))
	for _, v := range views {
		if !seen[v.HostID] {
			seen[v.HostID] = true
			ids = append(ids, v.HostID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	found, err := deps.Hosts.ListHostsByIDs(ctx, orgID, ids)
	if err != nil {
		slog.ErrorContext(ctx, "resolve host names for items", "org_id", orgID, "err", err)
		return nil
	}
	names := make(map[uuid.UUID]string, len(found))
	for _, h := range found {
		names[h.ID] = h.Name
	}
	return names
}

// itemDecisionSummary is the audit summary for the three mutating decision
// routes: verb, verdict, scope and pattern — never the note, which may
// carry anything an operator typed, the same restraint toRuleDTO's own
// audit summaries show toward a rule's source text.
func itemDecisionSummary(verb string, d items.Decision) string {
	return fmt.Sprintf("%s %s decision for check %q, pattern %q (%s scope)", verb, d.Verdict, d.CheckKey, d.Pattern, d.Scope)
}

// markDirtyForDecision marks the evaluation cursor dirty for every host d
// affects, so the very next tick re-judges them rather than waiting for
// each host's own staleness deadline — see EvalDirtyMarker's own doc
// comment. A host-scoped decision marks just that host; an
// organization-scoped one marks every *monitored* host in the organization
// in one batched call (EvalDirtyMarker.MarkHostsDirty), since Decide
// resolves an org-scoped decision against every one of them.
//
// Narrowed to monitored hosts deliberately, not out of caution: an
// unmonitored host runs no checks, so it has nothing an item decision could
// ever change the judgment of, and ClaimDueEvalCursors (due_sql.go) carries
// no monitored filter of its own — a dirtied unmonitored host is not merely
// ignored, it is claimed and evaluated on the next tick, spending a real
// slot in that batch for a host nothing is measuring. Marking it anyway
// would make review focus 1's own worst case (an organization pattern
// matching the whole fleet) burn ticker capacity on hosts that can never
// contain the item the pattern was written about.
//
// Best-effort: a failure is logged, never turned into an error response —
// the decision write has already committed, and the host's own next due
// time still re-judges it correctly regardless.
func markDirtyForDecision(ctx context.Context, deps Deps, orgID uuid.UUID, d items.Decision) {
	switch d.Scope {
	case items.ScopeHost:
		if d.HostID == nil {
			return
		}
		if err := deps.EvalDirty.MarkHostDirty(ctx, orgID, *d.HostID); err != nil {
			slog.ErrorContext(ctx, "mark host dirty after an item decision", "org_id", orgID, "host_id", *d.HostID, "err", err)
		}
	case items.ScopeOrg:
		hostList, err := deps.Hosts.ListAllHosts(ctx, orgID)
		if err != nil {
			slog.ErrorContext(ctx, "list hosts to mark dirty after an organization item decision", "org_id", orgID, "err", err)
			return
		}
		monitored := make([]uuid.UUID, 0, len(hostList))
		for _, h := range hostList {
			if h.Monitored {
				monitored = append(monitored, h.ID)
			}
		}
		if len(monitored) == 0 {
			return
		}
		if err := deps.EvalDirty.MarkHostsDirty(ctx, orgID, monitored); err != nil {
			slog.ErrorContext(ctx, "mark hosts dirty after an organization item decision", "org_id", orgID, "hosts", len(monitored), "err", err)
		}
	}
}

// registerItemRoutes wires the items surface: the two read-only listings,
// decision CRUD, and the preview route.
func registerItemRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/hosts/{id}/items", func(ctx context.Context, in *checkIDInput) (*hostItemsOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksRead)
		if err != nil {
			return nil, err
		}
		hostID, err := parseUUID(in.ID, "host id")
		if err != nil {
			return nil, err
		}
		// The host itself, first: an id from another organization must read
		// as "no such host", exactly like GET /hosts/{id}/checks.
		if _, err := deps.Hosts.GetHost(ctx, scope.OrgID, hostID); err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		views, err := deps.Items.ItemsForHost(ctx, scope.OrgID, hostID)
		if err != nil {
			return nil, mapItemsErr(ctx, err)
		}
		// The per-host read, so the per-host decision query (see
		// items.Service.Decisions): this route sits beside
		// GET /hosts/{id}/checks on every open host page and polls with
		// it, and no decision bound to another host can govern an item
		// here — toItemDTO's Decide would only drop it again.
		ds, err := deps.Items.Decisions(ctx, scope.OrgID, &hostID)
		if err != nil {
			return nil, mapItemsErr(ctx, err)
		}
		sortItemViews(views)
		out := &hostItemsOutput{}
		out.Body.Items = make([]itemDTO, len(views))
		for i, v := range views {
			out.Body.Items[i] = toItemDTO(v, ds)
		}
		return out, nil
	})

	huma.Get(hapi, "/api/v1/orgs/{org}/items", func(ctx context.Context, in *orgItemsInput) (*orgItemsOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksRead)
		if err != nil {
			return nil, err
		}
		views, err := deps.Items.ItemsForOrg(ctx, scope.OrgID, items.Filter{CheckKey: in.Check, ItemGlob: in.Item})
		if err != nil {
			return nil, mapItemsErr(ctx, err)
		}
		ds, err := deps.Items.ListDecisions(ctx, scope.OrgID)
		if err != nil {
			return nil, mapItemsErr(ctx, err)
		}
		sortItemViews(views)
		hostNames := itemHostNames(ctx, deps, scope.OrgID, views)
		out := &orgItemsOutput{}
		out.Body.Items = make([]itemDTO, len(views))
		for i, v := range views {
			dto := toItemDTO(v, ds)
			dto.HostID, dto.HostName = v.HostID.String(), hostNames[v.HostID]
			out.Body.Items[i] = dto
		}
		return out, nil
	})

	huma.Get(hapi, "/api/v1/orgs/{org}/item-decisions", func(ctx context.Context, in *checkOrgInput) (*itemDecisionListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksRead)
		if err != nil {
			return nil, err
		}
		list, err := deps.Items.ListDecisions(ctx, scope.OrgID)
		if err != nil {
			return nil, mapItemsErr(ctx, err)
		}
		out := &itemDecisionListOutput{}
		out.Body.Decisions = make([]itemDecisionDTO, len(list))
		for i, d := range list {
			out.Body.Decisions[i] = toItemDecisionDTO(d)
		}
		return out, nil
	})

	huma.Get(hapi, "/api/v1/orgs/{org}/item-decisions/{id}", func(ctx context.Context, in *checkIDInput) (*itemDecisionOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksRead)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "decision id")
		if err != nil {
			return nil, err
		}
		d, err := deps.Items.GetDecision(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapItemsErr(ctx, err)
		}
		out := &itemDecisionOutput{}
		out.Body.Decision = toItemDecisionDTO(d)
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/item-decisions", func(ctx context.Context, in *itemDecisionCreateInput) (*itemDecisionOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksWrite)
		if err != nil {
			return nil, err
		}
		input, err := toItemDecisionInput(in.Body)
		if err != nil {
			return nil, err
		}
		d, err := deps.Items.CreateDecision(ctx, scope.OrgID, scope.UserID, input)
		if err != nil {
			return nil, mapItemsErr(ctx, err)
		}
		markDirtyForDecision(ctx, deps, scope.OrgID, d)
		setAuditObject(ctx, d.ID.String(), itemDecisionSummary("created", d))
		out := &itemDecisionOutput{}
		out.Body.Decision = toItemDecisionDTO(d)
		return out, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createItemDecision", "item_decision"))

	huma.Put(hapi, "/api/v1/orgs/{org}/item-decisions/{id}", func(ctx context.Context, in *itemDecisionUpdateInput) (*itemDecisionOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "decision id")
		if err != nil {
			return nil, err
		}
		input, err := toItemDecisionInput(in.Body)
		if err != nil {
			return nil, err
		}
		// Existence before deep validation, exactly like rule_handlers.go's
		// own PUT: a malformed body against another organization's id (or
		// one that never existed) must answer the uniform 404, not 422.
		before, err := deps.Items.GetDecision(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapItemsErr(ctx, err)
		}
		d, err := deps.Items.UpdateDecision(ctx, scope.OrgID, id, input)
		if err != nil {
			return nil, mapItemsErr(ctx, err)
		}
		// Both the pre- and post-update target are marked: an edit that
		// moves a decision from one host to another (or between host and
		// org scope) must make the host it stopped applying to due again
		// too, not only the one it now applies to.
		markDirtyForDecision(ctx, deps, scope.OrgID, before)
		markDirtyForDecision(ctx, deps, scope.OrgID, d)
		setAuditObject(ctx, d.ID.String(), itemDecisionSummary("updated", d))
		out := &itemDecisionOutput{}
		out.Body.Decision = toItemDecisionDTO(d)
		return out, nil
	}, withAudit("updateItemDecision", "item_decision"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/item-decisions/{id}", func(ctx context.Context, in *checkIDInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "decision id")
		if err != nil {
			return nil, err
		}
		// Read before deleting, for the same reason rule_handlers.go's own
		// delete does: the row is gone once Delete returns, and it also
		// makes a delete of another organization's id answer 404 before
		// anything is attempted.
		before, err := deps.Items.GetDecision(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapItemsErr(ctx, err)
		}
		if err := deps.Items.DeleteDecision(ctx, scope.OrgID, id); err != nil {
			return nil, mapItemsErr(ctx, err)
		}
		markDirtyForDecision(ctx, deps, scope.OrgID, before)
		setAuditObject(ctx, id.String(), itemDecisionSummary("deleted", before))
		return &okOutput{}, nil
	}, withAudit("deleteItemDecision", "item_decision"))

	huma.Post(hapi, "/api/v1/orgs/{org}/item-decisions/preview", func(ctx context.Context, in *itemDecisionPreviewInput) (*itemPreviewOutput, error) {
		// checks.read, not checks.write: preview exposes nothing a reader
		// cannot already reach — it answers a question about a pattern the
		// caller just submitted, the same bar /rules/validate,
		// /monitoring-groups/preview and /schedules/preview all use for the
		// same read-shaped-POST reason.
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksRead)
		if err != nil {
			return nil, err
		}
		input, err := toItemDecisionInput(in.Body)
		if err != nil {
			return nil, err
		}
		// A host-scoped preview names a host, so it is a host-addressed
		// route and answers like one: an id from another organization
		// reads as "no such host", exactly as GET /hosts/{id}/items does
		// three handlers up. Nothing leaked before this guard — the store
		// filters on org_id, so the preview came back empty — but the
		// same body refused with 422 by the sibling create path answered
		// 200 here, which is one question with two answers and neither of
		// them the uniform 404 the rest of the surface gives.
		if input.Scope == items.ScopeHost && input.HostID != nil {
			if _, err := deps.Hosts.GetHost(ctx, scope.OrgID, *input.HostID); err != nil {
				return nil, mapChecksErr(ctx, err)
			}
		}
		// Persists nothing — it calls Preview, never CreateDecision (a test
		// proves it: a create attempt right after a preview call still sees
		// an empty decision list). Not audited: this is a read shaped as a
		// POST only because the pattern is a request body, exactly like the
		// three siblings named above (all allowlisted in
		// audit_coverage_test.go for the same reason) — see this route's
		// own entry there.
		preview, err := deps.Items.Preview(ctx, scope.OrgID, input)
		if err != nil {
			return nil, mapItemsErr(ctx, err)
		}
		out := &itemPreviewOutput{}
		out.Body.Matches = make([]itemPreviewTargetDTO, len(preview.Matches))
		for i, m := range preview.Matches {
			out.Body.Matches[i] = itemPreviewTargetDTO{HostID: m.HostID.String(), CheckKey: m.CheckKey, Item: m.Item}
		}
		out.Body.Total, out.Body.Capped = preview.Total, preview.Capped
		return out, nil
	})
}

// mapItemsErr maps an internal/items error to its HTTP status, the same
// shape mapRulesErr and mapChecksErr each give their own domain's errors.
func mapItemsErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, items.ErrInvalidInput):
		msg := err.Error()
		return huma.Error422UnprocessableEntity(msg, &huma.ErrorDetail{Message: msg, Location: "body"})
	case errors.Is(err, items.ErrNotFound):
		return huma.Error404NotFound("not found")
	}
	slog.ErrorContext(ctx, "unmapped items error", "err", err)
	return huma.Error500InternalServerError("internal error")
}
