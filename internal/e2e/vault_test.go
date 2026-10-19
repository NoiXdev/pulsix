// The v0.2e milestone gate (backlog 2.89): a repository can hold more than
// one labelled vault password, a job template may add more on top, and a
// run receives the union with the template winning a label collision
// (internal/repos.ResolveVaultIdentities). This scenario drives that over
// the real HTTP API, the real claim path and the real worker loop: a
// repository with two labelled passwords, a playbook whose two vars_files
// are each encrypted under a different one, a run that succeeds — and a
// second template that adds a third identity, proving its sibling never
// receives it.
//
// Runner modes, same duality as ansible_test.go: the deterministic fake
// ansible-runner (harness_test.go) emits a fixed event shape no matter
// which real playbook is named, so it cannot itself prove the two files
// decrypted — only the real ansible-runner pass (PULSIX_E2E_REAL_ANSIBLE=1)
// does that, and this scenario's extra assertion for it only runs then.
// What the fake pass proves instead, over the real API and database, is
// the full resolution path this sub-project actually added: the
// repository's and each template's own labelled set, merged with the
// template winning a collision, reported on the run detail with where each
// identity came from.
package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestVaultIdentitiesEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("no docker for the testcontainer database: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := wireScenario(t, ctx, cancel, wiring{})
	s.driveVault()
}

// repoFullOut is a wider mirror of repoOut (steps_test.go), carrying the
// fields a repo update needs to echo back unchanged plus the vault
// identity set this scenario edits.
type repoFullOut struct {
	Repo struct {
		ID              string `json:"id"`
		Name            string `json:"name"`
		URL             string `json:"url"`
		Branch          string `json:"branch"`
		Subpath         string `json:"subpath"`
		CredentialID    string `json:"credential_id"`
		VaultIdentities []struct {
			CredentialID string `json:"credential_id"`
			Label        string `json:"label"`
		} `json:"vault_identities"`
	} `json:"repo"`
}

// driveVault is the scenario's vault half. It runs independently of
// drive() (it pins the host key itself, the same way automate() does),
// against the repository and host wireScenario already set up.
func (s *scenario) driveVault() {
	s.t.Helper()
	prefix := "/api/v1/" + s.apiPath()
	s.pinHostKey()

	// --- Three vault_password credentials, over the API --------------------
	prodCred := s.vaultCredential("vault-prod", "prod-vault-pass-e2e")
	stagingCred := s.vaultCredential("vault-staging", "staging-vault-pass-e2e")
	extraCred := s.vaultCredential("vault-extra", "extra-vault-pass-e2e")

	// --- The repository's own base set: prod + staging ----------------------
	var repo repoFullOut
	s.get(prefix+"/repos/"+s.repoID, &repo)
	s.put(prefix+"/repos/"+s.repoID, map[string]any{
		"name": repo.Repo.Name, "url": repo.Repo.URL, "branch": repo.Repo.Branch,
		"subpath": repo.Repo.Subpath, "credential_id": repo.Repo.CredentialID,
		"vault_identities": []map[string]string{
			{"credential_id": prodCred, "label": "prod"},
			{"credential_id": stagingCred, "label": "staging"},
		},
	}, &repo)
	if len(repo.Repo.VaultIdentities) != 2 {
		s.t.Fatalf("repo vault identities after update = %+v, want 2", repo.Repo.VaultIdentities)
	}

	// --- A repo-backed playbook: two vars_files, each needing a different
	// vault password to decrypt (fixtures/git/vault_prod.yml, encrypted
	// --vault-id prod, and vault_staging.yml, --vault-id staging) -----------
	var pb playbookOut
	s.post(prefix+"/playbooks", map[string]any{"repo_id": s.repoID, "path": "vault.yml"}, &pb)
	if pb.Playbook.Source != "repo" {
		s.t.Fatalf("vault playbook source = %q, want repo", pb.Playbook.Source)
	}

	// --- The base template: no vault identities of its own, so a run gets
	// exactly the repository's two -------------------------------------------
	baseTID := s.createTemplate("vault-base", pb.Playbook.ID, map[string]any{})
	baseRunID := s.runTemplate(baseTID)
	baseRun := s.awaitJob(baseRunID, "run", 90*time.Second)
	if baseRun.Job.Status != "succeeded" {
		s.t.Fatalf("vault base run = %q (error %q)%s", baseRun.Job.Status, baseRun.Job.Error, s.runnerOutput())
	}
	s.assertVaultIdentities(baseRun, map[string]bool{"prod": false, "staging": false})
	s.assertVaultDecryptedIfReal(baseRunID)

	// --- A second template adds a third, on top of the repository's base ---
	var extraTmpl templateOut
	s.post(prefix+"/job-templates", map[string]any{
		"name": "vault-extra", "playbook_id": pb.Playbook.ID,
		"scope":            map[string]any{"host_ids": []string{s.hostID}},
		"vault_identities": []map[string]string{{"credential_id": extraCred, "label": "extra"}},
	}, &extraTmpl)
	extraTID := extraTmpl.Template.ID

	extraRunID := s.runTemplate(extraTID)
	extraRun := s.awaitJob(extraRunID, "run", 90*time.Second)
	if extraRun.Job.Status != "succeeded" {
		s.t.Fatalf("vault extra run = %q (error %q)%s", extraRun.Job.Status, extraRun.Job.Error, s.runnerOutput())
	}
	s.assertVaultIdentities(extraRun, map[string]bool{"prod": false, "staging": false, "extra": true})

	// --- The sibling (base) template never receives the third ---------------
	siblingRunID := s.runTemplate(baseTID)
	siblingRun := s.awaitJob(siblingRunID, "run", 90*time.Second)
	if siblingRun.Job.Status != "succeeded" {
		s.t.Fatalf("vault sibling run = %q (error %q)%s", siblingRun.Job.Status, siblingRun.Job.Error, s.runnerOutput())
	}
	s.assertVaultIdentities(siblingRun, map[string]bool{"prod": false, "staging": false})
}

