// DELETE /orgs/{org}/hosts/{id}/items (backlog 3.12b): the host page's
// Forget action. The forgetting itself — every table, the transaction, the
// races — is internal/checks' and its store's to prove; these tests pin the
// HTTP surface: the status codes, the query shape (an item name travels in
// the query, and an empty one is the whole-host item), the role, the audit
// entry, and that the route reaches the alert closer the manager wires.
package api_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/rules"
)

// forgetURL builds the route's URL with check_key and item URL-encoded.
// withItem false leaves the item parameter out altogether, which is not
// the same request as an empty one.
func forgetURL(hostURL, checkKey, item string, withItem bool) string {
	q := url.Values{}
	q.Set("check_key", checkKey)
	if withItem {
		q.Set("item", item)
	}
	return hostURL + "/items?" + q.Encode()
}

// currentRows counts hostID's check_current rows for (checkKey, item).
func currentRows(t *testing.T, f checksFixture, hostID uuid.UUID, checkKey, item string) int {
	t.Helper()
	var n int
	if err := f.s.DB.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM check_current WHERE host_id = $1 AND check_key = $2 AND item = $3`,
		hostID, checkKey, item).Scan(&n); err != nil {
		t.Fatalf("count current rows: %v", err)
	}
	return n
}

func TestForgetItemRoute(t *testing.T) {
	f := newChecksFixture(t, "forget-item")
	at := time.Now().Add(-10 * time.Minute)
	// An item name with characters a path could not carry, and the
	// whole-host item "".
	odd := "/mnt/a b/?x=1&y#z"
	f.record(t,
		checks.Observation{HostID: f.web, CheckKey: "disk", Item: odd, Metric: "used_percent", At: at, Value: floatPtr(50)},
		checks.Observation{HostID: f.web, CheckKey: "disk", Item: odd, Metric: "free_bytes", At: at, Value: floatPtr(1)},
		checks.Observation{HostID: f.web, CheckKey: "disk", Item: "/", Metric: "used_percent", At: at, Value: floatPtr(10)},
		checks.Observation{HostID: f.web, CheckKey: "cpu", Metric: "usage_percent", At: at, Value: floatPtr(5)},
	)
	host := f.hostURL(f.web)

	t.Run("a viewer may not forget", func(t *testing.T) {
		resp := requestJSON(t, f.viewerC, http.MethodDelete, forgetURL(host, "disk", odd, true), nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("viewer forget = %d %s, want 403", resp.StatusCode, mustReadBody(t, resp))
		}
		if n := currentRows(t, f, f.web, "disk", odd); n != 2 {
			t.Fatalf("current rows after a refused forget = %d, want 2", n)
		}
	})

	t.Run("a non-member reads it as no such organization", func(t *testing.T) {
		resp := requestJSON(t, f.stranger, http.MethodDelete, forgetURL(host, "disk", odd, true), nil)
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusForbidden {
			t.Fatalf("stranger forget = %d, want 404 or 403", resp.StatusCode)
		}
	})

	t.Run("check_key is required", func(t *testing.T) {
		resp := requestJSON(t, f.operatorC, http.MethodDelete, host+"/items?item=x", nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("forget without check_key = %d %s, want 400", resp.StatusCode, mustReadBody(t, resp))
		}
	})

	// An absent item is refused rather than read as the whole-host item: a
	// client that forgot the parameter must not wipe the host-wide values.
	t.Run("item must be present, even if empty", func(t *testing.T) {
		resp := requestJSON(t, f.operatorC, http.MethodDelete, forgetURL(host, "cpu", "", false), nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("forget without item = %d %s, want 400", resp.StatusCode, mustReadBody(t, resp))
		}
		if n := currentRows(t, f, f.web, "cpu", ""); n != 1 {
			t.Fatalf("whole-host rows after a refused forget = %d, want 1", n)
		}
	})

	t.Run("nothing matched is 404", func(t *testing.T) {
		resp := requestJSON(t, f.operatorC, http.MethodDelete, forgetURL(host, "disk", "/never", true), nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("forget unknown item = %d %s, want 404", resp.StatusCode, mustReadBody(t, resp))
		}
	})

	t.Run("a host of no organization the caller sees is 404", func(t *testing.T) {
		resp := requestJSON(t, f.operatorC, http.MethodDelete, forgetURL(f.hostURL(uuid.New()), "disk", odd, true), nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("forget on unknown host = %d %s, want 404", resp.StatusCode, mustReadBody(t, resp))
		}
	})

	t.Run("an operator forgets an item with any characters in its name", func(t *testing.T) {
		resp := requestJSON(t, f.operatorC, http.MethodDelete, forgetURL(host, "disk", odd, true), nil)
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("forget = %d %s, want 204", resp.StatusCode, mustReadBody(t, resp))
		}
		if n := currentRows(t, f, f.web, "disk", odd); n != 0 {
			t.Fatalf("current rows after forget = %d, want 0", n)
		}
		if n := currentRows(t, f, f.web, "disk", "/"); n != 1 {
			t.Fatalf("a sibling item lost its rows: %d, want 1", n)
		}
		// A second forget finds nothing left.
		again := requestJSON(t, f.operatorC, http.MethodDelete, forgetURL(host, "disk", odd, true), nil)
		if again.StatusCode != http.StatusNotFound {
			t.Fatalf("second forget = %d, want 404", again.StatusCode)
		}
	})

	t.Run("item= forgets the whole-host item", func(t *testing.T) {
		resp := requestJSON(t, f.operatorC, http.MethodDelete, forgetURL(host, "cpu", "", true), nil)
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("forget whole-host item = %d %s, want 204", resp.StatusCode, mustReadBody(t, resp))
		}
		if n := currentRows(t, f, f.web, "cpu", ""); n != 0 {
			t.Fatalf("whole-host rows after forget = %d, want 0", n)
		}
	})

	t.Run("each forget is audited with host, check key, item and actor", func(t *testing.T) {
		resp := requestJSON(t, f.ownerC, http.MethodGet, f.activityURL(), nil)
		var found []eventDTO
		for _, ev := range decodeActivity(t, resp).Items {
			if ev.Action == "DELETE forgetHostItem" {
				found = append(found, ev)
			}
		}
		// Two successful forgets; the refused and not-found ones leave none.
		if len(found) != 2 {
			t.Fatalf("forget audit events = %d (%+v), want 2", len(found), found)
		}
		var sawOdd, sawWhole bool
		for _, ev := range found {
			if ev.ObjectID != f.web.String() || ev.ObjectType != "host" || ev.ActorKind != "user" || ev.ActorID == nil {
				t.Fatalf("event %+v does not name the host and a user actor", ev)
			}
			switch {
			case strings.Contains(ev.Summary, `"disk"`) && strings.Contains(ev.Summary, `"/mnt/a b/?x=1&y#z"`):
				sawOdd = true
			case strings.Contains(ev.Summary, `"cpu"`) && strings.Contains(ev.Summary, `""`):
				sawWhole = true
			}
		}
		if !sawOdd || !sawWhole {
			t.Fatalf("summaries %+v do not name each forget's check key and item", found)
		}
	})
}

// TestForgetItemRouteClosesTheItemsAlerts: the route reaches the alert
// closer — the apitest server is wired like the manager, so an open alert
// on the forgotten item comes back closed as forgotten.
func TestForgetItemRouteClosesTheItemsAlerts(t *testing.T) {
	f := newAlertFixture(t, "forget-item-alert")
	at := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	scoped := db.WithOrgScope(context.Background(), f.orgID)
	if res, err := f.s.Checks.RecordObservations(scoped, f.orgID, []checks.Observation{
		{HostID: f.hostA, CheckKey: "disk", Item: "/data", Metric: "used_percent", At: at, Value: floatPtr(97)},
	}); err != nil || res.Accepted != 1 {
		t.Fatalf("record: %+v, %v", res, err)
	}
	id := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "disk", "/data", "used_percent"), rules.StateCrit, at)

	hostURL := f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/hosts/" + f.hostA.String()
	resp := requestJSON(t, f.operatorC, http.MethodDelete, forgetURL(hostURL, "disk", "/data", true), nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("forget = %d %s, want 204", resp.StatusCode, mustReadBody(t, resp))
	}
	got, detail := f.getAlert(t, f.viewerC, id)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("get alert = %d", got.StatusCode)
	}
	if detail.ClosedAt == nil || detail.ClosedReason != "forgotten" {
		t.Fatalf("closed_at/closed_reason = %v/%q, want set/forgotten", detail.ClosedAt, detail.ClosedReason)
	}
}
