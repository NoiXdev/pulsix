package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// Task 11 fix round 2: the re-review judged fix round 1's single grouped
// allowlist entry for all eleven Task 5-8 auth/credential routes too
// coarse, and settled the question route by route. These four tests cover
// the four routes that moved off auditAllowlist (audit_coverage_test.go)
// onto the same instance-level (org_id = nil) recording mechanism
// TestDeactivateUserRecordsInstanceLevelAuditEvent already exercises for
// POST /admin/users/{id}/deactivate: no organization, the acting user
// named as actor, and the right object — read back through
// GET /me/activity, since none of these routes has an organization to look
// the event up under.

// enrolAndConfirmTOTP drives POST /auth/totp/enrol then POST
// /auth/totp/confirm to completion for c's session — the same two calls
// TestTOTPGatesTheSession (mfa_handlers_test.go) uses — and returns the
// confirmed secret so a caller can derive further valid codes (e.g. for a
// subsequent DELETE /auth/totp) without re-enrolling.
func enrolAndConfirmTOTP(t *testing.T, s *apitest.Server, c *http.Client) string {
	t.Helper()
	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/enrol", struct{}{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enrol: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var enrol struct {
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&enrol); err != nil {
		t.Fatal(err)
	}
	code, _ := totp.GenerateCode(enrol.Secret, time.Now())
	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/auth/totp/confirm", map[string]string{"code": code})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("confirm: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	return enrol.Secret
}

// findEvent returns the one item in items whose Action matches action, or
// fails the test — every test below expects to find exactly the one event
// it just triggered.
func findEvent(t *testing.T, items []eventDTO, action string) eventDTO {
	t.Helper()
	for _, ev := range items {
		if ev.Action == action {
			return ev
		}
	}
	t.Fatalf("no %q event in %+v", action, items)
	return eventDTO{}
}

// assertNoEvent fails the test if items contains any event with the given
// Action — used to prove an event triggered by one user does not leak into
// another user's own GET /me/activity feed.
func assertNoEvent(t *testing.T, items []eventDTO, action string) {
	t.Helper()
	for _, ev := range items {
		if ev.Action == action {
			t.Fatalf("unexpected %q event in %+v", action, items)
		}
	}
}

func myActivity(t *testing.T, s *apitest.Server, c *http.Client) []eventDTO {
	t.Helper()
	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/me/activity?limit=50", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get me/activity: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	return decodeActivity(t, resp).Items
}

// TestTOTPConfirmRecordsInstanceLevelAuditEvent: POST /auth/totp/confirm is
// the step that actually enables a second factor — it must show up on
// GET /me/activity with no organization, the enrolling user as actor, and
// object (totp, <that user's id>): TOTP has exactly one secret per user,
// so the user's own id is the only stable identifier for "which second
// factor" without a separate credential row to point at (see the doc
// comment on the confirm handler, mfa_handlers.go).
func TestTOTPConfirmRecordsInstanceLevelAuditEvent(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("audit-totp-confirm@example.test", "correct horse battery")
	c := s.LoginAs(uid)

	enrolAndConfirmTOTP(t, s, c)

	ev := findEvent(t, myActivity(t, s, c), "POST confirmTOTP")
	if ev.OrgID != nil {
		t.Fatalf("OrgID = %v, want nil (an own-account MFA change has no organization)", *ev.OrgID)
	}
	if ev.ActorID == nil || *ev.ActorID != uid.String() {
		t.Fatalf("ActorID = %v, want the enrolling user %s", ev.ActorID, uid)
	}
	if ev.ObjectType != "totp" || ev.ObjectID != uid.String() {
		t.Fatalf("object = (%q, %q), want (totp, %s)", ev.ObjectType, ev.ObjectID, uid)
	}
}

