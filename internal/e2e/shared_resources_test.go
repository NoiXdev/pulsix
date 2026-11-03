// The v0.2f milestone gate (backlog 2.90): an instance admin defines a
// repository ONCE and it is usable by every organization, while what makes
// it usable — a credential able to reach it — stays entirely the
// customer's own. This is the sub-project's own end-to-end scenario, over
// the real HTTP API, a real worker claim loop and a real git-over-SSH
// clone for each of two organizations, proving the design's central claim
// is structural rather than a policy: a shared definition carries no
// secret by construction, so there is nothing for an organization to leak
// to another one even when both point at the exact same url.
//
// Deliberately real, not a shortcut: two organizations, each with its own
// deploy credential, its own host-login credential and its own managed SSH
// host, each cloning the SAME upstream repository through the SAME git
// server fixture with a DIFFERENT key, each running a real playbook that
// drops its own marker through the connection it was actually given. A
// scenario that only showed the repository appearing in both organizations
// would prove the convenience; this one also proves the safety — the
// brief's own words for why the leak assertions at the bottom are the part
// that matters most.
//
// Org A is owned by the same instance administrator the setup wizard
// creates (the same shape every other scenario in this package already
// uses). Org B is deliberately handed to a SECOND, independent user over a
// real invite (member_handlers.go's own accept-without-mail path), not
// another organization the admin happens to also own: the isolation this
// test exists to demonstrate is most convincing when it is two different
// people who could, in production, never see each other's organization at
// all — not merely two rows the same operator could tell apart by hand.
package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/ssh"

	"github.com/noixdev/pulsix/internal/app"
	"github.com/noixdev/pulsix/internal/config"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/setup"
	"github.com/noixdev/pulsix/internal/worker"
	"github.com/noixdev/pulsix/internal/worker/runner"
)

// repoFields is the flat shape both the list and (nested under "repo") the
// single-GET/PUT routes carry for one repository row — enough of it to
// drive the scenario and to search for a leak: id to address it, the three
// shared fields to prove propagation and the url refusal, credential_id
// and shared_repo_id to prove the projection marker and the isolation.
type repoFields struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	URL          string `json:"url"`
	Branch       string `json:"branch"`
	Subpath      string `json:"subpath"`
	CredentialID string `json:"credential_id"`
	SharedRepoID string `json:"shared_repo_id"`
}

type repoGetOut struct {
	Repo repoFields `json:"repo"`
}

type repoListOut struct {
	Repos []repoFields `json:"repos"`
}

// repoDefOut decodes an instance-admin repository DEFINITION response
// (shared_handlers.go), distinct from repoGetOut: a definition has no
// credential and no shared_repo_id of its own — it IS what a projection's
// shared_repo_id points at.
type repoDefOut struct {
	Definition struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		URL    string `json:"url"`
		Branch string `json:"branch"`
	} `json:"definition"`
}

// sharedTenant is one organization's own slice of the scenario: its own
// credentials, its own managed host, its own worker claim loop — every
// piece that must never be visible from the other tenant.
type sharedTenant struct {
	t   *testing.T
	api *apiClient // the organization's OWN session (never the other tenant's)
	org string

	hostLoginCred string
	deployCred    string
	hostLoginPub  ssh.PublicKey
	deployPub     ssh.PublicKey

	zoneID string
	hostID string
	host   *hostSSHServer
}

func (ten *sharedTenant) prefix() string { return "/api/v1/orgs/" + ten.org }

func (ten *sharedTenant) get(path string, out any) { ten.api.do(http.MethodGet, path, nil, out) }
func (ten *sharedTenant) post(path string, body, out any) {
	ten.api.do(http.MethodPost, path, body, out)
}
func (ten *sharedTenant) put(path string, body, out any) { ten.api.do(http.MethodPut, path, body, out) }

