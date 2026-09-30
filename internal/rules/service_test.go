package rules_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/rules"
)

var ctx = context.Background()

// fakeStore is an in-memory rules.Store: no database, so the whole
// validation table runs without Docker.
type fakeStore struct {
	mu   sync.Mutex
	byID map[uuid.UUID]rules.Rule
}

func newFakeStore() *fakeStore { return &fakeStore{byID: map[uuid.UUID]rules.Rule{}} }

func (f *fakeStore) Create(_ context.Context, r rules.Rule) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[r.ID] = r
	return nil
}

func (f *fakeStore) Update(_ context.Context, r rules.Rule) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	existing, ok := f.byID[r.ID]
	if !ok || existing.OrgID != r.OrgID {
		return rules.ErrNotFound
	}
	f.byID[r.ID] = r
	return nil
}

func (f *fakeStore) Delete(_ context.Context, orgID, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	existing, ok := f.byID[id]
	if !ok || existing.OrgID != orgID {
		return rules.ErrNotFound
	}
	delete(f.byID, id)
	return nil
}

func (f *fakeStore) Get(_ context.Context, orgID, id uuid.UUID) (rules.Rule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.byID[id]
	if !ok || r.OrgID != orgID {
		return rules.Rule{}, rules.ErrNotFound
	}
	return r, nil
}