// vaultCredential creates a vault_password credential and returns its id.
// Unlike setupCredentials' cred() helper, this never asserts a public key:
// a password credential carries none.
func (s *scenario) vaultCredential(name, password string) string {
	s.t.Helper()
	var out struct {
		Credential struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"credential"`
	}
	s.post("/api/v1/"+s.apiPath()+"/credentials", map[string]any{
		"name": name, "kind": "vault_password", "password": password,
	}, &out)
	if out.Credential.Kind != "vault_password" || out.Credential.ID == "" {
		s.t.Fatalf("vault credential %s = %+v, want kind vault_password with an id", name, out.Credential)
	}
	return out.Credential.ID
}

// assertVaultIdentities checks a run's resolved vault identity set (spec
// decision 3) against exactly the labels in want, each mapped to whether
// it should read from_template true.
func (s *scenario) assertVaultIdentities(run jobDetailOut, want map[string]bool) {
	s.t.Helper()
	got := run.Job.VaultIdentities
	if len(got) != len(want) {
		s.t.Fatalf("run vault identities = %+v, want %d entries matching %+v", got, len(want), want)
	}
	seen := make(map[string]bool, len(got))
	for _, id := range got {
		fromTemplate, ok := want[id.Label]
		if !ok {
			s.t.Fatalf("run vault identities carry unexpected label %q: %+v", id.Label, got)
		}
		if id.FromTemplate != fromTemplate {
			s.t.Fatalf("vault identity %q from_template = %v, want %v", id.Label, id.FromTemplate, fromTemplate)
		}
		if id.CredentialID == "" {
			s.t.Fatalf("vault identity %q carries no credential id", id.Label)
		}
		seen[id.Label] = true
	}
	for label := range want {
		if !seen[label] {
			s.t.Fatalf("run vault identities = %+v, missing label %q", got, label)
		}
	}
}

// assertVaultDecryptedIfReal checks the real ansible-runner's actual
// decryption of both vars_files — the one thing the fake runner cannot
// prove (see this file's header comment). It is a no-op under the default
// fake-runner pass.
func (s *scenario) assertVaultDecryptedIfReal(runID string) {
	s.t.Helper()
	if os.Getenv("PULSIX_E2E_REAL_ANSIBLE") != "1" {
		return
	}
	events := s.awaitEvents(runID, 30*time.Second, func(e jobEventsOut) bool {
		return hasEvent(e, "ok", taskGreeting)
	})
	for _, ev := range events.Events {
		if ev.State != "ok" || ev.Task != taskGreeting {
			continue
		}
		raw, err := json.Marshal(ev.Payload)
		if err != nil {
			s.t.Fatalf("marshal vault greeting payload: %v", err)
		}
		msg := string(raw)
		if !strings.Contains(msg, "prod-decrypted-ok") || !strings.Contains(msg, "staging-decrypted-ok") {
			s.t.Fatalf("the real ansible run's greeting event does not carry both decrypted vault secrets: %s", msg)
		}
		return
	}
	s.t.Fatalf("no ok event for %q to check the decrypted vault content", taskGreeting)
}
