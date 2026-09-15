// The bootstrap and rotate routes' tests (auth matrix, validation, the
// never-stored rule for the one-time login, the audit trail and the
// immediate re-key), driven through the full API against the test
// database.
package api_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// sysBoxKey is the same fixed master key apitest seals with.
var sysBoxKey = [32]byte{'p', 'u', 'l', 's', 'i', 'x', '-', 't', 'e', 's', 't', '-', 'k', 'e', 'y'}

// sysFixture is the seeded org: owner, operator, viewer, one managed host
// with its own ssh_key credential, and the clients the auth matrix drives.
type sysFixture struct {
	s         *apitest.Server
	orgID     uuid.UUID
	ownerC    *http.Client
	operatorC *http.Client
	viewerC   *http.Client
	hostID    string
	credID    string
}

func seedSysOrg(t *testing.T) sysFixture {
	t.Helper()
	s := apitest.New(t)
	owner := s.SeedUser("sys-owner@example.test", "correct horse battery")
	operator := s.SeedUser("sys-operator@example.test", "correct horse battery")
	viewer := s.SeedUser("sys-viewer@example.test", "correct horse battery")
	org := s.SeedOrg("sys-org", owner)
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
	// The host's own ssh_key credential: the bootstrap installs THAT
	// credential's public key.
	keyCred, err := s.Deps.Credentials.Create(hctx, org, credentials.CreateInput{
		Name: "web-01-key", Kind: credentials.KindSSHKey, Username: "ansible", Material: credentials.Material{Generate: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deps.SSH.SetHostSSH(hctx, org, created.ID, sshconf.HostSSHInput{
		CredentialID: &keyCred.ID, Port: 22, BastionMode: sshconf.ModeInherit,
	}); err != nil {
		t.Fatal(err)
	}
	return sysFixture{
		s: s, orgID: org, ownerC: s.LoginAs(owner), operatorC: s.LoginAs(operator), viewerC: s.LoginAs(viewer),
		hostID: created.ID.String(), credID: keyCred.ID.String(),
	}
}

func (f sysFixture) bootstrapURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/sys-org/hosts/" + f.hostID + "/bootstrap"
}

func (f sysFixture) rotateURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/sys-org/credentials/" + f.credID + "/rotate"
}

func TestBootstrapRouteEnqueuesSealedJob(t *testing.T) {
	f := seedSysOrg(t)

	resp := postJSON(t, f.ownerC, f.bootstrapURL(), map[string]any{"user": "root", "password": "one-time-pw-9"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("bootstrap: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var started struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&started); err != nil {
		t.Fatal(err)
	}
	j, err := f.s.Deps.Jobs.Get(hctx, f.orgID, mustUUID(t, started.JobID))
	if err != nil {
		t.Fatalf("job: %v", err)
	}
	if string(j.Kind) != "bootstrap" || len(j.HostIDs) != 1 || j.HostIDs[0].String() != f.hostID {
		t.Fatalf("job = %+v", j)
	}

	// The stored payload is the sealed string form; the one-time login
	// rides only inside the ciphertext, under the job row's AAD.
	var b64 string
	if err := json.Unmarshal(j.Payload, &b64); err != nil {
		t.Fatalf("payload = %q, want the sealed string form", j.Payload)
	}
	sealed, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := crypto.NewBox(sysBoxKey).OpenAAD(sealed, jobs.JobAAD(f.orgID, j.ID))
	if err != nil {
		t.Fatalf("open sealed payload: %v", err)
	}
	var stored struct {
		CredentialID string             `json:"credential_id"`
		LoginUser    string             `json:"login_user"`
		Login        credentials.Secret `json:"login"`
	}
	if err := json.Unmarshal(plain, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.CredentialID != f.credID || stored.LoginUser != "root" || stored.Login.Password != "one-time-pw-9" {
		t.Fatalf("sealed payload = %+v", stored)
	}

	// The body is never stored: the row's own columns and the audit trail
	// carry nothing but ids and summaries.
	for _, blob := range []string{string(j.Payload), j.Error} {
		if strings.Contains(blob, "one-time-pw-9") {
			t.Errorf("the one-time password leaked at rest: %q", blob)
		}
	}
	// The body is never stored: the audit event names the host and never
	// the body's one-time login.
	activity := readActivity(t, f.s, f.ownerC, "sys-org")
	bootstrapped := false
	for _, ev := range activity.Items {
		if ev.Action == "POST bootstrapHost" && ev.ObjectType == "host" {
			bootstrapped = true
		}
		if strings.Contains(ev.Summary, "one-time-pw-9") || strings.Contains(ev.Action, "one-time-pw-9") {
			t.Errorf("audit event leaks the one-time password: %+v", ev)
		}
	}
	if !bootstrapped {
		t.Fatalf("no bootstrap event in %+v", activity.Items)
	}
}

func TestBootstrapRouteAuthMatrixAndValidation(t *testing.T) {
	f := seedSysOrg(t)

	// Viewer: 403.
	if resp := postJSON(t, f.viewerC, f.bootstrapURL(), map[string]any{"user": "root", "password": "pw"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer: status %d", resp.StatusCode)
	}
	// Operator and owner: 202.
	for name, c := range map[string]*http.Client{"operator": f.operatorC, "owner": f.ownerC} {
		resp := postJSON(t, c, f.bootstrapURL(), map[string]any{"user": "root", "password": "pw"})
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("%s: status %d, body %s", name, resp.StatusCode, mustReadBody(t, resp))
		}
	}

	// Neither credential: 400.
	if resp := postJSON(t, f.ownerC, f.bootstrapURL(), map[string]any{"user": "root"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no credential: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	// Both credentials: 400.
	if resp := postJSON(t, f.ownerC, f.bootstrapURL(), map[string]any{"user": "root", "password": "a", "private_key": "b"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("both credentials: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	// Empty user: 422 (huma's minLength).
	if resp := postJSON(t, f.ownerC, f.bootstrapURL(), map[string]any{"user": "", "password": "pw"}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("empty user: status %d", resp.StatusCode)
	}
	// Oversized user: 422 (huma's maxLength).
	if resp := postJSON(t, f.ownerC, f.bootstrapURL(), map[string]any{"user": strings.Repeat("u", 65), "password": "pw"}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("long user: status %d", resp.StatusCode)
	}
}

func TestBootstrapRouteValidatesTheHost(t *testing.T) {
	f := seedSysOrg(t)

	// An unmanaged host: 400.
	zones, err := f.s.Deps.Hosts.ListZones(hctx, f.orgID)
	if err != nil {
		t.Fatal(err)
	}
	unmanaged, err := f.s.Deps.Hosts.CreateHost(hctx, f.orgID, hosts.HostInput{Name: "bare-01", Address: "10.0.0.2", ZoneID: zones[0].ID, Managed: false})
	if err != nil {
		t.Fatal(err)
	}
	resp := postJSON(t, f.ownerC, f.s.HTTP.URL+"/api/v1/orgs/sys-org/hosts/"+unmanaged.ID.String()+"/bootstrap",
		map[string]any{"user": "root", "password": "pw"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unmanaged host: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	// A managed host with no ssh_key credential of its own: 422.
	keyless, err := f.s.Deps.Hosts.CreateHost(hctx, f.orgID, hosts.HostInput{Name: "keyless-01", Address: "10.0.0.3", ZoneID: zones[0].ID, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	resp = postJSON(t, f.ownerC, f.s.HTTP.URL+"/api/v1/orgs/sys-org/hosts/"+keyless.ID.String()+"/bootstrap",
		map[string]any{"user": "root", "password": "pw"})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("keyless host: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	// A host of another organization is invisible: the org-scoped route
	// refuses a caller that is not a member before resolving any host.
	other := apitest.New(t)
	otherOwner := other.SeedUser("sys-foreign-owner@example.test", "correct horse battery")
	other.SeedOrg("sys-foreign", otherOwner)
	resp = postJSON(t, other.LoginAs(otherOwner), f.bootstrapURL(),
		map[string]any{"user": "root", "password": "pw"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign host: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

func TestRotateRouteStartsTheRotationGroup(t *testing.T) {
	f := seedSysOrg(t)

	resp := postJSON(t, f.ownerC, f.rotateURL(), nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("rotate: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var started struct {
		JobID    string `json:"job_id"`
		JobCount int    `json:"job_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&started); err != nil {
		t.Fatal(err)
	}
	// One host target → one run plus its follow-up connection_test.
	if started.JobCount != 2 || started.JobID == "" {
		t.Fatalf("202 body = %+v, want the run plus its verification test", started)
	}
	j, err := f.s.Deps.Jobs.Get(hctx, f.orgID, mustUUID(t, started.JobID))
	if err != nil {
		t.Fatalf("job: %v", err)
	}
	if string(j.Kind) != "rotate_key" || j.RunGroupID == uuid.Nil {
		t.Fatalf("job = %+v", j)
	}
	// The group's second job is the follow-up connection_test in the same
	// run group.
	testKind := jobs.KindConnectionTest
	list, _, err := f.s.Deps.Jobs.List(hctx, f.orgID, jobs.Filter{Kind: &testKind}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	var testJob *jobs.Job
	for i := range list {
		if list[i].RunGroupID == j.RunGroupID {
			testJob = &list[i]
		}
	}
	if testJob == nil || len(testJob.HostIDs) != 1 || testJob.HostIDs[0].String() != f.hostID {
		t.Fatalf("the rotation group carries no follow-up test for the host: %+v", list)
	}
	// The rotate event is audited with a summary that names no secret.
	activity := readActivity(t, f.s, f.ownerC, "sys-org")
	found := false
	for _, ev := range activity.Items {
		if ev.Action == "POST rotateCredential" && ev.ObjectType == "credential" && ev.ObjectID == f.credID {
			found = true
		}
	}
	if !found {
		t.Fatalf("no rotate event in %+v", activity.Items)
	}
}

func TestRotateRouteImmediateReKey(t *testing.T) {
	f := seedSysOrg(t)

	// A credential nothing references re-keys immediately: 202 with an
	// empty job id and count 0, and the new private key sealed on.
	unused, err := f.s.Deps.Credentials.Create(hctx, f.orgID, credentials.CreateInput{
		Name: "unused-key", Kind: credentials.KindSSHKey, Username: "ansible", Material: credentials.Material{Generate: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	oldSecret, err := f.s.Deps.Credentials.OpenSecret(hctx, f.orgID, unused.ID)
	if err != nil {
		t.Fatal(err)
	}
	resp := postJSON(t, f.ownerC, f.s.HTTP.URL+"/api/v1/orgs/sys-org/credentials/"+unused.ID.String()+"/rotate", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("unused key rotate: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var immediate struct {
		JobID    string `json:"job_id"`
		JobCount int    `json:"job_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&immediate); err != nil {
		t.Fatal(err)
	}
	if immediate.JobID != "" || immediate.JobCount != 0 {
		t.Fatalf("immediate re-key body = %+v", immediate)
	}
	newSecret, err := f.s.Deps.Credentials.OpenSecret(hctx, f.orgID, unused.ID)
	if err != nil {
		t.Fatal(err)
	}
	if newSecret.PrivateKey == "" || newSecret.PrivateKey == oldSecret.PrivateKey {
		t.Fatal("the unused credential did not re-key")
	}
}

func TestRotateRouteAuthMatrixAndNotFound(t *testing.T) {
	f := seedSysOrg(t)

	// Viewer: 403.
	if resp := postJSON(t, f.viewerC, f.rotateURL(), nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer: status %d", resp.StatusCode)
	}
	// Operator: credentials.write is admin-only → 403.
	if resp := postJSON(t, f.operatorC, f.rotateURL(), nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator: status %d", resp.StatusCode)
	}
	// Owner: 202.
	if resp := postJSON(t, f.ownerC, f.rotateURL(), nil); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("owner: status %d", resp.StatusCode)
	}

	// An unknown credential is 404.
	resp := postJSON(t, f.ownerC, f.s.HTTP.URL+"/api/v1/orgs/sys-org/credentials/"+uuid.Must(uuid.NewV7()).String()+"/rotate", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown credential: status %d", resp.StatusCode)
	}

	// A password credential does not rotate: 400.
	pw, err := f.s.Deps.Credentials.Create(hctx, f.orgID, credentials.CreateInput{
		Name: "unused-pw", Kind: credentials.KindSSHPassword, Username: "x", Material: credentials.Material{Password: "pw"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp = postJSON(t, f.ownerC, f.s.HTTP.URL+"/api/v1/orgs/sys-org/credentials/"+pw.ID.String()+"/rotate", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("password credential: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

// readActivity lists the org's activity feed with the owner's own client
// (the audit route gates on audit.read, which the owner role carries).
func readActivity(t *testing.T, s *apitest.Server, owner *http.Client, slug string) activityDTO {
	t.Helper()
	resp := requestJSON(t, owner, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/"+slug+"/activity", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get activity: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	return decodeActivity(t, resp)
}
