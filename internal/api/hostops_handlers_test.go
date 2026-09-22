// The connection-test and host-key routes' tests (auth matrix, the
// pin-what-you-saw rule, validation, cross-org invisibility), driven
// through the full API against the test database.
package api_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/tenancy"
)

var hctx = context.Background()

const observedKey = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// hostopsFixture is the seeded org: owner, operator, viewer and one managed
// host, plus the logged-in clients the auth matrix drives.
type hostopsFixture struct {
	s         *apitest.Server
	orgID     uuid.UUID
	ownerC    *http.Client
	operatorC *http.Client
	viewerC   *http.Client
	hostID    string
}

func seedHostopsOrg(t *testing.T) hostopsFixture {
	t.Helper()
	s := apitest.New(t)
	owner := s.SeedUser("hostops-owner@example.test", "correct horse battery")
	operator := s.SeedUser("hostops-operator@example.test", "correct horse battery")
	viewer := s.SeedUser("hostops-viewer@example.test", "correct horse battery")
	org := s.SeedOrg("hostops-org", owner)
	s.AddMember(org, operator, tenancy.RoleOperator)
	s.AddMember(org, viewer, tenancy.RoleViewer)
	zones, err := s.Deps.Hosts.ListZones(hctx, org)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}
	created, err := s.Deps.Hosts.CreateHost(hctx, org, hosts.HostInput{Name: "web-01", Address: "10.0.0.1", ZoneID: zones[0].ID, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	return hostopsFixture{
		s: s, orgID: org, ownerC: s.LoginAs(owner), operatorC: s.LoginAs(operator), viewerC: s.LoginAs(viewer),
		hostID: created.ID.String(),
	}
}

func (f hostopsFixture) testURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/hostops-org/hosts/" + f.hostID + "/connection-test"
}

func (f hostopsFixture) pinURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/hostops-org/hosts/" + f.hostID + "/host-key"
}

func TestConnectionTestRouteEnqueuesJob(t *testing.T) {
	f := seedHostopsOrg(t)

	resp := postJSON(t, f.ownerC, f.testURL(), nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var started struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&started); err != nil {
		t.Fatal(err)
	}
	if started.JobID == "" {
		t.Fatal("202 body carries no job id")
	}
	j, err := f.s.Deps.Jobs.Get(hctx, f.orgID, mustUUID(t, started.JobID))
	if err != nil {
		t.Fatalf("job: %v", err)
	}
	if string(j.Kind) != "connection_test" || len(j.HostIDs) != 1 || j.HostIDs[0].String() != f.hostID {
		t.Fatalf("job = %+v", j)
	}
	if j.Trigger != "manual" {
		t.Fatalf("trigger = %q", j.Trigger)
	}
}

