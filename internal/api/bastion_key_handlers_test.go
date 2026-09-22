// The jump host host-key routes' tests (backlog 2.40): the auth matrix,
// the pin-what-you-saw rule against the connection tests' hop observations,
// the DTO's pin fields, validation and cross-org invisibility.
package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type bastionFixture struct {
	s         *apitest.Server
	orgID     uuid.UUID
	ownerC    *http.Client
	operatorC *http.Client
	viewerC   *http.Client
	bastionID uuid.UUID
	// The generated key pair the tests pin.
	fp  string
	pub string
}

// bastionFP and bastionPub are the generated key pair the tests pin
	// (set by seedBastionOrg, the same pair for every fixture instance).
var (
	bastionFP  string
	bastionPub string
)

func seedBastionOrg(t *testing.T) bastionFixture {
	// A real key pair: the fingerprint and authorized-key line a test's
	// hop entry carries, in the shape pin-what-you-saw validates.
	bastionFP, bastionPub = testHostKey(t)
	t.Helper()
	s := apitest.New(t)
	owner := s.SeedUser("bastionpin-owner@example.test", "correct horse battery")
	operator := s.SeedUser("bastionpin-operator@example.test", "correct horse battery")
	viewer := s.SeedUser("bastionpin-viewer@example.test", "correct horse battery")
	org := s.SeedOrg("bastionpin-org", owner)
	s.AddMember(org, operator, tenancy.RoleOperator)
	s.AddMember(org, viewer, tenancy.RoleViewer)
	b, err := s.Deps.SSH.CreateBastion(hctx, org, sshconf.BastionInput{
		Name: "edge", Address: "10.0.0.9", Port: 22, Username: "ops",
	})
	if err != nil {
		t.Fatal(err)
	}
	return bastionFixture{
		s: s, orgID: org, ownerC: s.LoginAs(owner), operatorC: s.LoginAs(operator), viewerC: s.LoginAs(viewer),
		bastionID: b.ID, fp: bastionFP, pub: bastionPub,
	}
}

func (f bastionFixture) pinURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/bastionpin-org/bastions/" + f.bastionID.String() + "/host-key"
}

func (f bastionFixture) getURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/bastionpin-org/bastions/" + f.bastionID.String()
}

// observeHop appends one connection-test row whose hop keys carry the given
// observation for the fixture's bastion.
func (f bastionFixture) observe(t *testing.T, hostID uuid.UUID, fingerprint, publicKey, state string) {
	t.Helper()
	if err := f.s.Deps.Hosts.RecordConnectionTest(hctx, f.orgID, hostID, nowTrunc(), true,
		"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "", "unpinned", nil,
		[]hosts.HopKey{{BastionID: f.bastionID, Fingerprint: fingerprint, PublicKey: publicKey, State: state}}); err != nil {
		t.Fatal(err)
	}
}

// oneZone returns the org's default zone (the startup reconciliation
// provisions exactly one).
func (f bastionFixture) oneZone(t *testing.T) uuid.UUID {
	t.Helper()
	zones, err := f.s.Deps.Hosts.ListZones(hctx, f.orgID)
	if err != nil {
		t.Fatal(err)
	}
	if len(zones) == 0 {
		t.Fatal("no zones seeded")
	}
	return zones[0].ID
}