func (f *fakeStore) List(_ context.Context, orgID uuid.UUID, filter rules.Filter) ([]rules.Rule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []rules.Rule
	for _, r := range f.byID {
		if r.OrgID != orgID {
			continue
		}
		if filter.Scope != "" && r.Scope != filter.Scope {
			continue
		}
		if filter.CheckKey != "" && r.CheckKey != filter.CheckKey {
			continue
		}
		if filter.HostID != nil && (r.HostID == nil || *r.HostID != *filter.HostID) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// seed inserts r directly into the store, bypassing Service
// validation — the seeded-row and cross-org tests need a row already in
// place with fields Create's own validation could never produce (a "seed"
// origin, a fixed id).
func (f *fakeStore) seed(r rules.Rule) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[r.ID] = r
}

func fixedClock(t time.Time) rules.Option {
	return rules.WithClock(func() time.Time { return t })
}

// hostForm returns a valid RuleInput bound to a host, judging cpu.used_pct
// > 90 in form mode — the baseline every validation test mutates one field
// of.
func hostForm(hostID uuid.UUID) rules.RuleInput {
	return rules.RuleInput{
		Name: "high cpu", Enabled: true, Scope: rules.ScopeHost, HostID: &hostID,
		Metric: "cpu.used_pct", Severity: rules.SeverityWarn, Mode: "form",
		Form: &rules.Form{Metric: "cpu.used_pct", Op: ">", Value: 90},
		For:  5 * time.Minute,
	}
}

func TestCreateValidation(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	group := uuid.Must(uuid.NewV7())
	org := uuid.Must(uuid.NewV7())

	tests := []struct {
		name string
		in   rules.RuleInput
	}{
		{"empty name", func() rules.RuleInput { in := hostForm(host); in.Name = "   "; return in }()},
		{"name too long", func() rules.RuleInput {
			in := hostForm(host)
			long := make([]byte, 101)
			for i := range long {
				long[i] = 'a'
			}
			in.Name = string(long)
			return in
		}()},
		{"name with control character", func() rules.RuleInput { in := hostForm(host); in.Name = "bad\x00name"; return in }()},
		{"negative for_duration", func() rules.RuleInput { in := hostForm(host); in.For = -time.Second; return in }()},
		{"for_duration over 24h", func() rules.RuleInput { in := hostForm(host); in.For = 25 * time.Hour; return in }()},
		{"unknown severity", func() rules.RuleInput { in := hostForm(host); in.Severity = "urgent"; return in }()},
		{"unknown metric", func() rules.RuleInput {
			in := hostForm(host)
			in.Metric = "cpu.nonsense"
			in.Form.Metric = "cpu.nonsense"
			return in
		}()},
		{"unknown scope", func() rules.RuleInput { in := hostForm(host); in.Scope = "planet"; return in }()},
		{"org scope with host set", func() rules.RuleInput {
			in := hostForm(host)
			in.Scope = rules.ScopeOrg
			return in
		}()},
		{"org scope with item set", func() rules.RuleInput {
			in := hostForm(host)
			in.Scope, in.HostID, in.Item = rules.ScopeOrg, nil, "sda1"
			return in
		}()},
		{"group scope without group id", func() rules.RuleInput {
			in := hostForm(host)
			in.Scope, in.HostID = rules.ScopeGroup, nil
			return in
		}()},
		{"group scope with host also set", func() rules.RuleInput {
			in := hostForm(host)
			in.Scope, in.GroupID = rules.ScopeGroup, &group
			return in
		}()},
		{"host scope without host id", func() rules.RuleInput { in := hostForm(host); in.HostID = nil; return in }()},
		{"host scope with item set", func() rules.RuleInput { in := hostForm(host); in.Item = "sda1"; return in }()},
		{"item scope without host id", func() rules.RuleInput {
			in := hostForm(host)
			in.Scope, in.HostID, in.Item = rules.ScopeItem, nil, "sda1"
			return in
		}()},
		{"item scope with empty item", func() rules.RuleInput { in := hostForm(host); in.Scope = rules.ScopeItem; return in }()},
		{"unknown mode", func() rules.RuleInput { in := hostForm(host); in.Mode = "yaml"; return in }()},
		{"form mode without a form", func() rules.RuleInput { in := hostForm(host); in.Form = nil; return in }()},
		{"form mode with an expression too", func() rules.RuleInput {
			in := hostForm(host)
			in.Expression = "cpu.used_pct > 90"
			return in
		}()},
		{"form metric mismatches rule metric", func() rules.RuleInput {
			in := hostForm(host)
			in.Form.Metric = "mem.used_pct"
			return in
		}()},
		{"form operator does not fit metric kind", func() rules.RuleInput {
			in := hostForm(host)
			in.Form.Op = "in"
			return in
		}()},
		{"expression mode without an expression", func() rules.RuleInput {
			in := hostForm(host)
			in.Mode, in.Form = "expression", nil
			return in
		}()},
		{"expression mode with a form too", func() rules.RuleInput {
			in := hostForm(host)
			in.Mode, in.Expression = "expression", "cpu.used_pct > 90"
			return in
		}()},
		{"expression: syntax error", func() rules.RuleInput {
			in := hostForm(host)
			in.Mode, in.Form, in.Expression = "expression", nil, "cpu.used_pct >"
			return in
		}()},
		{"expression: unknown metric (type error)", func() rules.RuleInput {
			in := hostForm(host)
			in.Mode, in.Form, in.Expression = "expression", nil, "cpu.nonsense > 90"
			return in
		}()},
		{"expression: window longer than 24h", func() rules.RuleInput {
			in := hostForm(host)
			in.Mode, in.Form, in.Expression = "expression", nil, "avg(cpu.used_pct, 25h) > 90"
			return in
		}()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := rules.NewService(newFakeStore())
			_, err := svc.Create(ctx, org, tt.in)
			if !errors.Is(err, rules.ErrInvalidInput) {
				t.Fatalf("Create(%+v) error = %v, want ErrInvalidInput", tt.in, err)
			}
			// The two sentinels ErrInvalidRule wraps must never leak past
			// this service's own surface (task's own error-surface contract).
			if errors.Is(err, rules.ErrInvalidRule) {
				t.Fatalf("Create error leaks ErrInvalidRule: %v", err)
			}
			var pe *rules.ParseError
			if errors.As(err, &pe) {
				t.Fatalf("Create error leaks *ParseError: %v", err)
			}
			var tce *rules.TypeCheckError
			if errors.As(err, &tce) {
				t.Fatalf("Create error leaks *TypeCheckError: %v", err)
			}
		})
	}
}