func TestConnectionTestRouteAuthMatrix(t *testing.T) {
	f := seedHostopsOrg(t)

	// Reads are inventory.read: the viewer may read (untested → 404, not 403).
	if resp := requestJSON(t, f.viewerC, http.MethodGet, f.testURL(), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("viewer GET on untested host: %d (want 404)", resp.StatusCode)
	}
	// Writes are inventory.write: the viewer is forbidden everywhere.
	if resp := postJSON(t, f.viewerC, f.testURL(), nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer POST: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, f.viewerC, http.MethodPut, f.pinURL(), map[string]any{"fingerprint": observedKey}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer PUT: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, f.viewerC, http.MethodDelete, f.pinURL(), nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer DELETE: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	// The operator passes inventory.write for both writes.
	if resp := postJSON(t, f.operatorC, f.testURL(), nil); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("operator POST: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

func TestConnectionTestRouteCrossOrgInvisible(t *testing.T) {
	f := seedHostopsOrg(t)
	otherOwner := f.s.SeedUser("hostops-other@example.test", "correct horse battery")
	f.s.SeedOrg("hostops-other", otherOwner)
	other := f.s.LoginAs(otherOwner)

	// The other org's routes name the same host id: a host of another org
	// must be as invisible as one that does not exist.
	foreignTest := f.s.HTTP.URL + "/api/v1/orgs/hostops-other/hosts/" + f.hostID + "/connection-test"
	foreignPin := f.s.HTTP.URL + "/api/v1/orgs/hostops-other/hosts/" + f.hostID + "/host-key"
	if resp := postJSON(t, other, foreignTest, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-org POST: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, other, http.MethodGet, foreignTest, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-org GET: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, other, http.MethodDelete, foreignPin, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-org DELETE: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

func TestGetLatestConnectionTest(t *testing.T) {
	f := seedHostopsOrg(t)

	if resp := requestJSON(t, f.ownerC, http.MethodGet, f.testURL(), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET on untested host: %d", resp.StatusCode)
	}
	// Two outcomes: the newest wins.
	// Microsecond precision, like the Postgres column: an untruncated
	// Linux time.Now() carries nanoseconds and never round-trips Equal.
	at1 := time.Now().Add(-2 * time.Hour).Truncate(time.Microsecond)
	at2 := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
	if err := f.s.Deps.Hosts.RecordConnectionTest(hctx, f.orgID, mustUUID(t, f.hostID), at1, true, observedKey, "", "unpinned",
		[]hosts.HostTestCheck{{Name: "reachability", Detail: "connected", OK: true}}, nil); err != nil {
		t.Fatal(err)
	}
	changedKey := "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	if err := f.s.Deps.Hosts.RecordConnectionTest(hctx, f.orgID, mustUUID(t, f.hostID), at2, false, changedKey, "", "changed", nil, nil); err != nil {
		t.Fatal(err)
	}
	resp := requestJSON(t, f.ownerC, http.MethodGet, f.testURL(), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var listed struct {
		Test struct {
			At           time.Time `json:"at"`
			OK           bool      `json:"ok"`
			HostKey      string    `json:"host_key"`
			HostKeyState string    `json:"host_key_state"`
			PinState     *string   `json:"pin_state"`
		} `json:"test"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if listed.Test.HostKeyState != "changed" || listed.Test.OK || listed.Test.HostKey != changedKey || !listed.Test.At.Equal(at2) {
		t.Fatalf("test = %+v, want the newest changed row", listed.Test)
	}
	if listed.Test.PinState != nil {
		t.Fatalf("unpinned host: test.pin_state = %v, want absent", listed.Test.PinState)
	}
}

// TestGetLatestConnectionTestShowsPinState (backlog 2.43): the connection
// test read carries the host's pin state so the SPA can show whether the
// pin is complete or still needs a confirming test — even when the latest
// test row itself predates the state.
func TestGetLatestConnectionTestShowsPinState(t *testing.T) {
	f := seedHostopsOrg(t)
	hostID := mustUUID(t, f.hostID)

	// A fingerprint-only pin (the pre-00021 shape), with the pin's own
	// fingerprint as the latest test's observation.
	legacyFP, legacyPub := hostKeyPairForAPI(t)
	if err := f.s.Deps.Hosts.SetHostKey(hctx, f.orgID, hostID, legacyFP, legacyPub); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DB.Pool().Exec(hctx,
		`UPDATE host_ssh SET ssh_host_public_key = NULL WHERE org_id = $1 AND host_id = $2`, f.orgID, hostID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Deps.Hosts.RecordConnectionTest(hctx, f.orgID, hostID, time.Now().Truncate(time.Microsecond), true, legacyFP, "", "pinned", nil, nil); err != nil {
		t.Fatal(err)
	}

	var listed struct {
		Test struct {
			PinState *string `json:"pin_state"`
		} `json:"test"`
	}
	decodeInto(t, requestJSON(t, f.ownerC, http.MethodGet, f.testURL(), nil), &listed)
	if listed.Test.PinState == nil || *listed.Test.PinState != "fingerprint_only" {
		t.Fatalf("test.pin_state = %v, want \"fingerprint_only\"", listed.Test.PinState)
	}
}

func TestPutHostKeyOnlyPinsWhatWasSeen(t *testing.T) {
	f := seedHostopsOrg(t)
	seenFP, seenPub := testHostKey(t)

	// No test yet → 422.
	if resp := requestJSON(t, f.ownerC, http.MethodPut, f.pinURL(), map[string]any{"fingerprint": seenFP}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("pin without a test: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	// A test that observed the fingerprint but not the full key (a row
	// from before full keys were recorded) cannot be pinned: 422 asking
	// for a fresh test.
	if err := f.s.Deps.Hosts.RecordConnectionTest(hctx, f.orgID, mustUUID(t, f.hostID), time.Now().Add(-time.Minute), true, seenFP, "", "unpinned", nil, nil); err != nil {
		t.Fatal(err)
	}
	if resp := requestJSON(t, f.ownerC, http.MethodPut, f.pinURL(), map[string]any{"fingerprint": seenFP}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("pin without the observed key: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	// A fresh test observed the key.
	if err := f.s.Deps.Hosts.RecordConnectionTest(hctx, f.orgID, mustUUID(t, f.hostID), time.Now(), true, seenFP, seenPub, "unpinned", nil, nil); err != nil {
		t.Fatal(err)
	}
	// A pin of a key no test observed → 422 (you can only pin what you saw).
	if resp := requestJSON(t, f.ownerC, http.MethodPut, f.pinURL(), map[string]any{"fingerprint": "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("pin of an unobserved key: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	// The observed key pins — fingerprint and the key itself.
	if resp := requestJSON(t, f.ownerC, http.MethodPut, f.pinURL(), map[string]any{"fingerprint": seenFP}); resp.StatusCode != http.StatusOK {
		t.Fatalf("pin: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if stored := storedPin(f.s, f.orgID, mustUUID(t, f.hostID)); stored == nil || *stored != seenFP {
		t.Fatalf("stored pin = %v", stored)
	}
	var storedPub *string
	if err := f.s.DB.Pool().QueryRow(hctx, `SELECT ssh_host_public_key FROM host_ssh WHERE host_id = $1`, mustUUID(t, f.hostID)).Scan(&storedPub); err != nil {
		t.Fatal(err)
	}
	if storedPub == nil || *storedPub != seenPub {
		t.Fatalf("stored pin key = %v, want the observed key", storedPub)
	}
	// DELETE unpins.
	if resp := requestJSON(t, f.ownerC, http.MethodDelete, f.pinURL(), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if stored := storedPin(f.s, f.orgID, mustUUID(t, f.hostID)); stored != nil {
		t.Fatalf("after clear: %v", stored)
	}
}

// testHostKey generates a host key: its fingerprint and authorized-key
// line, what a connection test observes.
func testHostKey(t *testing.T) (string, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return ssh.FingerprintSHA256(sshPub), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
}

func TestPutHostKeyValidatesFingerprintShape(t *testing.T) {
	f := seedHostopsOrg(t)
	if err := f.s.Deps.Hosts.RecordConnectionTest(hctx, f.orgID, mustUUID(t, f.hostID), time.Now(), true, observedKey, "", "unpinned", nil, nil); err != nil {
		t.Fatal(err)
	}
	// huma's body validation rejects a malformed fingerprint with 422
	// before the handler runs.
	if resp := requestJSON(t, f.ownerC, http.MethodPut, f.pinURL(), map[string]any{"fingerprint": "not-a-fingerprint"}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bad shape: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

func TestHostKeyRoutesForeignHostInvisible(t *testing.T) {
	f := seedHostopsOrg(t)
	otherOwner := f.s.SeedUser("hostops-other@example.test", "correct horse battery")
	f.s.SeedOrg("hostops-other", otherOwner)
	other := f.s.LoginAs(otherOwner)
	foreignPin := f.s.HTTP.URL + "/api/v1/orgs/hostops-other/hosts/" + f.hostID + "/host-key"

	if resp := requestJSON(t, other, http.MethodPut, foreignPin, map[string]any{"fingerprint": observedKey}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-org PUT: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, other, http.MethodDelete, foreignPin, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-org DELETE: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

// storedPin reads the pin straight from the database (the pin has no
// dedicated GET route; the host detail's test state is the user surface).
func storedPin(s *apitest.Server, orgID, hostID uuid.UUID) *string {
	var stored *string
	if err := s.DB.Pool().QueryRow(hctx, `SELECT ssh_host_key FROM host_ssh WHERE org_id = $1 AND host_id = $2`, orgID, hostID).Scan(&stored); err != nil {
		s.T.Fatal(err)
	}
	return stored
}

func mustUUID(t *testing.T, id string) uuid.UUID {
	t.Helper()
	parsed, err := uuid.Parse(id)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
