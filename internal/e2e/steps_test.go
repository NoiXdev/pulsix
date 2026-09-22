// The scenario's steps: everything after the wiring — credentials, host and
// bastion, the connection test with its pin, the repository sync and the
// inventory. Each step drives the real HTTP API and asserts on what the
// backend actually did.
package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// hostName is the managed host's inventory name (stable, because it is the
// key the golden file's hostvars map is keyed by).
const hostName = "e2e-host.fra"

// The DTOs the scenario reads — deliberately narrow mirrors of the API's
// own shapes, only the fields the assertions touch.
type (
	credentialOut struct {
		Credential struct {
			ID        string `json:"id"`
			Kind      string `json:"kind"`
			PublicKey string `json:"public_key"`
		} `json:"credential"`
	}
	bastionOut struct {
		Bastion struct {
			ID string `json:"id"`
		} `json:"bastion"`
	}
	hostOut struct {
		Host struct {
			ID string `json:"id"`
		} `json:"host"`
	}
	jobStartOut struct {
		Jobs []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"jobs"`
	}
	jobDetailOut struct {
		Job struct {
			ID         string         `json:"id"`
			Status     string         `json:"status"`
			Hosts      []string       `json:"hosts"`
			Stats      map[string]any `json:"stats"`
			Error      string         `json:"error,omitempty"`
			WorkerName string         `json:"worker_name,omitempty"`
			Template   *struct {
				Name string `json:"name"`
			} `json:"template,omitempty"`
			Group []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"group"`
		} `json:"job"`
	}
	jobEventsOut struct {
		Events []struct {
			Seq     uint64         `json:"seq"`
			Host    string         `json:"host"`
			Task    string         `json:"task"`
			State   string         `json:"state"`
			Payload map[string]any `json:"payload"`
		} `json:"events"`
	}
	hostTestOut struct {
		Test struct {
			OK           bool   `json:"ok"`
			HostKey      string `json:"host_key"`
			HostKeyState string `json:"host_key_state"`
			Checks       []struct {
				Name   string `json:"name"`
				Detail string `json:"detail"`
				OK     bool   `json:"ok"`
			} `json:"checks"`
			// HopKeys is what the chain's jump hosts presented (backlog 2.40).
			HopKeys []struct {
				BastionID   string `json:"bastion_id"`
				Fingerprint string `json:"fingerprint"`
				State       string `json:"state"`
			} `json:"hop_keys"`
		} `json:"test"`
	}
	repoOut struct {
		Repo struct {
			ID            string     `json:"id"`
			LastSync      *time.Time `json:"last_sync,omitempty"`
			LastSyncError string     `json:"last_sync_error,omitempty"`
		} `json:"repo"`
	}
	repoSyncOut struct {
		Job struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"job"`
	}
	previewOut struct {
		Inventory   map[string]any            `json:"inventory"`
		Connections map[string]map[string]any `json:"connections"`
		Warnings    []map[string]any          `json:"warnings"`
	}
	templateOut struct {
		Template struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"template"`
	}
	playbookOut struct {
		Playbook struct {
			ID     string `json:"id"`
			Source string `json:"source"`
		} `json:"playbook"`
	}
)

// apiPath returns the org-scoped path prefix.
func (s *scenario) apiPath() string { return "orgs/" + s.org }

// get/put/post are thin wrappers keeping the call sites readable.
func (s *scenario) get(path string, out any) { s.api.do(http.MethodGet, path, nil, out) }

func (s *scenario) post(path string, body, out any) {
	s.api.do(http.MethodPost, path, body, out)
}

func (s *scenario) put(path string, body, out any) { s.api.do(http.MethodPut, path, body, out) }

// setupCredentials creates the three generated credentials: the host's
// login key, the bastion's login key and the git deploy key. The fixtures
// authorize exactly the public keys these carry.
func (s *scenario) setupCredentials() {
	s.t.Helper()
	prefix := "/api/v1/" + s.apiPath()
	cred := func(name, kind string) string {
		s.t.Helper()
		var out credentialOut
		s.post(prefix+"/credentials", map[string]any{
			"name": name, "kind": kind, "generate": true,
		}, &out)
		if out.Credential.Kind != kind {
			s.t.Fatalf("credential %s: kind %q, want %q", name, out.Credential.Kind, kind)
		}
		if out.Credential.PublicKey == "" {
			s.t.Fatalf("credential %s carries no public key", name)
		}
		return out.Credential.ID
	}
	s.hostLoginCred = cred("host-login", "ssh_key")
	s.bastionCred = cred("bastion-login", "ssh_key")
	s.deployCred = cred("deploy-key", "git_key")

	// The public keys the fixtures authorize: exactly what the API reports
	// for the generated credentials (the operator path installs these).
	s.hostLoginPub = s.credentialKey(s.hostLoginCred)
	s.bastionPub = s.credentialKey(s.bastionCred)
	s.deployPub = s.credentialKey(s.deployCred)
}