func TestBastionKeyPinAuthMatrix(t *testing.T) {
	f := seedBastionOrg(t)

	// Writes are inventory.write: the viewer is forbidden everywhere.
	if resp := requestJSON(t, f.viewerC, http.MethodPut, f.pinURL(), map[string]any{"fingerprint": bastionFP}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer PUT: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, f.viewerC, http.MethodDelete, f.pinURL(), nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer DELETE: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	// Reads are inventory.read: the viewer may read the pin fields.
	if resp := requestJSON(t, f.viewerC, http.MethodGet, f.getURL(), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer GET: %d", resp.StatusCode)
	}
	// The operator passes inventory.write.
	if resp := requestJSON(t, f.operatorC, http.MethodDelete, f.pinURL(), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("operator DELETE: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

func TestBastionKeyPinOnlyWhatYouSaw(t *testing.T) {
	f := seedBastionOrg(t)
	host, err := f.s.Deps.Hosts.CreateHost(hctx, f.orgID, hosts.HostInput{Name: "web-01", Address: "10.0.0.1", ZoneID: f.oneZone(t), Managed: true})
	if err != nil {
		t.Fatal(err)
	}

	// No observation yet: 422 with the connection-test remedy.
	if resp := requestJSON(t, f.ownerC, http.MethodPut, f.pinURL(), map[string]any{"fingerprint": bastionFP}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("pin before any observation: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	// The test observed a different fingerprint: 422.
	f.observe(t, host.ID, "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", bastionPub, "unpinned")
	if resp := requestJSON(t, f.ownerC, http.MethodPut, f.pinURL(), map[string]any{"fingerprint": bastionFP}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("pin of a fingerprint no test observed: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	// A test whose hop entry arrived without the full key cannot be
	// pinned: 422 asking for a fresh test.
	f.observe(t, host.ID, bastionFP, "", "unpinned")
	if resp := requestJSON(t, f.ownerC, http.MethodPut, f.pinURL(), map[string]any{"fingerprint": bastionFP}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("pin without the observed key: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	// A fresh test observed the key and the hop state is unpinned: the
	// operator pins it.
	f.observe(t, host.ID, bastionFP, bastionPub, "unpinned")
	if resp := requestJSON(t, f.ownerC, http.MethodPut, f.pinURL(), map[string]any{"fingerprint": bastionFP}); resp.StatusCode != http.StatusOK {
		t.Fatalf("pin: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	// The pin fields travel on the read: pinned fingerprint, full state,
	// the observation still reported.
	var got bastionGetOut
	requestDecoded(t, f.ownerC, http.MethodGet, f.getURL(), &got)
	if got.Bastion.SSHHostKey == nil || *got.Bastion.SSHHostKey != bastionFP {
		t.Fatalf("pinned fingerprint = %+v, want %s", got.Bastion.SSHHostKey, bastionFP)
	}
	if got.Bastion.PinState == nil || *got.Bastion.PinState != hosts.BastionPinStateFull {
		t.Fatalf("pin state = %+v, want full", got.Bastion.PinState)
	}
	if got.Bastion.ObservedKey == nil || *got.Bastion.ObservedKey != bastionFP {
		t.Fatalf("observed key = %+v, want %s", got.Bastion.ObservedKey, bastionFP)
	}

	// A test whose hop entry reads pinned confirms the pin — a
	// fingerprint-only pin would be completed here (the full key rides
	// the observation); the second pin over an existing one stores the
	// same values (pin-what-you-saw again).
	f.observe(t, host.ID, bastionFP, bastionPub, "pinned")

	// Unpin, then the fields are absent again.
	if resp := requestJSON(t, f.ownerC, http.MethodDelete, f.pinURL(), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("unpin: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	got = bastionGetOut{}
	requestDecoded(t, f.ownerC, http.MethodGet, f.getURL(), &got)
	if got.Bastion.SSHHostKey != nil || got.Bastion.PinState != nil {
		t.Fatalf("pin fields after unpin = %v/%v, want absent", got.Bastion.SSHHostKey, got.Bastion.PinState)
	}
	// The observation still travels after the pin is gone (it is the test
	// history, not the pin).
	if got.Bastion.ObservedKey == nil {
		t.Fatal("the observed key must survive the unpin")
	}
}

func TestBastionPinFingerprintOnlyObservationCompletes(t *testing.T) {
	f := seedBastionOrg(t)
	host, err := f.s.Deps.Hosts.CreateHost(hctx, f.orgID, hosts.HostInput{Name: "web-01", Address: "10.0.0.1", ZoneID: f.oneZone(t), Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	// A fingerprint-only pin — the pre-migration shape (2.43) — is seeded
	// directly (the API never makes one); a confirming test's hop
	// observation completes it with the observed key.
	if _, err := f.s.DB.Pool().Exec(hctx,
		`UPDATE bastions SET ssh_host_key = $2, ssh_host_public_key = NULL WHERE org_id = $1 AND id = $3`,
		f.orgID, bastionFP, f.bastionID); err != nil {
		t.Fatalf("seed fingerprint-only pin: %v", err)
	}
	f.observe(t, host.ID, bastionFP, bastionPub, "pinned")
	pins, err := f.s.Deps.Hosts.PinnedBastionPublicKeys(hctx, f.orgID, []uuid.UUID{f.bastionID})
	if err != nil {
		t.Fatal(err)
	}
	if pins[f.bastionID] != bastionPub {
		t.Fatalf("backfill = %q, want the observed key", pins[f.bastionID])
	}
	states, err := f.s.Deps.Hosts.BastionPinStates(hctx, f.orgID, []uuid.UUID{f.bastionID})
	if err != nil {
		t.Fatal(err)
	}
	if states[f.bastionID] != hosts.BastionPinStateFull {
		t.Fatalf("pin state = %q, want full", states[f.bastionID])
	}
}

func TestBastionKeyPinCrossOrgInvisible(t *testing.T) {
	f := seedBastionOrg(t)
	otherOwner := f.s.SeedUser("bastionpin-other@example.test", "correct horse battery")
	f.s.SeedOrg("bastionpin-other", otherOwner)
	other := f.s.LoginAs(otherOwner)

	foreignPin := f.s.HTTP.URL + "/api/v1/orgs/bastionpin-other/bastions/" + f.bastionID.String() + "/host-key"
	if resp := requestJSON(t, other, http.MethodPut, foreignPin, map[string]any{"fingerprint": bastionFP}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-org pin: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, other, http.MethodDelete, foreignPin, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-org unpin: %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	// The pin itself did not happen.
	pins, err := f.s.Deps.Hosts.PinnedBastionKeys(hctx, f.orgID, []uuid.UUID{f.bastionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 0 {
		t.Fatalf("a cross-org pin leaked through: %v", pins)
	}
}

// bastionKeyDTOBooleans guards the hop observation's validation at the
// store level: a hop entry whose key does not match its own fingerprint is
// dropped rather than stored.
func TestHopKeyObservationSanitization(t *testing.T) {
	f := seedBastionOrg(t)
	host, err := f.s.Deps.Hosts.CreateHost(hctx, f.orgID, hosts.HostInput{Name: "web-01", Address: "10.0.0.1", ZoneID: f.oneZone(t), Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.Deps.Hosts.RecordConnectionTest(hctx, f.orgID, host.ID, nowTrunc(), true,
		"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "", "unpinned", nil,
		[]hosts.HopKey{
			{BastionID: f.bastionID, Fingerprint: bastionFP, PublicKey: "not a key", State: "pinned"},
			{BastionID: f.bastionID, Fingerprint: "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", PublicKey: "", State: "weird"},
			{BastionID: f.bastionID, Fingerprint: bastionFP, PublicKey: bastionPub, State: "pinned"},
		}); err != nil {
		t.Fatal(err)
	}
	obs, err := f.s.Deps.Hosts.LatestBastionObservation(hctx, f.orgID, f.bastionID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.State != hosts.KeyStatePinned || obs.Fingerprint != bastionFP {
		t.Fatalf("observation = %+v, want the valid entry kept", obs)
	}
	if obs.PublicKey != bastionPub {
		t.Fatalf("observation key = %q, want %q", obs.PublicKey, bastionPub)
	}
}

// bastionGetOut is the narrow mirror of the bastion read's body.
type bastionGetOut struct {
	Bastion struct {
		ID            string     `json:"id"`
		SSHHostKey    *string    `json:"ssh_host_key"`
		PinState      *string    `json:"pin_state"`
		ObservedKey   *string    `json:"observed_key"`
		ObservedState *string    `json:"observed_state"`
		ObservedAt    *time.Time `json:"observed_at"`
	} `json:"bastion"`
}

// requestDecoded issues a GET and decodes the body into out.
func requestDecoded(t *testing.T, c *http.Client, method, url string, out any) {
	t.Helper()
	resp := requestJSON(t, c, method, url, nil)
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode %s %s: %v", method, url, err)
	}
}

// nowTrunc is a microsecond-precise timestamp, like the Postgres column.
func nowTrunc() time.Time { return time.Now().Truncate(time.Microsecond) }
