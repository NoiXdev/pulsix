// The on-call rotation API's tests (v0.4c Task 8): the five rotation routes
// (registerOncallRoutes, oncall_handlers.go) plus the roster view, driven
// through the full API against the test database, authenticated with a
// bearer token (apitest.Server.OrgWithToken/OrgWithTokenPerms) the same way
// alert_route_handlers_test.go exercises its own sibling surface.
package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/tenancy"
)

func TestOncallRotationCRUD(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	ch := s.SeedChannel(t, org)

	created := s.Post(t, tok, "/api/v1/orgs/"+org.String()+"/oncall-rotations", map[string]any{
		"name": "db team", "period_days": 7,
		"anchor": "2026-01-05T09:00:00+01:00", "timezone": "Europe/Berlin",
		"channels": []string{ch.String()},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("POST = %d, want 201: %s", created.Code, created.Body)
	}
	id := decodeID(t, created)

	shifts := s.Get(t, tok, "/api/v1/orgs/"+org.String()+"/oncall-rotations/"+id+
		"/shifts?from=2026-01-05T00:00:00Z&to=2026-02-01T00:00:00Z")
	if shifts.Code != http.StatusOK {
		t.Fatalf("GET shifts = %d, want 200: %s", shifts.Code, shifts.Body)
	}
}

func TestOncallWriteNeedsTheChannelPermission(t *testing.T) {
	// A rotation decides where alerts leave the system — the same bar the
	// routes themselves sit behind, deliberately not the check permissions.
	s := apitest.New(t)
	org, tok := s.OrgWithTokenPerms(t, tenancy.PermChecksWrite, tenancy.PermChecksRead)
	ch := s.SeedChannel(t, org)

	resp := s.Post(t, tok, "/api/v1/orgs/"+org.String()+"/oncall-rotations", map[string]any{
		"name": "db team", "period_days": 7,
		"anchor": "2026-01-05T09:00:00+01:00", "timezone": "Europe/Berlin",
		"channels": []string{ch.String()},
	})
	if resp.Code != http.StatusForbidden {
		t.Errorf("POST with checks.write but not channels.write = %d, want 403", resp.Code)
	}
}

// TestOncallShiftsCarryChannelNames pins the one requirement that is easy to
// miss (v0.4c Task 8 brief): the roster strip that reads this endpoint has
// to print who is on call without resolving a channel id against a
// paginated channel list itself, so the DTO carries the name, not only the
// id — the same reasoning v0.4a settled for the alert list's host name.
func TestOncallShiftsCarryChannelNames(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	ch := s.SeedChannel(t, org)

	created := s.Post(t, tok, "/api/v1/orgs/"+org.String()+"/oncall-rotations", map[string]any{
		"name": "db team", "period_days": 7,
		"anchor": "2026-01-05T09:00:00+01:00", "timezone": "Europe/Berlin",
		"channels": []string{ch.String()},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("POST = %d, want 201: %s", created.Code, created.Body)
	}
	id := decodeID(t, created)

	shifts := s.Get(t, tok, "/api/v1/orgs/"+org.String()+"/oncall-rotations/"+id+
		"/shifts?from=2026-01-05T00:00:00Z&to=2026-01-12T00:00:00Z")
	if shifts.Code != http.StatusOK {
		t.Fatalf("GET shifts = %d, want 200: %s", shifts.Code, shifts.Body)
	}
	var body struct {
		Shifts []struct {
			ChannelID   string `json:"channel_id"`
			ChannelName string `json:"channel_name"`
		} `json:"shifts"`
	}
	if err := json.Unmarshal(shifts.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode shifts: %v: %s", err, shifts.Body)
	}
	if len(body.Shifts) == 0 {
		t.Fatalf("no shifts returned: %s", shifts.Body)
	}
	for _, sh := range body.Shifts {
		if sh.ChannelName == "" {
			t.Errorf("shift for channel %s has no channel_name: %s", sh.ChannelID, shifts.Body)
		}
	}
}

// TestOncallRotationUpdateAndDelete exercises the rest of the CRUD surface
// TestOncallRotationCRUD leaves untouched: a PATCH with a correct If-Match
// changes the stored rotation and its ETag, and a DELETE then makes the id
// answer 404 — the same proof TestAlertRouteUpdateAndDelete gives its own
// sibling resource.
func TestOncallRotationUpdateAndDelete(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	ch := s.SeedChannel(t, org)
	url := "/api/v1/orgs/" + org.String() + "/oncall-rotations"
	created := s.Post(t, tok, url, map[string]any{
		"name": "db team", "period_days": 7,
		"anchor": "2026-01-05T09:00:00+01:00", "timezone": "Europe/Berlin",
		"channels": []string{ch.String()},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", created.Code, created.Body)
	}
	id := decodeID(t, created)

	updated := s.Patch(t, tok, url+"/"+id, `"1"`, map[string]any{
		"name": "db team (updated)", "period_days": 14,
		"anchor": "2026-01-05T09:00:00+01:00", "timezone": "Europe/Berlin",
		"channels": []string{ch.String()},
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("PATCH = %d, want 200: %s", updated.Code, updated.Body)
	}

	if resp := s.Delete(t, tok, url+"/"+id, `"2"`); resp.Code != http.StatusOK {
		t.Fatalf("DELETE = %d, want 200: %s", resp.Code, resp.Body)
	}
	if resp := s.Get(t, tok, url+"/"+id); resp.Code != http.StatusNotFound {
		t.Errorf("GET after delete = %d, want 404", resp.Code)
	}
}

// TestOncallReadNeedsTheChannelPermission is TestOncallWriteNeedsTheChannel-
// Permission's read-side counterpart.
func TestOncallReadNeedsTheChannelPermission(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithTokenPerms(t, tenancy.PermHostsRead)

	resp := s.Get(t, tok, "/api/v1/orgs/"+org.String()+"/oncall-rotations")
	if resp.Code != http.StatusForbidden {
		t.Errorf("GET with hosts.read but not channels.read = %d, want 403", resp.Code)
	}
}

// seedOncallRotation creates a rotation through the API and returns its id —
// the fixture every exception test below starts from. name must be unique
// within org: oncall_rotations enforces UNIQUE (org_id, name), so a test
// that seeds two rotations in the same organization (e.g.
// TestOncallExceptionDeleteRejectsAWrongRotation) needs two distinct names.
func seedOncallRotation(t *testing.T, s *apitest.Server, org, tok, name, ch string) string {
	t.Helper()
	created := s.Post(t, tok, "/api/v1/orgs/"+org+"/oncall-rotations", map[string]any{
		"name": name, "period_days": 7,
		"anchor": "2026-01-05T09:00:00+01:00", "timezone": "Europe/Berlin",
		"channels": []string{ch},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("POST rotation = %d, want 201: %s", created.Code, created.Body)
	}
	return decodeID(t, created)
}

// TestOncallExceptionsRoundTrip pins the review's "missing" finding (v0.4c
// Task 8, fix round 1): a stand-in can be created, read back in its
// rotation's list, and deleted — the three routes the next GUI task's
// stand-ins section needs and the original brief never asked for.
func TestOncallExceptionsRoundTrip(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	ch := s.SeedChannel(t, org)
	standIn := s.SeedChannel(t, org)
	rotationID := seedOncallRotation(t, s, org.String(), tok, "db team", ch.String())

	url := "/api/v1/orgs/" + org.String() + "/oncall-rotations/" + rotationID + "/exceptions"
	created := s.Post(t, tok, url, map[string]any{
		"starts_at": "2026-01-10T00:00:00Z", "ends_at": "2026-01-11T00:00:00Z",
		"channel_id": standIn.String(), "note": "vacation cover",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("POST exception = %d, want 201: %s", created.Code, created.Body)
	}
	exceptionID := decodeID(t, created)

	list := s.Get(t, tok, url)
	if list.Code != http.StatusOK {
		t.Fatalf("GET exceptions = %d, want 200: %s", list.Code, list.Body)
	}
	if !strings.Contains(list.Body.String(), exceptionID) {
		t.Errorf("the created exception is missing from its rotation's list: %s", list.Body)
	}

	if resp := s.Delete(t, tok, url+"/"+exceptionID, ""); resp.Code != http.StatusOK {
		t.Fatalf("DELETE exception = %d, want 200: %s", resp.Code, resp.Body)
	}

	after := s.Get(t, tok, url)
	if after.Code != http.StatusOK {
		t.Fatalf("GET exceptions after delete = %d, want 200: %s", after.Code, after.Body)
	}
	if strings.Contains(after.Body.String(), exceptionID) {
		t.Errorf("deleted exception is still listed: %s", after.Body)
	}
}

// TestOncallExceptionRejectsAForeignChannel pins the review's own example of
// the scope guarantee it asked for: a stand-in can never name another
// organization's channel. oncall_exceptions' composite foreign key
// (org_id, channel_id) onto notification_channels is what actually refuses
// it (CreateException's own doc comment, store_sql.go); this pins that the
// API surfaces that refusal as a client error.
func TestOncallExceptionRejectsAForeignChannel(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	ch := s.SeedChannel(t, org)
	rotationID := seedOncallRotation(t, s, org.String(), tok, "db team", ch.String())

	foreignOrg, _ := s.OrgWithToken(t)
	foreignCh := s.SeedChannel(t, foreignOrg)

	resp := s.Post(t, tok, "/api/v1/orgs/"+org.String()+"/oncall-rotations/"+rotationID+"/exceptions", map[string]any{
		"starts_at": "2026-01-10T00:00:00Z", "ends_at": "2026-01-11T00:00:00Z",
		"channel_id": foreignCh.String(),
	})
	if resp.Code != http.StatusBadRequest {
		t.Errorf("POST exception naming another organization's channel = %d, want 400: %s", resp.Code, resp.Body)
	}
}

// TestOncallExceptionDeleteSucceedsThroughItsOwnRotation pins the plain
// success case DeleteException's own rotation_id predicate must still allow:
// the path's rotation, the exception's rotation and the request all agree.
func TestOncallExceptionDeleteSucceedsThroughItsOwnRotation(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	ch := s.SeedChannel(t, org)
	standIn := s.SeedChannel(t, org)
	rotationID := seedOncallRotation(t, s, org.String(), tok, "db team", ch.String())

	url := "/api/v1/orgs/" + org.String() + "/oncall-rotations/" + rotationID + "/exceptions"
	created := s.Post(t, tok, url, map[string]any{
		"starts_at": "2026-01-10T00:00:00Z", "ends_at": "2026-01-11T00:00:00Z",
		"channel_id": standIn.String(),
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("POST exception = %d, want 201: %s", created.Code, created.Body)
	}
	exceptionID := decodeID(t, created)

	if resp := s.Delete(t, tok, url+"/"+exceptionID, ""); resp.Code != http.StatusOK {
		t.Fatalf("DELETE through its own rotation = %d, want 200: %s", resp.Code, resp.Body)
	}
}

// TestOncallExceptionDeleteRejectsAWrongRotation pins review finding 3 (v0.4c
// Task 8, fix round 2): the path's rotation id is load-bearing, not
// decorative. An exception created under rotationA must not be deletable by
// naming rotationB in the path, even though both rotations and the token
// belong to the same organization — a path that names a parent must mean
// it, the same invariant the exception-list route already holds through its
// own existence check (TestOncallExceptionListRejectsAForeignRotation).
func TestOncallExceptionDeleteRejectsAWrongRotation(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	chA, chB := s.SeedChannel(t, org), s.SeedChannel(t, org)
	standIn := s.SeedChannel(t, org)
	rotationA := seedOncallRotation(t, s, org.String(), tok, "rotation a", chA.String())
	rotationB := seedOncallRotation(t, s, org.String(), tok, "rotation b", chB.String())

	urlA := "/api/v1/orgs/" + org.String() + "/oncall-rotations/" + rotationA + "/exceptions"
	created := s.Post(t, tok, urlA, map[string]any{
		"starts_at": "2026-01-10T00:00:00Z", "ends_at": "2026-01-11T00:00:00Z",
		"channel_id": standIn.String(),
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("POST exception = %d, want 201: %s", created.Code, created.Body)
	}
	exceptionID := decodeID(t, created)

	wrongURL := "/api/v1/orgs/" + org.String() + "/oncall-rotations/" + rotationB + "/exceptions/" + exceptionID
	if resp := s.Delete(t, tok, wrongURL, ""); resp.Code != http.StatusNotFound {
		t.Errorf("DELETE through a different rotation in the same organization = %d, want 404: %s", resp.Code, resp.Body)
	}

	// Still there — the wrong attempt must not have deleted it regardless of
	// the status it answered with.
	rightURL := urlA + "/" + exceptionID
	if resp := s.Delete(t, tok, rightURL, ""); resp.Code != http.StatusOK {
		t.Errorf("DELETE through its own rotation after the wrong attempt = %d, want 200: %s", resp.Code, resp.Body)
	}
}

// TestOncallExceptionListRejectsAForeignRotation pins the existence check
// registerOncallRoutes' exception-list handler runs before ListExceptions:
// a rotation id from another organization must read as "no such rotation"
// rather than confirming (via an empty list) that it exists somewhere, the
// same ordering the per-host alert-route union view gives its own host
// lookup.
func TestOncallExceptionListRejectsAForeignRotation(t *testing.T) {
	s := apitest.New(t)
	orgA, tokA := s.OrgWithToken(t)
	chA := s.SeedChannel(t, orgA)
	rotationID := seedOncallRotation(t, s, orgA.String(), tokA, "db team", chA.String())

	orgB, tokB := s.OrgWithToken(t)
	resp := s.Get(t, tokB, "/api/v1/orgs/"+orgB.String()+"/oncall-rotations/"+rotationID+"/exceptions")
	if resp.Code != http.StatusNotFound {
		t.Errorf("GET exceptions for another organization's rotation = %d, want 404: %s", resp.Code, resp.Body)
	}
}

// GET .../shifts takes its range straight off the query string, so the
// range is attacker-chosen. Without oncall.MaxRange the widest one RFC 3339
// can express walks roughly 3.6 million handovers and builds as many
// segments and DTOs — about a gigabyte and tens of seconds, inside the
// process that is also the alert follower, for anyone holding channels.read
// (whole-branch review, Critical #1). It has to be a 400 the caller can
// read, not a 500 and a log line.
func TestOncallShiftsRefuseAnUnboundedRange(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	ch := s.SeedChannel(t, org)

	created := s.Post(t, tok, "/api/v1/orgs/"+org.String()+"/oncall-rotations", map[string]any{
		"name": "db team", "period_days": 1,
		"anchor": "2026-01-05T09:00:00+01:00", "timezone": "Europe/Berlin",
		"channels": []string{ch.String()},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("POST = %d, want 201: %s", created.Code, created.Body)
	}
	id := decodeID(t, created)

	resp := s.Get(t, tok, "/api/v1/orgs/"+org.String()+"/oncall-rotations/"+id+
		"/shifts?from=0001-01-01T00:00:00Z&to=9999-12-31T00:00:00Z")
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("GET shifts over the whole representable range = %d, want 400: %s", resp.Code, resp.Body)
	}
}

// Deleting the last channel a rotation names cascades its cycle rows away
// — the spec's own "Error handling" section calls this ordinary, and the
// GUI's own delete confirmation warns about it. The roster then cannot be
// built, which is a 400 the rotations page can render, not a 500 and an
// "unmapped oncall error" log line that leaves it loading forever
// (whole-branch review, Important #4).
func TestOncallShiftsOnAnEmptiedCycleAreABadRequest(t *testing.T) {
	s := apitest.New(t)
	org, tok := s.OrgWithToken(t)
	ch := s.SeedChannel(t, org)

	created := s.Post(t, tok, "/api/v1/orgs/"+org.String()+"/oncall-rotations", map[string]any{
		"name": "db team", "period_days": 7,
		"anchor": "2026-01-05T09:00:00+01:00", "timezone": "Europe/Berlin",
		"channels": []string{ch.String()},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("POST = %d, want 201: %s", created.Code, created.Body)
	}
	id := decodeID(t, created)

	deleted := s.Delete(t, tok, "/api/v1/orgs/"+org.String()+"/notification-channels/"+ch.String(), "")
	if deleted.Code != http.StatusOK && deleted.Code != http.StatusNoContent {
		t.Fatalf("DELETE channel = %d: %s", deleted.Code, deleted.Body)
	}

	resp := s.Get(t, tok, "/api/v1/orgs/"+org.String()+"/oncall-rotations/"+id+
		"/shifts?from=2026-01-05T00:00:00Z&to=2026-01-12T00:00:00Z")
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("GET shifts for a rotation whose channels were deleted = %d, want 400: %s", resp.Code, resp.Body)
	}
}