// credentialKey fetches one credential's public key and parses it.
func (s *scenario) credentialKey(id string) ssh.PublicKey {
	s.t.Helper()
	var out credentialOut
	s.get("/api/v1/"+s.apiPath()+"/credentials/"+id, &out)
	return parseAuthorizedKey(s.t, out.Credential.PublicKey)
}

// parseAuthorizedKey reads the API's public-key line (OpenSSH authorized
// keys format) into a key.
func parseAuthorizedKey(t *testing.T, line string) ssh.PublicKey {
	t.Helper()
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(line)))
	if err != nil {
		t.Fatalf("parse public key %q: %v", line, err)
	}
	return pub
}

// setupHostAndBastion adds the bastion object and the managed host, wires
// the host's SSH settings to its credential, and makes the bastion the
// zone default — the rendered route the run and the probe both take.
func (s *scenario) setupHostAndBastion() {
	s.t.Helper()
	prefix := "/api/v1/" + s.apiPath()

	var out bastionOut
	s.post(prefix+"/bastions", map[string]any{
		"name": "edge", "address": "127.0.0.1", "port": s.bastionS.port,
		"username": "ops", "credential_id": s.bastionCred,
	}, &out)
	s.bastionID = out.Bastion.ID

	var hostOut hostOut
	s.post(prefix+"/hosts", map[string]any{
		"name": hostName, "zone_id": s.zoneID, "address": s.host.host, "managed": true,
	}, &hostOut)
	s.hostID = hostOut.Host.ID

	// Host SSH settings: the login user and credential, and the port the
	// fixture listens on (bastion mode stays inherit — the zone default
	// carries the hop).
	var sshOut struct {
		SSH struct {
			CredentialID string `json:"credential_id"`
			BastionMode  string `json:"bastion_mode"`
		} `json:"ssh"`
	}
	s.put(prefix+"/hosts/"+s.hostID+"/ssh", map[string]any{
		"username": "ansible", "port": s.host.port, "credential_id": s.hostLoginCred,
	}, &sshOut)
	if got := sshOut.SSH.CredentialID; got != s.hostLoginCred {
		s.t.Fatalf("host ssh credential = %q, want %q", got, s.hostLoginCred)
	}

	// The zone default bastion: the probe and the run both route through
	// it, and the render marks the connection's source "zone".
	var zoneOut map[string]any
	s.put(prefix+"/zones/"+s.zoneID+"/bastion", map[string]any{"bastion_id": s.bastionID}, &zoneOut)
}

// setupRepo registers the fixture git repository with the deploy key and
// syncs it, asserting last_sync lands and the sync's stats report that no
// requirements install was needed.
func (s *scenario) setupRepo(gitSrv *gitServer) {
	s.t.Helper()
	prefix := "/api/v1/" + s.apiPath()

	var created repoOut
	s.post(prefix+"/repos", map[string]any{
		"name": "fixture", "url": gitSrv.url, "branch": "main", "credential_id": s.deployCred,
	}, &created)
	s.repoID = created.Repo.ID

	var syncOut repoSyncOut
	s.post(prefix+"/repos/"+s.repoID+"/sync", struct{}{}, &syncOut)
	job := s.awaitJob(syncOut.Job.ID, "sync_repo", 60*time.Second)
	if job.Job.Status != "succeeded" {
		s.t.Fatalf("repo sync job %s = %q (error %q)", syncOut.Job.ID, job.Job.Status, job.Job.Error)
	}
	// The fixture repository carries no requirements.yml: the sync
	// materializes the clone and installs nothing (galaxy needs network;
	// Task 11's sync tests cover the install path at the unit level).
	if got, _ := job.Job.Stats["requirements_installed"]; got != false {
		s.t.Fatalf("sync stats requirements_installed = %v, want false", got)
	}

	var repo repoOut
	s.get(prefix+"/repos/"+s.repoID, &repo)
	if repo.Repo.LastSync == nil {
		s.t.Fatal("repo last_sync is unset after a successful sync")
	}
	if repo.Repo.LastSyncError != "" {
		s.t.Fatalf("repo last_sync_error = %q, want empty", repo.Repo.LastSyncError)
	}
}

// setupInventory creates the group tree (one group) and assigns the host
// to it — the fixture set the golden file renders.
func (s *scenario) setupInventory() {
	s.t.Helper()
	prefix := "/api/v1/" + s.apiPath()

	var group struct {
		Group struct {
			ID string `json:"id"`
		} `json:"group"`
	}
	s.post(prefix+"/inventory/groups", map[string]any{"name": "web"}, &group)
	s.groupID = group.Group.ID

	var member struct {
		Member map[string]any `json:"member"`
	}
	s.put(prefix+"/inventory/groups/"+s.groupID+"/members/"+s.hostID, map[string]any{"vars": map[string]any{}}, &member)
	if got := member.Member["host_id"]; got != s.hostID {
		s.t.Fatalf("membership host_id = %v, want %s", got, s.hostID)
	}
}