// TestCreateValidExpression pins the counterpart of the validation table:
// a syntactically and semantically valid expression is accepted, its
// Source is stored verbatim (the controller's own decision: an
// expression-mode rule's Source is what the operator typed, not a
// Format-rendered re-summary), CheckKey is derived from the metric via the
// catalogue, and a generated id/org/origin/timestamps come from the
// service, never the caller.
func TestCreateValidExpression(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	org := uuid.Must(uuid.NewV7())
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	store := newFakeStore()
	svc := rules.NewService(store, fixedClock(now))

	in := rules.RuleInput{
		Name: "high cpu", Enabled: true, Scope: rules.ScopeHost, HostID: &host,
		Metric: "cpu.used_pct", Severity: rules.SeverityCrit, Mode: "expression",
		Expression: "cpu.used_pct   >   95", // deliberately odd spacing: Source keeps it
		For:        time.Minute,
	}
	r, err := svc.Create(ctx, org, in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != in.Expression {
		t.Fatalf("Source = %q, want the operator's own text %q", r.Source, in.Expression)
	}
	if r.CheckKey != "cpu" {
		t.Fatalf("CheckKey = %q, want %q (derived from the catalogue)", r.CheckKey, "cpu")
	}
	if r.Origin != "user" {
		t.Fatalf("Origin = %q, want %q", r.Origin, "user")
	}
	if r.ID == uuid.Nil || r.OrgID != org {
		t.Fatalf("ID/OrgID = %v/%v", r.ID, r.OrgID)
	}
	if !r.CreatedAt.Equal(now) || !r.UpdatedAt.Equal(now) {
		t.Fatalf("timestamps = %v/%v, want both %v", r.CreatedAt, r.UpdatedAt, now)
	}
	if r.Trigger.Kind != rules.KindCompare {
		t.Fatalf("Trigger = %+v, want a parsed compare node", r.Trigger)
	}
	if r.Clear.Kind != "" {
		t.Fatalf("Clear = %+v, want the zero Node (expression mode never sets one)", r.Clear)
	}

	// The exact same rule persisted in the fake store.
	got, err := store.Get(ctx, org, r.ID)
	if err != nil || got.Source != in.Expression {
		t.Fatalf("stored rule = %+v, %v", got, err)
	}
}

// TestCreateValidFormMode pins the form-mode half of the Source decision:
// Source is a Format-rendered summary of the compiled trigger, never
// anything resembling raw user text (there is none to store).
func TestCreateValidFormMode(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	org := uuid.Must(uuid.NewV7())
	svc := rules.NewService(newFakeStore())

	r, err := svc.Create(ctx, org, hostForm(host))
	if err != nil {
		t.Fatal(err)
	}
	want := rules.Format(r.Trigger)
	if r.Source != want {
		t.Fatalf("Source = %q, want Format(trigger) = %q", r.Source, want)
	}
	if r.Source != "cpu.used_pct > 90" {
		t.Fatalf("Source = %q, want the rendered comparison", r.Source)
	}
}

// TestCreateFormWithClearCondition exercises the one path Compile takes
// through a two-sided Form (trigger and clear), confirming both trees are
// stored.
func TestCreateFormWithClearCondition(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	org := uuid.Must(uuid.NewV7())
	svc := rules.NewService(newFakeStore())

	in := hostForm(host)
	in.Form.ClearOp, in.Form.ClearValue = "<", 70
	r, err := svc.Create(ctx, org, in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Clear.Kind != rules.KindCompare {
		t.Fatalf("Clear = %+v, want a compiled clear compare node", r.Clear)
	}
}

// TestUpdateSeededRuleImmutability pins the controller's own contract:
// metric, scope and severity are fixed on a seeded rule (copy it instead),
// while thresholds (the compiled tree via Form/Expression), duration and
// enabled all remain ordinary mutable columns.
func TestUpdateSeededRuleImmutability(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	org := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := rules.NewService(store)

	seeded := rules.Rule{
		ID: id, OrgID: org, Name: "seed cpu", Enabled: true, Origin: "seed",
		Scope: rules.ScopeHost, HostID: &host, CheckKey: "cpu", Metric: "cpu.used_pct",
		Severity: rules.SeverityWarn, Mode: "form", Source: "cpu.used_pct > 80",
		Trigger: rules.Node{Kind: rules.KindCompare, Op: ">", Args: []rules.Node{
			{Kind: rules.KindMetric, Name: "cpu.used_pct"}, {Kind: rules.KindNumber, Num: 80},
		}},
		For: time.Minute, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	store.seed(seeded)

	// Allowed: threshold (via a new form value), duration and enabled all
	// change freely on a seeded row.
	allowed := hostForm(host)
	allowed.Form.Value = 85
	allowed.For = 10 * time.Minute
	allowed.Enabled = false
	updated, err := svc.Update(ctx, org, id, allowed)
	if err != nil {
		t.Fatalf("changing threshold/duration/enabled on a seeded rule: %v", err)
	}
	if updated.For != 10*time.Minute || updated.Enabled {
		t.Fatalf("allowed update did not apply: %+v", updated)
	}

	// Refused: metric.
	refuseMetric := hostForm(host)
	refuseMetric.Metric, refuseMetric.Form.Metric = "mem.used_pct", "mem.used_pct"
	if _, err := svc.Update(ctx, org, id, refuseMetric); !errors.Is(err, rules.ErrInvalidInput) {
		t.Fatalf("changing a seeded rule's metric = %v, want ErrInvalidInput", err)
	}

	// Refused: scope.
	refuseScope := hostForm(host)
	refuseScope.Scope, refuseScope.HostID, refuseScope.GroupID = rules.ScopeOrg, nil, nil
	if _, err := svc.Update(ctx, org, id, refuseScope); !errors.Is(err, rules.ErrInvalidInput) {
		t.Fatalf("changing a seeded rule's scope = %v, want ErrInvalidInput", err)
	}

	// Refused: severity.
	refuseSeverity := hostForm(host)
	refuseSeverity.Severity = rules.SeverityCrit
	if _, err := svc.Update(ctx, org, id, refuseSeverity); !errors.Is(err, rules.ErrInvalidInput) {
		t.Fatalf("changing a seeded rule's severity = %v, want ErrInvalidInput", err)
	}
}

// TestUpdateUserRuleAnyFieldChanges is the counterpart: an operator-created
// ("user") rule has no such restriction.
func TestUpdateUserRuleAnyFieldChanges(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	org := uuid.Must(uuid.NewV7())
	svc := rules.NewService(newFakeStore())

	created, err := svc.Create(ctx, org, hostForm(host))
	if err != nil {
		t.Fatal(err)
	}
	changed := hostForm(host)
	changed.Metric, changed.Form.Metric, changed.Severity = "mem.used_pct", "mem.used_pct", rules.SeverityCrit
	updated, err := svc.Update(ctx, org, created.ID, changed)
	if err != nil {
		t.Fatalf("a user rule's metric and severity must be changeable: %v", err)
	}
	if updated.Metric != "mem.used_pct" || updated.Severity != rules.SeverityCrit {
		t.Fatalf("update did not apply: %+v", updated)
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("CreatedAt = %v, want unchanged %v", updated.CreatedAt, created.CreatedAt)
	}
}

// TestCrossOrgNotFound pins that Get, Update and Delete all treat a rule
// belonging to another organization exactly like one that does not exist.
func TestCrossOrgNotFound(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	orgA := uuid.Must(uuid.NewV7())
	orgB := uuid.Must(uuid.NewV7())
	svc := rules.NewService(newFakeStore())

	created, err := svc.Create(ctx, orgA, hostForm(host))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, orgB, created.ID); !errors.Is(err, rules.ErrNotFound) {
		t.Fatalf("cross-org Get = %v, want ErrNotFound", err)
	}
	if _, err := svc.Update(ctx, orgB, created.ID, hostForm(host)); !errors.Is(err, rules.ErrNotFound) {
		t.Fatalf("cross-org Update = %v, want ErrNotFound", err)
	}
	if err := svc.Delete(ctx, orgB, created.ID); !errors.Is(err, rules.ErrNotFound) {
		t.Fatalf("cross-org Delete = %v, want ErrNotFound", err)
	}
	// orgA's own view is untouched by orgB's attempts.
	if _, err := svc.Get(ctx, orgA, created.ID); err != nil {
		t.Fatalf("orgA's rule must survive orgB's attempts: %v", err)
	}
}

// TestCheckValid pins Check's happy path: OK, the rendered Source, and
// FormReady true for a rule built from a Form (Compile's output always
// round-trips through AsForm — see form_test.go's own coverage of that).
func TestCheckValid(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	org := uuid.Must(uuid.NewV7())
	svc := rules.NewService(newFakeStore())

	checked, err := svc.Check(ctx, org, hostForm(host))
	if err != nil {
		t.Fatal(err)
	}
	if !checked.OK || !checked.FormReady {
		t.Fatalf("checked = %+v, want OK and FormReady", checked)
	}
	if checked.Source != "cpu.used_pct > 90" {
		t.Fatalf("Source = %q", checked.Source)
	}
	if checked.Pos != 0 || checked.Message != "" {
		t.Fatalf("a valid input must carry no position or message: %+v", checked)
	}
}

// TestCheckExpressionOutsideFormSubset pins FormReady = false for a
// perfectly valid expression that nonetheless cannot be represented in the
// form editor (an "and" of two comparisons) — the data behind the "switch
// to expression mode is one-way" warning.
func TestCheckExpressionOutsideFormSubset(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	org := uuid.Must(uuid.NewV7())
	svc := rules.NewService(newFakeStore())

	in := hostForm(host)
	in.Mode, in.Form = "expression", nil
	in.Expression = "cpu.used_pct > 90 and cpu.load1 > 4"
	checked, err := svc.Check(ctx, org, in)
	if err != nil {
		t.Fatal(err)
	}
	if !checked.OK {
		t.Fatalf("checked = %+v, want OK (the expression is valid)", checked)
	}
	if checked.FormReady {
		t.Fatalf("checked = %+v, want FormReady = false: an \"and\" does not fit the form subset", checked)
	}
}

// TestCheckInvalidCarriesPosition pins the reason Check exists as its own
// method rather than just "Create and see if it errors": the editor needs
// the byte offset of a parse failure to place the caret, and Check must
// hand it back through Checked.Pos, not just inside an opaque error string.
func TestCheckInvalidCarriesPosition(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	org := uuid.Must(uuid.NewV7())
	svc := rules.NewService(newFakeStore())

	in := hostForm(host)
	in.Mode, in.Form = "expression", nil
	const expr = "cpu.used_pct >"
	in.Expression = expr

	wantPos, wantMsg := 0, ""
	if _, perr := rules.Parse(expr); perr != nil {
		var pe *rules.ParseError
		if errors.As(perr, &pe) {
			wantPos, wantMsg = pe.Pos, pe.Msg
		} else {
			t.Fatalf("Parse(%q) error is not a *ParseError: %v", expr, perr)
		}
	} else {
		t.Fatalf("Parse(%q) unexpectedly succeeded; fixture is wrong", expr)
	}

	checked, err := svc.Check(ctx, org, in)
	if !errors.Is(err, rules.ErrInvalidInput) {
		t.Fatalf("Check error = %v, want ErrInvalidInput", err)
	}
	if errors.Is(err, rules.ErrInvalidRule) {
		t.Fatalf("Check error leaks ErrInvalidRule: %v", err)
	}
	if checked.OK {
		t.Fatalf("checked = %+v, want OK = false", checked)
	}
	if checked.Pos != wantPos || checked.Message != wantMsg {
		t.Fatalf("checked = %+v, want Pos=%d Message=%q", checked, wantPos, wantMsg)
	}
}

// errReader always fails: newID's entropy-failure panic (mirrored on
// uuid.Must elsewhere in the codebase, and on checks.Service's own newID)
// is otherwise never exercised.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestCreatePanicsWhenEntropyFails(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	svc := rules.NewService(newFakeStore(), rules.WithRand(errReader{}))
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic when the entropy source fails")
		}
	}()
	_, _ = svc.Create(ctx, uuid.Must(uuid.NewV7()), hostForm(host))
}

