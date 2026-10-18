// The alert API's tests (v0.4a Task 8): the six routes
// (registerAlertRoutes, alert_handlers.go) driven through the full API
// against the test database, on a lightweight fixture of its own —
// alertFixture below, not the heavier runFixture job_handlers_test.go
// builds (playbooks, templates, credentials none of this needs).
//
// Opening an alert is not exposed on alerts.Service at all — only Apply,
// driven by a rules evaluation, does that (service.go's own doc comment).
// seedOpenAlert instead drives alerts.NewSQLStore(s.DB).Execute directly
// on a bare tx, the identical shape internal/alerts' own
// store_sql_test.go uses to build its fixtures.
package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alertroutes"
	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/rules"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type alertFixture struct {
	s         *apitest.Server
	orgID     uuid.UUID
	orgSlug   string
	operatorC *http.Client
	viewerC   *http.Client
	hostA     uuid.UUID
	hostB     uuid.UUID
}

func newAlertFixture(t *testing.T, slug string) alertFixture {
	t.Helper()
	s := apitest.New(t)
	owner := s.SeedUser(slug+"-owner@example.test", "correct horse battery")
	operator := s.SeedUser(slug+"-operator@example.test", "correct horse battery")
	viewer := s.SeedUser(slug+"-viewer@example.test", "correct horse battery")
	org := s.SeedOrg(slug, owner)
	s.AddMember(org, operator, tenancy.RoleOperator)
	s.AddMember(org, viewer, tenancy.RoleViewer)
	scoped := db.WithOrgScope(context.Background(), org)
	zones, err := s.Deps.Hosts.ListZones(scoped, org)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}
	hostA, err := s.Deps.Hosts.CreateHost(scoped, org, hosts.HostInput{Name: "web-1", Address: "10.0.0.1", ZoneID: zones[0].ID, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	hostB, err := s.Deps.Hosts.CreateHost(scoped, org, hosts.HostInput{Name: "web-2", Address: "10.0.0.2", ZoneID: zones[0].ID, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	return alertFixture{
		s: s, orgID: org, orgSlug: slug,
		operatorC: s.LoginAs(operator), viewerC: s.LoginAs(viewer),
		hostA: hostA.ID, hostB: hostB.ID,
	}
}

func (f alertFixture) alertsURL(suffix string) string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/alerts" + suffix
}

// tupleSubject is a plain tuple-scope alerts.Subject on host, the shape
// every seedOpenAlert call below opens.
func tupleSubject(host uuid.UUID, checkKey, item, metric string) alerts.Subject {
	return alerts.Subject{HostID: host, Scope: alerts.ScopeTuple, CheckKey: checkKey, Item: item, Metric: metric}
}

// seedOpenAlert opens one alert directly through the store, on orgID/host,
// and returns its id. It does not seed check_states — InsertAlert's own
// was_flapping subquery tolerates no match (store_sql_test.go's own
// TestExecuteOpenHostScopeIgnoresTupleCheckState) — nothing here asserts
// on WasFlapping.
func (f alertFixture) seedOpenAlert(t *testing.T, host uuid.UUID, sub alerts.Subject, severity rules.State, openedAt time.Time) uuid.UUID {
	t.Helper()
	st := alerts.NewSQLStore(f.s.DB)
	ctx := context.Background()
	tx, err := f.s.DB.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := st.Execute(ctx, tx, f.orgID, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: openedAt, Severity: severity},
	}); err != nil {
		t.Fatalf("seed open alert: %v", err)
	}
	open, err := st.OpenForHost(ctx, tx, f.orgID, host)
	if err != nil {
		t.Fatalf("open for host: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	for _, a := range open {
		if a.Subject == sub {
			return a.ID
		}
	}
	t.Fatalf("no seeded alert found for subject %+v among %+v", sub, open)
	return uuid.Nil
}

// seedClosedAlert opens then immediately closes one alert, for the tests
// that need a closed row to filter against or to acknowledge-and-expect-422.
func (f alertFixture) seedClosedAlert(t *testing.T, host uuid.UUID, sub alerts.Subject, severity rules.State, at time.Time) uuid.UUID {
	t.Helper()
	id := f.seedOpenAlert(t, host, sub, severity, at)
	st := alerts.NewSQLStore(f.s.DB)
	ctx := context.Background()
	tx, err := f.s.DB.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := st.Execute(ctx, tx, f.orgID, []alerts.Action{
		{Kind: alerts.ActionClose, AlertID: id, At: at.Add(time.Minute), Reason: alerts.Resolved},
	}); err != nil {
		t.Fatalf("seed close alert: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return id
}

// seedEscalatedEvent writes one 'escalated' alert_events row directly
// through alertroutes.Store.RecordEscalation — the only writer that
// exists (escalatePass, the follower's second pass, v0.4d Task 6) — so
// this fixture needs neither a real route nor a real follower tick to
// prove the API round-trips the payload RecordEscalation writes.
func (f alertFixture) seedEscalatedEvent(t *testing.T, alertID uuid.UUID, position int, channels []uuid.UUID, at time.Time) {
	t.Helper()
	st := alertroutes.NewSQLStore(f.s.DB)
	ctx := context.Background()
	tx, err := f.s.DB.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := st.RecordEscalation(ctx, tx, f.orgID, alertID, position, channels, at); err != nil {
		t.Fatalf("seed escalated event: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

type alertOut struct {
	ID                string     `json:"id"`
	HostID            string     `json:"host_id"`
	HostName          string     `json:"host_name"`
	Scope             string     `json:"scope"`
	CheckKey          string     `json:"check_key"`
	Item              string     `json:"item"`
	Metric            string     `json:"metric"`
	OpenedAt          time.Time  `json:"opened_at"`
	Severity          string     `json:"severity"`
	Peak              string     `json:"peak_severity"`
	ClosedAt          *time.Time `json:"closed_at"`
	ClosedReason      string     `json:"closed_reason"`
	AckAt             *time.Time `json:"ack_at"`
	AckBy             string     `json:"ack_by"`
	AckSeverity       string     `json:"ack_severity"`
	AckNote           string     `json:"ack_note"`
	SnoozedUntil      *time.Time `json:"snoozed_until"`
	SnoozeUntilChange bool       `json:"snooze_until_change"`
	Muted             bool       `json:"muted"`
	MuteReason        string     `json:"mute_reason"`
	MutedUntil        *time.Time `json:"muted_until"`
}

type alertListOut struct {
	Alerts     []alertOut `json:"alerts"`
	NextCursor string     `json:"next_cursor"`
}

type alertTimelineEntryOut struct {
	At    time.Time `json:"at"`
	Kind  string    `json:"kind"`
	Event *struct {
		ID                   string   `json:"id"`
		Kind                 string   `json:"kind"`
		EscalationPosition   int      `json:"escalation_position"`
		EscalationChannelIDs []string `json:"escalation_channel_ids"`
	} `json:"event"`
	Change *struct {
		FromState string `json:"from_state"`
		ToState   string `json:"to_state"`
	} `json:"change"`
}

type alertDetailOut struct {
	alertOut
	Timeline []alertTimelineEntryOut `json:"timeline"`
}

func (f alertFixture) getAlert(t *testing.T, c *http.Client, id uuid.UUID) (*http.Response, alertDetailOut) {
	t.Helper()
	resp := requestJSON(t, c, http.MethodGet, f.alertsURL("/"+id.String()), nil)
	var out struct {
		Alert alertDetailOut `json:"alert"`
	}
	if resp.StatusCode == http.StatusOK {
		decodeInto(t, resp, &out)
	}
	return resp, out.Alert
}

// TestAlertListAndGet is the happy path: list returns the seeded alert
// with its fields pinned, and get returns the same alert plus a one-entry
// timeline (its own "opened" event) — both unmuted, since nothing covers
// either host in maintenance.
func TestAlertListAndGet(t *testing.T) {
	f := newAlertFixture(t, "alert-get")
	opened := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	id := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", "used_percent"), rules.StateWarn, opened)

	resp := requestJSON(t, f.viewerC, http.MethodGet, f.alertsURL(""), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var list alertListOut
	decodeInto(t, resp, &list)
	if len(list.Alerts) != 1 {
		t.Fatalf("list = %+v, want exactly one alert", list.Alerts)
	}
	got := list.Alerts[0]
	if got.ID != id.String() || got.HostID != f.hostA.String() || got.HostName != "web-1" || got.Scope != "tuple" ||
		got.CheckKey != "cpu" || got.Metric != "used_percent" || got.Severity != "warn" || got.Peak != "warn" {
		t.Fatalf("list[0] = %+v, want the seeded alert's own fields", got)
	}
	if got.ClosedAt != nil || got.AckAt != nil || got.SnoozedUntil != nil || got.Muted {
		t.Fatalf("list[0] = %+v, want open/unacknowledged/unsnoozed/unmuted", got)
	}
	if !got.OpenedAt.Equal(opened) {
		t.Fatalf("opened_at = %v, want %v (state.since, not now)", got.OpenedAt, opened)
	}

	getResp, detail := f.getAlert(t, f.viewerC, id)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("get = %d %s", getResp.StatusCode, mustReadBody(t, getResp))
	}
	if detail.ID != id.String() || detail.HostName != "web-1" {
		t.Fatalf("get = %+v, want id %q and host_name \"web-1\"", detail, id.String())
	}
	if len(detail.Timeline) != 1 || detail.Timeline[0].Kind != "event" || detail.Timeline[0].Event == nil || detail.Timeline[0].Event.Kind != "opened" {
		t.Fatalf("timeline = %+v, want exactly one 'opened' event", detail.Timeline)
	}
}

// TestAlertTimelineCarriesAnEscalatedEventsStepAndChannels proves the
// 'escalated' event (alerts.EventEscalated, v0.4d) round-trips its own
// payload through GET instead of reading as an event with nothing in it:
// before this test, alerts.Event never surfaced its payload column at all
// (alert_handlers.go's own doc comment on alertEventDTO, pre-v0.4d Task 8)
// — the step number that fired and the channels it reached.
func TestAlertTimelineCarriesAnEscalatedEventsStepAndChannels(t *testing.T) {
	f := newAlertFixture(t, "alert-escalated")
	opened := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	id := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", "used_percent"), rules.StateCrit, opened)
	ch := uuid.New()
	f.seedEscalatedEvent(t, id, 2, []uuid.UUID{ch}, opened.Add(10*time.Minute))

	_, detail := f.getAlert(t, f.operatorC, id)
	var found *struct {
		ID                   string   `json:"id"`
		Kind                 string   `json:"kind"`
		EscalationPosition   int      `json:"escalation_position"`
		EscalationChannelIDs []string `json:"escalation_channel_ids"`
	}
	for _, e := range detail.Timeline {
		if e.Kind == "event" && e.Event != nil && e.Event.Kind == "escalated" {
			found = e.Event
		}
	}
	if found == nil {
		t.Fatalf("timeline = %+v, want an 'escalated' event", detail.Timeline)
	}
	if found.EscalationPosition != 2 {
		t.Fatalf("escalation_position = %d, want 2", found.EscalationPosition)
	}
	if len(found.EscalationChannelIDs) != 1 || found.EscalationChannelIDs[0] != ch.String() {
		t.Fatalf("escalation_channel_ids = %+v, want [%s]", found.EscalationChannelIDs, ch)
	}
}

// TestEscalationChannelIDsDistinguishesReachedNobodyFromNotEscalated proves
// the one thing EscalationChannelIDs' own doc comment (alert_handlers.go)
// exists to guarantee: `[]` and absent/`null` are different facts on the
// wire, not two spellings of the same one. Deliberately asserts on the raw
// JSON bytes rather than decoding into a Go []string first — decoding
// would itself erase the distinction being tested (Go's json package maps
// both `null` and a missing key to a nil slice, so a assertion against the
// decoded value could pass by accident even if omitempty had been left on).
func TestEscalationChannelIDsDistinguishesReachedNobodyFromNotEscalated(t *testing.T) {
	f := newAlertFixture(t, "alert-escalated-nobody")
	opened := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	id := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", "used_percent"), rules.StateCrit, opened)
	// Empty, not nil: RecordEscalation's own contract for "this rung
	// reached nobody" (escalate_sql.go's doc comment) is an explicit empty
	// list, never a nil one.
	f.seedEscalatedEvent(t, id, 1, []uuid.UUID{}, opened.Add(5*time.Minute))

	resp := requestJSON(t, f.operatorC, http.MethodGet, f.alertsURL("/"+id.String()), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		Alert struct {
			Timeline []struct {
				Event *struct {
					Kind                 string          `json:"kind"`
					EscalationChannelIDs json.RawMessage `json:"escalation_channel_ids"`
				} `json:"event"`
			} `json:"timeline"`
		} `json:"alert"`
	}
	if err := json.Unmarshal([]byte(mustReadBody(t, resp)), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var openedRaw, escalatedRaw json.RawMessage
	for _, e := range out.Alert.Timeline {
		if e.Event == nil {
			continue
		}
		switch e.Event.Kind {
		case "opened":
			openedRaw = e.Event.EscalationChannelIDs
		case "escalated":
			escalatedRaw = e.Event.EscalationChannelIDs
		}
	}
	if string(openedRaw) != "null" {
		t.Fatalf("opened event's escalation_channel_ids = %s, want the literal null (not escalated, nothing to decode)", openedRaw)
	}
	if string(escalatedRaw) != "[]" {
		t.Fatalf("escalated event's escalation_channel_ids = %s, want the literal [] (reached nobody, not merely absent)", escalatedRaw)
	}
}

// TestAlertListNamesEachAlertsOwnHost is the review's own discriminating
// case at the HTTP layer, mirroring internal/alerts' own
// TestGetAlertAndListAlertsNameEachAlertsOwnHost one layer up: a test
// merely asserting host_name is non-empty would still pass against a join
// that returned the wrong row (e.g. one host's name leaking onto every
// alert in the organization). hostA is "web-1", hostB is "web-2"
// (newAlertFixture) — two real, differently-named hosts in the same
// organization, so a swapped or constant join answer shows up as a wrong
// string, not merely a missing one.
func TestAlertListNamesEachAlertsOwnHost(t *testing.T) {
	f := newAlertFixture(t, "alert-host-names")
	now := time.Now().UTC()
	idA := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", "used_percent"), rules.StateWarn, now)
	idB := f.seedOpenAlert(t, f.hostB, tupleSubject(f.hostB, "disk", "", "used_percent"), rules.StateCrit, now)

	resp := requestJSON(t, f.viewerC, http.MethodGet, f.alertsURL(""), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var list alertListOut
	decodeInto(t, resp, &list)
	if len(list.Alerts) != 2 {
		t.Fatalf("list = %+v, want exactly 2 alerts", list.Alerts)
	}
	gotNames := map[string]string{}
	for _, a := range list.Alerts {
		gotNames[a.ID] = a.HostName
	}
	if gotNames[idA.String()] != "web-1" {
		t.Fatalf("alert on hostA named %q, want \"web-1\" (got the wrong host's name)", gotNames[idA.String()])
	}
	if gotNames[idB.String()] != "web-2" {
		t.Fatalf("alert on hostB named %q, want \"web-2\" (got the wrong host's name)", gotNames[idB.String()])
	}
}

// TestAlertGetNotFound covers alerts.ErrNotFound -> 404.
func TestAlertGetNotFound(t *testing.T) {
	f := newAlertFixture(t, "alert-404")
	resp := requestJSON(t, f.viewerC, http.MethodGet, f.alertsURL("/"+uuid.New().String()), nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get unknown id = %d, want 404", resp.StatusCode)
	}
}

// TestAlertDetailReportsAHeldNotification is v0.4c Task 11's own test: an
// alert whose "opened" event the ledger is holding 'deferred' (the route
// wanting it is outside its delivery window) must say so on its own
// detail — "why did nothing arrive" has to be a fact on the page an
// operator is already looking at, not a SQL query only an engineer can
// run. Uses apitest.Server's own bearer-token helpers (OrgWithToken, Get)
// rather than alertFixture above: SeedOpenAlertWithDeferredNotification
// lives on the server, the same shape SeedChannel/SeedHost already take.
func TestAlertDetailReportsAHeldNotification(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	alert := s.SeedOpenAlertWithDeferredNotification(t, org)

	resp := s.Get(t, tok, "/api/v1/orgs/"+org.String()+"/alerts/"+alert.String())
	if resp.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200: %s", resp.Code, resp.Body)
	}
	if !strings.Contains(resp.Body.String(), "notification_hold") {
		t.Errorf("the detail does not say the notification is held: %s", resp.Body)
	}
	if !strings.Contains(resp.Body.String(), `"notification_hold":"deferred"`) {
		t.Errorf(`the detail's own notification_hold is not "deferred": %s`, resp.Body)
	}
}

// TestAlertsAreScopedToTheOrganization is the brief's own test: org B's
// token (here, a session in org B — this package tests the same
// org-scoping requireOrg gives every other resource) gets 404, not 403,
// for org A's alert id. A 403 would have confirmed the id exists in some
// org the caller cannot see; 404 is the uniform "nothing here" every other
// cross-org test in this package (e.g. TestDeleteHostScopedToOrg) asserts.
func TestAlertsAreScopedToTheOrganization(t *testing.T) {
	a := newAlertFixture(t, "alert-scope-a")
	id := a.seedOpenAlert(t, a.hostA, tupleSubject(a.hostA, "cpu", "", "used_percent"), rules.StateCrit, time.Now().UTC())

	b := newAlertFixture(t, "alert-scope-b")
	resp := requestJSON(t, b.operatorC, http.MethodGet, b.alertsURL("/"+id.String()), nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("org B reading org A's alert = %d, want 404 (not 403 — it must not confirm the id exists elsewhere)", resp.StatusCode)
	}
}

// TestAcknowledgeRequiresTheMonitoringWritePermission is the brief's own
// test: a read-only member gets 403 acknowledging, the same
// checks.write bar every other monitoring write in this package uses
// (design doc: "v0.4a introduces no new [permission]").
func TestAcknowledgeRequiresTheMonitoringWritePermission(t *testing.T) {
	f := newAlertFixture(t, "alert-ack-perm")
	id := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", "used_percent"), rules.StateWarn, time.Now().UTC())

	resp := postJSON(t, f.viewerC, f.alertsURL("/"+id.String()+"/acknowledge"), map[string]any{"note": "looking into it"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer acknowledge = %d, want 403", resp.StatusCode)
	}
	// An operator may, proving the 403 above was the permission check and
	// not some other failure the viewer and operator would share equally.
	resp = postJSON(t, f.operatorC, f.alertsURL("/"+id.String()+"/acknowledge"), map[string]any{"note": "looking into it"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("operator acknowledge = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

// TestAcknowledgeAndUnacknowledge drives the full round trip: acknowledge
// stamps ack_at/ack_by/ack_severity/ack_note (acknowledged_severity is the
// row's own current severity, service.go's own decision 2), and
// unacknowledge clears all four.
func TestAcknowledgeAndUnacknowledge(t *testing.T) {
	f := newAlertFixture(t, "alert-ack")
	id := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "disk", "/", "used_percent"), rules.StateCrit, time.Now().UTC())

	resp := postJSON(t, f.operatorC, f.alertsURL("/"+id.String()+"/acknowledge"), map[string]any{"note": "paging the owner"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("acknowledge = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	_, detail := f.getAlert(t, f.operatorC, id)
	if detail.AckAt == nil || detail.AckBy == "" || detail.AckSeverity != "crit" || detail.AckNote != "paging the owner" {
		t.Fatalf("after acknowledge = %+v, want ack_at/ack_by/ack_severity=crit/ack_note set", detail)
	}

	resp = requestJSON(t, f.operatorC, http.MethodDelete, f.alertsURL("/"+id.String()+"/acknowledge"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unacknowledge = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	_, detail = f.getAlert(t, f.operatorC, id)
	if detail.AckAt != nil || detail.AckBy != "" || detail.AckSeverity != "" || detail.AckNote != "" {
		t.Fatalf("after unacknowledge = %+v, want every ack field cleared", detail)
	}
}

// TestAcknowledgeClosedAlertRejected covers the other ErrInvalidInput ->
// 422 path besides snooze's own (mapAlertsErr is one function for both):
// service.go's Acknowledge refuses a closed alert outright.
func TestAcknowledgeClosedAlertRejected(t *testing.T) {
	f := newAlertFixture(t, "alert-ack-closed")
	id := f.seedClosedAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", "used_percent"), rules.StateWarn, time.Now().UTC().Add(-time.Hour))

	resp := postJSON(t, f.operatorC, f.alertsURL("/"+id.String()+"/acknowledge"), map[string]any{})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("acknowledge a closed alert = %d, want 422", resp.StatusCode)
	}
	body := mustReadBody(t, resp)
	if !strings.Contains(body, "closed") {
		t.Fatalf("422 body = %q, want it to name the service's own 'cannot acknowledge a closed alert' message", body)
	}
}

// TestSnoozeRejectsBothFields is the brief's own test: until and
// until_change together are refused with 422 naming the service's own
// message, not silently resolved one way or the other.
func TestSnoozeRejectsBothFields(t *testing.T) {
	f := newAlertFixture(t, "alert-snooze-both")
	id := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", "used_percent"), rules.StateWarn, time.Now().UTC())

	until := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	resp := postJSON(t, f.operatorC, f.alertsURL("/"+id.String()+"/snooze"), map[string]any{"until": until, "until_change": true})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("snooze with both fields = %d, want 422", resp.StatusCode)
	}
	body := mustReadBody(t, resp)
	if !strings.Contains(body, "mutually exclusive") {
		t.Fatalf("422 body = %q, want the service's own 'mutually exclusive' message", body)
	}

	// Neither field set is refused too (service.go's own other half of
	// the same validation), proving this is the service's check and not
	// a one-sided "until wins" the first assertion alone could not rule
	// out.
	resp = postJSON(t, f.operatorC, f.alertsURL("/"+id.String()+"/snooze"), map[string]any{})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("snooze with neither field = %d, want 422", resp.StatusCode)
	}
	body = mustReadBody(t, resp)
	if !strings.Contains(body, "requires either") {
		t.Fatalf("422 body = %q, want the service's own 'requires either' message", body)
	}
}

// TestSnoozeAndUnsnooze drives both snooze shapes end to end: a bounded
// until, and an unbounded until_change, each muting the alert with
// mute_reason = snooze, and unsnooze clearing it back to unmuted.
func TestSnoozeAndUnsnooze(t *testing.T) {
	f := newAlertFixture(t, "alert-snooze")
	id := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", "used_percent"), rules.StateWarn, time.Now().UTC())

	until := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	resp := postJSON(t, f.operatorC, f.alertsURL("/"+id.String()+"/snooze"), map[string]any{"until": until.Format(time.RFC3339)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("snooze until = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	_, detail := f.getAlert(t, f.operatorC, id)
	if !detail.Muted || detail.MuteReason != "snooze" || detail.SnoozedUntil == nil || !detail.SnoozedUntil.Equal(until) || detail.SnoozeUntilChange {
		t.Fatalf("after snooze until = %+v, want muted/snooze/snoozed_until=%v", detail, until)
	}

	resp = requestJSON(t, f.operatorC, http.MethodDelete, f.alertsURL("/"+id.String()+"/snooze"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unsnooze = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	_, detail = f.getAlert(t, f.operatorC, id)
	if detail.Muted || detail.SnoozedUntil != nil || detail.SnoozeUntilChange {
		t.Fatalf("after unsnooze = %+v, want unmuted and snooze fields cleared", detail)
	}

	resp = postJSON(t, f.operatorC, f.alertsURL("/"+id.String()+"/snooze"), map[string]any{"until_change": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("snooze until_change = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	_, detail = f.getAlert(t, f.operatorC, id)
	if !detail.Muted || detail.MuteReason != "snooze" || detail.SnoozedUntil != nil || !detail.SnoozeUntilChange {
		t.Fatalf("after snooze until_change = %+v, want muted/snooze/snoozed_until=nil/snooze_until_change=true", detail)
	}
}

// TestListFiltersCombine is the brief's own test: ?state=open&severity=
// crit&host=hostA must narrow to the one alert matching all three, not
// widen to anything matching any one of them. Three alerts are seeded so
// that dropping any single filter would change the result: hostA/warn/
// open, hostA/crit/open (the one the combined query must return) and
// hostB/crit/open (excluded only by the host filter).
func TestListFiltersCombine(t *testing.T) {
	f := newAlertFixture(t, "alert-filters")
	now := time.Now().UTC()
	warnA := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", "used_percent"), rules.StateWarn, now)
	critA := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "disk", "/", "used_percent"), rules.StateCrit, now)
	critB := f.seedOpenAlert(t, f.hostB, tupleSubject(f.hostB, "cpu", "", "used_percent"), rules.StateCrit, now)

	q := url.Values{"state": {"open"}, "severity": {"crit"}, "host": {f.hostA.String()}}
	resp := requestJSON(t, f.viewerC, http.MethodGet, f.alertsURL("?"+q.Encode()), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("filtered list = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var list alertListOut
	decodeInto(t, resp, &list)
	if len(list.Alerts) != 1 || list.Alerts[0].ID != critA.String() {
		ids := make([]string, len(list.Alerts))
		for i, a := range list.Alerts {
			ids[i] = a.ID
		}
		t.Fatalf("state=open&severity=crit&host=hostA = %v, want exactly [%s] (not %s, excluded by host; not %s, excluded by severity)",
			ids, critA, critB, warnA)
	}
}

// TestListFiltersAcknowledgedAndMuted exercises the two alerts.Filter
// fields TestListFiltersCombine does not: Acknowledged and Muted both have
// no SQL column search can narrow on the same way state/severity/host/
// scope do (Muted especially — Filter's own doc comment says it is
// "applied here, after Mute has run for each row"), so a parsing mistake
// in parseAlertListFilter (e.g. the boolean inverted, or the field never
// assigned) would pass TestListFiltersCombine untouched and only show up
// here.
func TestListFiltersAcknowledgedAndMuted(t *testing.T) {
	f := newAlertFixture(t, "alert-filters-ack-mute")
	now := time.Now().UTC()
	acked := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "cpu", "", "used_percent"), rules.StateWarn, now)
	plain := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "disk", "/", "used_percent"), rules.StateWarn, now)

	if resp := postJSON(t, f.operatorC, f.alertsURL("/"+acked.String()+"/acknowledge"), map[string]any{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("acknowledge = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := postJSON(t, f.operatorC, f.alertsURL("/"+acked.String()+"/snooze"), map[string]any{"until_change": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("snooze = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	ackTrue := decodeAlertList(t, f.viewerC, f.alertsURL("?acknowledged=true"))
	if ids := ackTrue.ids(); len(ids) != 1 || ids[0] != acked.String() {
		t.Fatalf("acknowledged=true = %v, want exactly [%s]", ids, acked)
	}
	ackFalse := decodeAlertList(t, f.viewerC, f.alertsURL("?acknowledged=false"))
	if ids := ackFalse.ids(); len(ids) != 1 || ids[0] != plain.String() {
		t.Fatalf("acknowledged=false = %v, want exactly [%s]", ids, plain)
	}

	mutedTrue := decodeAlertList(t, f.viewerC, f.alertsURL("?muted=true"))
	if ids := mutedTrue.ids(); len(ids) != 1 || ids[0] != acked.String() {
		t.Fatalf("muted=true = %v, want exactly [%s] (snoozed)", ids, acked)
	}
	mutedFalse := decodeAlertList(t, f.viewerC, f.alertsURL("?muted=false"))
	if ids := mutedFalse.ids(); len(ids) != 1 || ids[0] != plain.String() {
		t.Fatalf("muted=false = %v, want exactly [%s]", ids, plain)
	}

	for q, want := range map[string]string{
		"acknowledged=notabool": "acknowledged",
		"muted=notabool":        "muted",
		"state=sideways":        "state",
		"severity=mild":         "severity",
		"scope=everything":      "scope",
		"host=not-a-uuid":       "host",
	} {
		resp := requestJSON(t, f.viewerC, http.MethodGet, f.alertsURL("?"+q), nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", q, resp.StatusCode)
			continue
		}
		if body := mustReadBody(t, resp); !strings.Contains(body, want) {
			t.Errorf("%s body = %q, want it to name %q", q, body, want)
		}
	}
}

func decodeAlertList(t *testing.T, c *http.Client, url string) alertListOut {
	t.Helper()
	resp := requestJSON(t, c, http.MethodGet, url, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list %s = %d %s", url, resp.StatusCode, mustReadBody(t, resp))
	}
	var out alertListOut
	decodeInto(t, resp, &out)
	return out
}

func (l alertListOut) ids() []string {
	out := make([]string, len(l.Alerts))
	for i, a := range l.Alerts {
		out[i] = a.ID
	}
	return out
}

// TestAlertListPaginates is v0.4a's final review, Important, at the HTTP
// layer: GET /alerts had neither a limit nor a cursor over a table the
// design deliberately gives no retention, so the feature's own front page
// read every alert the organization had ever opened. The route now takes
// ?limit= and ?cursor= and answers next_cursor, the same contract the
// hosts list already gives.
func TestAlertListPaginates(t *testing.T) {
	f := newAlertFixture(t, "alert-pages")
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	var want []string
	for i := range 3 {
		// Newest first is the list's order, so seed oldest first and
		// reverse: want[0] is the newest.
		id := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "disk", "/m"+string(rune('a'+i)), "used_pct"),
			rules.StateWarn, base.Add(time.Duration(i)*time.Minute))
		want = append([]string{id.String()}, want...)
	}

	var got []string
	cursor := ""
	for range 5 {
		u := f.alertsURL("?limit=1")
		if cursor != "" {
			u += "&cursor=" + url.QueryEscape(cursor)
		}
		resp := requestJSON(t, f.viewerC, http.MethodGet, u, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list page = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		var page alertListOut
		decodeInto(t, resp, &page)
		if len(page.Alerts) != 1 {
			t.Fatalf("page returned %d alerts, want the requested limit of 1", len(page.Alerts))
		}
		got = append(got, page.Alerts[0].ID)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(got) != len(want) {
		t.Fatalf("paged walk saw %d alerts, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("page %d = %s, want %s (newest opened first)", i, got[i], want[i])
		}
	}
}

// TestAlertListRejectsAMalformedCursor: a cursor the server did not mint
// is the caller's mistake and answers 400, never a 500 from a decode
// failure surfacing as an unmapped error.
func TestAlertListRejectsAMalformedCursor(t *testing.T) {
	f := newAlertFixture(t, "alert-bad-cursor")
	resp := requestJSON(t, f.viewerC, http.MethodGet, f.alertsURL("?cursor=not-a-cursor"), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("list with a malformed cursor = %d %s, want 400", resp.StatusCode, mustReadBody(t, resp))
	}
}