// setupSharedTenant wires one organization's own, real infrastructure: its
// credentials, its managed host (direct, no bastion — spec §5's own "none"
// route; this scenario's point is the credential boundary, not the routing
// one ansible_test.go already covers end to end) and a worker claim loop
// enrolled for exactly this organization, dialing the one shared manager.
func setupSharedTenant(t *testing.T, ctx context.Context, cancel context.CancelFunc, api *apiClient, org string, masterKey [32]byte, managerAddr, runnerBin, ansibleBin string) *sharedTenant {
	t.Helper()
	ten := &sharedTenant{t: t, api: api, org: org}
	prefix := ten.prefix()

	var zonesOut struct {
		Zones []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"zones"`
	}
	ten.get(prefix+"/zones", &zonesOut)
	for _, z := range zonesOut.Zones {
		if z.Slug == "default" {
			ten.zoneID = z.ID
		}
	}
	if ten.zoneID == "" {
		t.Fatalf("organization %s carries no default zone after provisioning", org)
	}

	cred := func(name, kind string) (string, ssh.PublicKey) {
		t.Helper()
		var out credentialOut
		ten.post(prefix+"/credentials", map[string]any{"name": name, "kind": kind, "generate": true}, &out)
		if out.Credential.PublicKey == "" {
			t.Fatalf("%s's %s credential carries no public key", org, name)
		}
		return out.Credential.ID, parseAuthorizedKey(t, out.Credential.PublicKey)
	}
	ten.hostLoginCred, ten.hostLoginPub = cred("host-login", "ssh_key")
	// This organization's OWN deploy key (spec decision 3): the shared
	// definition itself will carry none.
	ten.deployCred, ten.deployPub = cred("deploy-key", "git_key")

	bindHost := loopbackHost(t)
	ten.host = startHostSSHServer(t, ten.hostLoginPub, bindHost)
	var hostOut hostOut
	ten.post(prefix+"/hosts", map[string]any{
		"name": org + "-host", "zone_id": ten.zoneID, "address": ten.host.host, "managed": true,
	}, &hostOut)
	ten.hostID = hostOut.Host.ID
	var sshOut struct {
		SSH struct {
			CredentialID string `json:"credential_id"`
		} `json:"ssh"`
	}
	ten.put(prefix+"/hosts/"+ten.hostID+"/ssh", map[string]any{
		"username": "ansible", "port": ten.host.port, "credential_id": ten.hostLoginCred,
	}, &sshOut)
	if sshOut.SSH.CredentialID != ten.hostLoginCred {
		t.Fatalf("%s's host ssh credential = %q, want %q", org, sshOut.SSH.CredentialID, ten.hostLoginCred)
	}

	identity := enrollWithCLI(t, ctx, masterKey, org)
	workerCfg := worker.Config{
		ManagerAddr:      managerAddr,
		CertPath:         identity.CertPath,
		KeyPath:          identity.KeyPath,
		CAPath:           identity.CAPath,
		DataDir:          t.TempDir(),
		Zones:            []uuid.UUID{uuid.MustParse(ten.zoneID)},
		AnsibleRunnerBin: runnerBin,
		AnsibleBin:       ansibleBin,
		ClaimInterval:    200 * time.Millisecond,
		Version:          "e2e",
	}
	conn, err := worker.Dial(ctx, workerCfg)
	if err != nil {
		t.Fatalf("dial worker for %s: %v", org, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	runExec := runner.NewRunner(runnerBin, ansibleBin, "git", 0, testLogger(t))
	loop := worker.NewLoop(workerCfg, workerpb.NewWorkerServiceClient(conn),
		worker.NewCompositeExecutor(map[string]worker.Executor{
			"run":       runExec,
			"sync_repo": worker.NewSyncExecutor(runExec, "ansible-galaxy"),
		}),
		worker.WithLogger(testLogger(t)),
	)
	loopDone := make(chan struct{})
	go func() { defer close(loopDone); _ = loop.Run(ctx) }()
	t.Cleanup(func() {
		cancel() // the loop's clean shutdown: cancellation, not connection loss
		select {
		case <-loopDone:
		case <-time.After(5 * time.Second):
			t.Logf("%s's worker loop did not stop in time", org)
		}
	})
	return ten
}

// soleRepo asserts the organization carries exactly one repository (the
// shared definition's own projection) and returns it.
func (ten *sharedTenant) soleRepo() repoFields {
	ten.t.Helper()
	var list repoListOut
	ten.get(ten.prefix()+"/repos", &list)
	if len(list.Repos) != 1 {
		ten.t.Fatalf("%s's repos = %+v, want exactly one projection", ten.org, list.Repos)
	}
	return list.Repos[0]
}

// bindCredentialAndSync supplies this organization's own deploy credential
// to its projection and syncs it — a REAL git-over-SSH clone, authenticated
// with this organization's own key and nobody else's.
func (ten *sharedTenant) bindCredentialAndSync(repo repoFields) {
	ten.t.Helper()
	prefix := ten.prefix()
	var updated repoGetOut
	ten.put(prefix+"/repos/"+repo.ID, map[string]any{
		"name": repo.Name, "url": repo.URL, "branch": repo.Branch,
		"subpath": repo.Subpath, "credential_id": ten.deployCred,
	}, &updated)
	if updated.Repo.CredentialID != ten.deployCred {
		ten.t.Fatalf("%s's repo credential after bind = %q, want %q", ten.org, updated.Repo.CredentialID, ten.deployCred)
	}

	var syncOut repoSyncOut
	ten.post(prefix+"/repos/"+repo.ID+"/sync", struct{}{}, &syncOut)
	job := ten.awaitJob(syncOut.Job.ID, 60*time.Second)
	if job.Job.Status != "succeeded" {
		ten.t.Fatalf("%s's repo sync = %q (error %q)", ten.org, job.Job.Status, job.Job.Error)
	}
}

// registerAndRun registers this organization's own playbook row for path
// in the already-synced repository, runs it scoped to this organization's
// own host with its own greeting extra var, and asserts the run actually
// produced that greeting — not merely that the API returned no error (the
// sub-project's own stated lesson: a test that stops one step before the
// action is how a real defect survives).
func (ten *sharedTenant) registerAndRun(repoID, path, greeting string) jobDetailOut {
	ten.t.Helper()
	prefix := ten.prefix()

	var pb playbookOut
	ten.post(prefix+"/playbooks", map[string]any{"repo_id": repoID, "path": path}, &pb)
	if pb.Playbook.Source != "repo" {
		ten.t.Fatalf("%s's playbook source = %q, want repo", ten.org, pb.Playbook.Source)
	}

	var tpl templateOut
	ten.post(prefix+"/job-templates", map[string]any{
		"name": "hello-" + ten.org, "playbook_id": pb.Playbook.ID,
		"scope":      map[string]any{"host_ids": []string{ten.hostID}},
		"extra_vars": map[string]any{"greeting": greeting},
	}, &tpl)

	var runOut jobStartOut
	ten.post(prefix+"/job-templates/"+tpl.Template.ID+"/run", struct{}{}, &runOut)
	if len(runOut.Jobs) != 1 {
		ten.t.Fatalf("run of %s's hello template created %d job(s), want 1", ten.org, len(runOut.Jobs))
	}

	run := ten.awaitJob(runOut.Jobs[0].ID, 90*time.Second)
	if run.Job.Status != "succeeded" {
		ten.t.Fatalf("%s's hello run = %q (error %q)", ten.org, run.Job.Status, run.Job.Error)
	}

	events := ten.awaitEvents(run.Job.ID, 30*time.Second, func(e jobEventsOut) bool {
		return hasEvent(e, "ok", taskGreeting) && hasEvent(e, "run_end", "")
	})
	sawGreeting := false
	for _, ev := range events.Events {
		if ev.State == "ok" && ev.Task == taskGreeting {
			raw, _ := json.Marshal(ev.Payload)
			if strings.Contains(string(raw), greeting) {
				sawGreeting = true
			}
		}
	}
	if !sawGreeting {
		ten.t.Fatalf("%s's hello run produced no %q event carrying %q — the API said succeeded but the playbook's own output disagrees", ten.org, taskGreeting, greeting)
	}
	return run
}

func (ten *sharedTenant) awaitJob(id string, timeout time.Duration) jobDetailOut {
	ten.t.Helper()
	path := ten.prefix() + "/jobs/" + id
	deadline := time.Now().Add(timeout)
	for {
		var out jobDetailOut
		ten.get(path, &out)
		if isTerminal(out.Job.Status) {
			return out
		}
		if time.Now().After(deadline) {
			ten.t.Fatalf("%s's job %s did not finish in %s; last status %q", ten.org, id, timeout, out.Job.Status)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (ten *sharedTenant) awaitEvents(jobID string, timeout time.Duration, want func(e jobEventsOut) bool) jobEventsOut {
	ten.t.Helper()
	path := ten.prefix() + "/jobs/" + jobID + "/events?after=0"
	deadline := time.Now().Add(timeout)
	for {
		var out jobEventsOut
		ten.get(path, &out)
		if want(out) {
			return out
		}
		if time.Now().After(deadline) {
			raw, _ := json.Marshal(out)
			ten.t.Fatalf("%s's job %s events did not match within %s; got %s", ten.org, jobID, timeout, raw)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestSharedRepositoryEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("no docker for the testcontainer database: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// --- one manager, the same full production wiring every other scenario
	// in this package uses ------------------------------------------------
	httpLis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcLis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	masterKey := [32]byte{'e', '2', 'e', '-', 's', 'h', 'a', 'r', 'e', 'd', '-', 'r', 'e', 's', '0', '1'}
	base := "http://localhost:" + portOf(httpLis.Addr().String())
	cfg := config.Config{
		Listen:         httpLis.Addr().String(),
		DatabaseURL:    testdb.URL(t),
		PublicURL:      base,
		MasterKey:      masterKey,
		GRPCListen:     "localhost:0",
		GRPCPublicAddr: "localhost:" + portOf(grpcLis.Addr().String()),
		RetestInterval: 24 * time.Hour,
		JobMaxAttempts: config.DefaultJobMaxAttempts,
		Version:        "e2e",
	}
	a, err := app.Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)

	go func() { _ = a.GRPC.Serve(grpcLis) }()
	t.Cleanup(func() {
		stopped := make(chan struct{})
		go func() { a.GRPC.GracefulStop(); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(3 * time.Second):
			a.GRPC.Stop()
		}
	})
	httpSrv := &http.Server{Handler: a.Handler}
	go func() { _ = httpSrv.Serve(httpLis) }()
	t.Cleanup(func() { _ = httpSrv.Close() })
	managerAddr := "localhost:" + portOf(grpcLis.Addr().String())

	// --- the instance admin, through the wizard, also owns org A ---------
	admin := newAPIClient(t, base)
	admin.do(http.MethodPost, "/api/v1/setup/complete", map[string]any{
		"admin_email": "provider@e2e.test", "admin_password": "correct horse battery",
		"admin_name": "Provider Admin", "org_slug": "org-a", "org_name": "Org A",
		"setup_token": setup.Token(masterKey),
	}, nil)
	var adminEnrol struct {
		Secret string `json:"secret"`
	}
	admin.do(http.MethodPost, "/api/v1/auth/totp/enrol", struct{}{}, &adminEnrol)
	adminCode, err := totp.GenerateCode(adminEnrol.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	admin.do(http.MethodPost, "/api/v1/auth/totp/confirm", map[string]string{"code": adminCode}, nil)

	// --- org B: a SECOND organization handed to a SECOND, independent user
	// by real invite — not another organization the admin keeps for itself
	// (see the file header for why that distinction is the point) --------
	admin.do(http.MethodPost, "/api/v1/orgs", map[string]any{"slug": "org-b", "name": "Org B"}, nil)
	var invite struct {
		AcceptURL string `json:"accept_url"`
	}
	admin.do(http.MethodPost, "/api/v1/orgs/org-b/invites", map[string]any{
		"email": "customer-b@e2e.test", "role": "admin",
	}, &invite)
	if invite.AcceptURL == "" {
		t.Fatal("invite carries no accept_url — SMTP is not configured in this harness, so it must be handed over directly")
	}
	token := strings.TrimPrefix(invite.AcceptURL, base+"/invite/")
	if token == invite.AcceptURL || token == "" {
		t.Fatalf("could not extract an invite token from accept_url %q", invite.AcceptURL)
	}
	ownerB := newAPIClient(t, base)
	var accept struct {
		SignedIn bool `json:"signed_in"`
	}
	ownerB.do(http.MethodPost, "/api/v1/invites/accept", map[string]any{
		"token": token, "display_name": "Customer B", "password": "correct horse battery",
	}, &accept)
	if !accept.SignedIn {
		t.Fatal("accepting a fresh invite did not open a session")
	}
	var ownerBEnrol struct {
		Secret string `json:"secret"`
	}
	ownerB.do(http.MethodPost, "/api/v1/auth/totp/enrol", struct{}{}, &ownerBEnrol)
	ownerBCode, err := totp.GenerateCode(ownerBEnrol.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ownerB.do(http.MethodPost, "/api/v1/auth/totp/confirm", map[string]string{"code": ownerBCode}, nil)

	// --- each organization's own, real infrastructure ---------------------
	runnerBin, ansibleBin := resolveRunner(t)
	tenantA := setupSharedTenant(t, ctx, cancel, admin, "org-a", masterKey, managerAddr, runnerBin, ansibleBin)
	tenantB := setupSharedTenant(t, ctx, cancel, ownerB, "org-b", masterKey, managerAddr, runnerBin, ansibleBin)

	// --- one git server for the one shared url, authorizing BOTH
	// organizations' own, different deploy keys ---------------------------
	gitSrv := startGitServerAuthorizing(t, tenantA.deployPub, tenantB.deployPub)

	// --- the instance admin defines the shared repository (spec decision 1,
	// acceptance line 1) ---------------------------------------------------
	defsBase := "/api/v1/admin/repos/definitions"
	var def repoDefOut
	admin.do(http.MethodPost, defsBase, map[string]any{
		"name": "provider-infra", "url": gitSrv.url, "branch": "main",
	}, &def)
	defID := def.Definition.ID
	if defID == "" {
		t.Fatal("shared repository definition carries no id")
	}

	// --- it is already projected into BOTH organizations, as a distinct
	// row each (decision 1 and 4) ------------------------------------------
	projA := tenantA.soleRepo()
	projB := tenantB.soleRepo()
	if projA.SharedRepoID != defID || projB.SharedRepoID != defID {
		t.Fatalf("projection markers = %q / %q, want both %s", projA.SharedRepoID, projB.SharedRepoID, defID)
	}
	if projA.ID == projB.ID {
		t.Fatal("org A and org B resolved to the SAME repository row — tenancy did not project one per organization")
	}
	if projA.URL != gitSrv.url || projB.URL != gitSrv.url {
		t.Fatalf("projected urls = %q / %q, want both %s", projA.URL, projB.URL, gitSrv.url)
	}

	// --- each organization supplies its OWN credential and clones for real
	// (acceptance line 2, spec decision 3) ----------------------------------
	tenantA.bindCredentialAndSync(projA)
	tenantB.bindCredentialAndSync(projB)

	// --- neither organization can edit the shared url (acceptance line 3,
	// the task brief's own "neither can edit the url") ---------------------
	if code := admin.status(http.MethodPut, tenantA.prefix()+"/repos/"+projA.ID, map[string]any{
		"name": projA.Name, "url": "https://attacker.example.com/evil.git",
		"branch": projA.Branch, "subpath": projA.Subpath, "credential_id": tenantA.deployCred,
	}); code != http.StatusBadRequest {
		t.Fatalf("org A editing the shared url: %d, want 400", code)
	}
	if code := ownerB.status(http.MethodPut, tenantB.prefix()+"/repos/"+projB.ID, map[string]any{
		"name": projB.Name, "url": "https://attacker.example.com/evil.git",
		"branch": projB.Branch, "subpath": projB.Subpath, "credential_id": tenantB.deployCred,
	}); code != http.StatusBadRequest {
		t.Fatalf("org B editing the shared url: %d, want 400", code)
	}

	// --- both organizations run a REAL playbook, cloned through the shared
	// repository, each with its own greeting proving its own run actually
	// executed (acceptance line 2's "runs a playbook from it") -------------
	runA := tenantA.registerAndRun(projA.ID, "hello.yml", "hello from org A")
	runB := tenantB.registerAndRun(projB.ID, "hello.yml", "hello from org B")
	if runA.Job.ID == runB.Job.ID {
		t.Fatal("org A and org B's runs resolved to the SAME job — tenancy did not separate them")
	}

	// --- THE security claim (acceptance line 3, the task brief's own
	// closing sentence): org B's credential never appears anywhere
	// reachable from org A, and symmetrically — proven by searching every
	// body org A's own session can reach for org B's raw credential ids
	// (and the reverse), not by reasoning about the schema from a distance.
	// The ids are the right needle: they are the one thing an API response
	// could leak verbatim, the same technique
	// TestOrgScopedRepoRoutesNeverLeakAnotherOrgsIdentity (internal/api)
	// already uses for organization identity, applied here to the
	// credential boundary decision 3 exists for. ---------------------------
	bodiesA := map[string]string{
		"org A repo list":   rawGet(t, admin, base+tenantA.prefix()+"/repos"),
		"org A repo detail": rawGet(t, admin, base+tenantA.prefix()+"/repos/"+projA.ID),
		"org A credentials": rawGet(t, admin, base+tenantA.prefix()+"/credentials"),
		"org A run detail":  rawGet(t, admin, base+tenantA.prefix()+"/jobs/"+runA.Job.ID),
		"org A run events":  rawGet(t, admin, base+tenantA.prefix()+"/jobs/"+runA.Job.ID+"/events?after=0"),
	}
	forbiddenInA := []string{tenantB.deployCred, tenantB.hostLoginCred, tenantB.hostID}
	for label, body := range bodiesA {
		for _, needle := range forbiddenInA {
			if needle == "" {
				t.Fatalf("%s: a needle to search for is empty — the assertion would be vacuous", label)
			}
			if strings.Contains(body, needle) {
				t.Fatalf("%s unexpectedly contains org B's identifier %q:\n%s", label, needle, body)
			}
		}
	}

	bodiesB := map[string]string{
		"org B repo list":   rawGet(t, ownerB, base+tenantB.prefix()+"/repos"),
		"org B repo detail": rawGet(t, ownerB, base+tenantB.prefix()+"/repos/"+projB.ID),
		"org B credentials": rawGet(t, ownerB, base+tenantB.prefix()+"/credentials"),
		"org B run detail":  rawGet(t, ownerB, base+tenantB.prefix()+"/jobs/"+runB.Job.ID),
		"org B run events":  rawGet(t, ownerB, base+tenantB.prefix()+"/jobs/"+runB.Job.ID+"/events?after=0"),
	}
	forbiddenInB := []string{tenantA.deployCred, tenantA.hostLoginCred, tenantA.hostID}
	for label, body := range bodiesB {
		for _, needle := range forbiddenInB {
			if needle == "" {
				t.Fatalf("%s: a needle to search for is empty — the assertion would be vacuous", label)
			}
			if strings.Contains(body, needle) {
				t.Fatalf("%s unexpectedly contains org A's identifier %q:\n%s", label, needle, body)
			}
		}
	}
	// And org B must not even be ABLE to reach org A's own repository row
	// (or the reverse) through its own org-scoped path — tenancy, not just
	// the response body, must refuse it.
	if code := ownerB.status(http.MethodGet, tenantA.prefix()+"/repos/"+projA.ID, nil); code != http.StatusNotFound && code != http.StatusForbidden {
		t.Fatalf("org B reading org A's repository by id: %d, want 404 or 403", code)
	}

	// --- editing the DEFINITION's branch changes it in every organization;
	// the credentials stay untouched (spec acceptance list, the design
	// doc's own wording) -----------------------------------------------
	admin.do(http.MethodPut, defsBase+"/"+defID, map[string]any{
		"name": "provider-infra", "url": gitSrv.url, "branch": "release",
	}, nil)
	afterEditA := tenantA.soleRepo()
	afterEditB := tenantB.soleRepo()
	if afterEditA.Branch != "release" || afterEditB.Branch != "release" {
		t.Fatalf("branch after definition edit = %q / %q, want release in both", afterEditA.Branch, afterEditB.Branch)
	}
	if afterEditA.CredentialID != tenantA.deployCred || afterEditB.CredentialID != tenantB.deployCred {
		t.Fatalf("credentials after definition edit = %q / %q, want untouched (%s / %s)",
			afterEditA.CredentialID, afterEditB.CredentialID, tenantA.deployCred, tenantB.deployCred)
	}
}

// rawGet performs an authenticated GET and returns the raw response body —
// the leak assertions above need the literal bytes to search, not a
// narrowly typed decode that could hide a field nobody thought to name.
func rawGet(t *testing.T, api *apiClient, url string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cookie, ok := api.sessionCookie(); ok {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := api.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("GET %s: status %d, body %s", url, resp.StatusCode, raw)
	}
	return string(raw)
}