// TestListFilters exercises Filter's three independent axes against the
// fake store.
func TestListFilters(t *testing.T) {
	hostA := uuid.Must(uuid.NewV7())
	hostB := uuid.Must(uuid.NewV7())
	org := uuid.Must(uuid.NewV7())
	svc := rules.NewService(newFakeStore())

	cpuRule, err := svc.Create(ctx, org, hostForm(hostA))
	if err != nil {
		t.Fatal(err)
	}
	memIn := hostForm(hostB)
	memIn.Metric, memIn.Form.Metric = "mem.used_pct", "mem.used_pct"
	memRule, err := svc.Create(ctx, org, memIn)
	if err != nil {
		t.Fatal(err)
	}

	byCheckKey, err := svc.List(ctx, org, rules.Filter{CheckKey: "cpu"})
	if err != nil || len(byCheckKey) != 1 || byCheckKey[0].ID != cpuRule.ID {
		t.Fatalf("filter by check_key = %+v, %v", byCheckKey, err)
	}
	byHost, err := svc.List(ctx, org, rules.Filter{HostID: &hostB})
	if err != nil || len(byHost) != 1 || byHost[0].ID != memRule.ID {
		t.Fatalf("filter by host = %+v, %v", byHost, err)
	}
	byScope, err := svc.List(ctx, org, rules.Filter{Scope: rules.ScopeHost})
	if err != nil || len(byScope) != 2 {
		t.Fatalf("filter by scope = %+v, %v", byScope, err)
	}
}