// awaitJob polls the run view until the job reaches a terminal status and
// returns its detail.
func (s *scenario) awaitJob(id, kind string, timeout time.Duration) jobDetailOut {
	s.t.Helper()
	path := "/api/v1/" + s.apiPath() + "/jobs/" + id
	deadline := time.Now().Add(timeout)
	for {
		var out jobDetailOut
		s.get(path, &out)
		if isTerminal(out.Job.Status) {
			return out
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("job %s (%s) did not finish in %s; last status %q", id, kind, timeout, out.Job.Status)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func isTerminal(status string) bool {
	switch status {
	case "succeeded", "failed", "cancelled", "error":
		return true
	}
	return false
}

// awaitEvents polls the run view's events endpoint until the predicate
// matches and returns every event.
func (s *scenario) awaitEvents(jobID string, timeout time.Duration, want func(e jobEventsOut) bool) jobEventsOut {
	s.t.Helper()
	path := fmt.Sprintf("/api/v1/%s/jobs/%s/events", s.apiPath(), jobID)
	deadline := time.Now().Add(timeout)
	for {
		var out jobEventsOut
		s.get(path+"?after=0", &out)
		if want(out) {
			return out
		}
		if time.Now().After(deadline) {
			raw, _ := json.Marshal(out)
			s.t.Fatalf("job %s events did not match within %s; got %s", jobID, timeout, raw)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// sseFrame is one frame of the run view's live stream.
type sseFrame struct {
	Event string
	Data  string
}

// sseResult is one collected stream: frames or the reason the collection
// failed. The collector goroutine drive() spawns hands the main goroutine
// this value, so every failure — including sseStream's timeout — surfaces
// on the goroutine that owns the test, never as a t.Fatal on the collector
// (which would terminate only that goroutine and hang the harness).
type sseResult struct {
	frames []sseFrame
	err    error
}

// sseStream subscribes to the run view's live stream and records frames
// until the server ends it (the terminal status frame) or the timeout.
//
// It returns an error instead of calling t.Fatal because drive() runs it
// on a spawned goroutine: t.Fatal from a non-test goroutine terminates
// only that goroutine, and the main goroutine would block on the result
// channel forever — the harness would hang instead of failing.
func (s *scenario) sseStream(jobID string, timeout time.Duration) ([]sseFrame, error) {
	req, err := http.NewRequest(http.MethodGet,
		s.api.base+"/api/v1/"+s.apiPath()+"/jobs/"+jobID+"/stream", nil)
	if err != nil {
		return nil, fmt.Errorf("build SSE stream request for job %s: %w", jobID, err)
	}
	if cookie, ok := s.api.sessionCookie(); ok {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := s.api.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("open SSE stream for job %s: %w", jobID, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var frames []sseFrame
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	deadline := time.After(timeout)
	var cur sseFrame
	for {
		read := make(chan bool, 1)
		go func() {
			more := scanner.Scan()
			read <- more
		}()
		select {
		case more := <-read:
			if !more {
				if strings.TrimSpace(cur.Data) != "" {
					frames = append(frames, cur)
				}
				return frames, nil
			}
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				cur = sseFrame{Event: strings.TrimPrefix(line, "event: ")}
			case strings.HasPrefix(line, "data: "):
				cur.Data = strings.TrimPrefix(line, "data: ")
			case line == "" && strings.TrimSpace(cur.Data) != "":
				frames = append(frames, cur)
				cur = sseFrame{}
			}
		case <-deadline:
			// The deadline is the caller's signal, not a test failure: the
			// error travels back through the result channel and the main
			// goroutine fails the test from there.
			return frames, fmt.Errorf("SSE stream for job %s did not end within %s; frames so far: %d", jobID, timeout, len(frames))
		}
	}
}

// TestSSEStreamTimeoutReturnsErrorNotFatalf pins the fix the review asked
// for: sseStream runs on drive()'s spawned collector goroutine, so it must
// RETURN its failure instead of calling t.Fatal (which only terminates the
// calling goroutine and would leave the main goroutine blocked on the
// result channel forever). A server that opens an SSE stream and never
// ends it forces the timeout; the shortened deadline keeps the test cheap.
func TestSSEStreamTimeoutReturnsErrorNotFatally(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Never write another frame: hold until the client goes away, so
		// the only way out for sseStream is its own deadline.
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	s := &scenario{t: t, api: newAPIClient(t, srv.URL)}
	frames, err := s.sseStream("job-1", 250*time.Millisecond)
	if err == nil {
		t.Fatal("sseStream returned no error on a stream that never ends")
	}
	if !strings.Contains(err.Error(), "did not end within") {
		t.Fatalf("timeout error = %v, want the did-not-end reason", err)
	}
	if len(frames) != 0 {
		t.Fatalf("frames = %v, want none", frames)
	}
}