// TestTOTPDisableRecordsCorrectActor: DELETE /auth/totp is called out as
// one of the highest-value forensic signals in the account-security
// trail — a second factor being stripped is exactly the kind of event a
// user needs to see after a compromise. This test uses two independent
// users specifically so a misattribution bug (the event recorded against
// the wrong user) cannot pass unnoticed: only userB disables, and
// assertNoEvent below fails the test if that event ever turns up in
// userA's own feed instead of (or in addition to) userB's.
func TestTOTPDisableRecordsCorrectActor(t *testing.T) {
	s := apitest.New(t)
	uidA := s.SeedUser("audit-totp-disable-a@example.test", "correct horse battery")
	uidB := s.SeedUser("audit-totp-disable-b@example.test", "correct horse battery")
	cA := s.LoginAs(uidA)
	cB := s.LoginAs(uidB)

	enrolAndConfirmTOTP(t, s, cA)
	secretB := enrolAndConfirmTOTP(t, s, cB)
	// A second factor is mandatory, so TOTP can only be disabled while the
	// account keeps another one.
	s.SeedPasskey(uidB, "B's fallback key")

	// A never disables. Only B does, using a fresh code (a later time step
	// than the one already spent confirming) — the same
	// time.Now().Add(30*time.Second) pattern
	// TestTOTPDisableRequiresFreshCodeAndRemovesState uses, since Disable
	// requires a code that has not already been consumed for this secret.
	disableCode, _ := totp.GenerateCode(secretB, time.Now().Add(30*time.Second))
	if resp := deleteJSON(t, cB, s.HTTP.URL+"/api/v1/auth/totp", map[string]string{"code": disableCode}); resp.StatusCode != http.StatusOK {
		t.Fatalf("disable B: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	ev := findEvent(t, myActivity(t, s, cB), "DELETE disableTOTP")
	if ev.OrgID != nil {
		t.Fatalf("OrgID = %v, want nil", *ev.OrgID)
	}
	if ev.ActorID == nil || *ev.ActorID != uidB.String() {
		t.Fatalf("ActorID = %v, want the disabling user %s, not anyone else", ev.ActorID, uidB)
	}
	if ev.ObjectType != "totp" || ev.ObjectID != uidB.String() {
		t.Fatalf("object = (%q, %q), want (totp, %s)", ev.ObjectType, ev.ObjectID, uidB)
	}
	assertNoEvent(t, myActivity(t, s, cA), "DELETE disableTOTP")
}

// registerPasskeyForTest drives a full register/begin -> register/finish
// ceremony to completion using buildNoneAttestationResponse
// (passkey_registration_conflict_test.go, same package) — the "none"
// attestation format needs no Task 15 virtual authenticator to pass
// go-webauthn's real verification — and returns the id GET /auth/passkeys
// reports for the credential it just created, credID distinguishing this
// registration's credential from any other caller in the same test.
func registerPasskeyForTest(t *testing.T, s *apitest.Server, c *http.Client, name string, credID []byte) string {
	t.Helper()
	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/begin", struct{}{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register/begin: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var begin struct {
		CeremonyID string          `json:"ceremony_id"`
		Options    json.RawMessage `json:"options"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&begin); err != nil {
		t.Fatal(err)
	}

	response := buildNoneAttestationResponse(t, begin.Options, "localhost", s.HTTP.URL, credID)
	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/finish", map[string]any{
		"ceremony_id": begin.CeremonyID, "name": name, "response": json.RawMessage(response),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register/finish: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	req, _ := http.NewRequest(http.MethodGet, s.HTTP.URL+"/api/v1/auth/passkeys", nil)
	listResp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Passkeys []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"passkeys"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	for _, pk := range list.Passkeys {
		if pk.Name == name {
			return pk.ID
		}
	}
	t.Fatalf("registered passkey %q not found in list %+v", name, list.Passkeys)
	return ""
}

// TestPasskeyRegisterFinishRecordsInstanceLevelAuditEvent:
// POST /auth/passkeys/register/finish is the step that actually attaches a
// passkey to the account — it must show up on GET /me/activity with no
// organization, the registering user as actor, and object type "passkey".
// Enrolment.FinishPasskey (internal/auth) does not return the new credential's
// id, only persists it, so the object is identified by account rather than
// by credential row — the handler's own doc comment (passkey_handlers.go)
// explains the tradeoff; the human-readable name is carried in the
// summary instead.
func TestPasskeyRegisterFinishRecordsInstanceLevelAuditEvent(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("audit-passkey-finish@example.test", "correct horse battery")
	c := s.LoginAs(uid)

	registerPasskeyForTest(t, s, c, "Finish Test Key", []byte("audit-passkey-finish-cred"))

	ev := findEvent(t, myActivity(t, s, c), "POST finishPasskeyRegistration")
	if ev.OrgID != nil {
		t.Fatalf("OrgID = %v, want nil (an own-account passkey change has no organization)", *ev.OrgID)
	}
	if ev.ActorID == nil || *ev.ActorID != uid.String() {
		t.Fatalf("ActorID = %v, want the registering user %s", ev.ActorID, uid)
	}
	if ev.ObjectType != "passkey" || ev.ObjectID != uid.String() {
		t.Fatalf("object = (%q, %q), want (passkey, %s)", ev.ObjectType, ev.ObjectID, uid)
	}
}

// TestPasskeyDeleteRecordsCorrectActor: DELETE /auth/passkeys/{id} removes
// a passkey — a real account-security state change. This test uses two
// independent users, each with their own registered passkey, specifically
// so a misattribution bug (the event recorded against the wrong user)
// cannot pass unnoticed: only userB deletes, and assertNoEvent below fails
// the test if that event ever turns up in userA's own feed instead of (or
// in addition to) userB's.
func TestPasskeyDeleteRecordsCorrectActor(t *testing.T) {
	s := apitest.New(t)
	uidA := s.SeedUser("audit-passkey-delete-a@example.test", "correct horse battery")
	uidB := s.SeedUser("audit-passkey-delete-b@example.test", "correct horse battery")
	cA := s.LoginAs(uidA)
	cB := s.LoginAs(uidB)

	registerPasskeyForTest(t, s, cA, "A's Key", []byte("audit-passkey-delete-a-cred"))
	idB := registerPasskeyForTest(t, s, cB, "B's Key", []byte("audit-passkey-delete-b-cred"))
	// A second factor is mandatory, so a passkey can only be removed while
	// the account keeps another one.
	s.SeedPasskey(uidB, "B's fallback key")

	del, err := http.NewRequest(http.MethodDelete, s.HTTP.URL+"/api/v1/auth/passkeys/"+idB, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cB.Do(del)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete B's passkey: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	ev := findEvent(t, myActivity(t, s, cB), "DELETE deletePasskey")
	if ev.OrgID != nil {
		t.Fatalf("OrgID = %v, want nil", *ev.OrgID)
	}
	if ev.ActorID == nil || *ev.ActorID != uidB.String() {
		t.Fatalf("ActorID = %v, want the deleting user %s, not anyone else", ev.ActorID, uidB)
	}
	if ev.ObjectType != "passkey" || ev.ObjectID != idB {
		t.Fatalf("object = (%q, %q), want (passkey, %s) — the deleted credential, not the account", ev.ObjectType, ev.ObjectID, idB)
	}
	assertNoEvent(t, myActivity(t, s, cA), "DELETE deletePasskey")
}
