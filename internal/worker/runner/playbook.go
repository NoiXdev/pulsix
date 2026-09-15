// Playbook materialization: an editor playbook ships inline in the claim
// and is written verbatim; an ad-hoc job's one-task playbook is built from
// the kind payload (module shell, the argv, per the plan's Task 15); a repo
// playbook resolves `repo:<repo-id>:<subpath>` against the org-scoped repo
// cache, fetching it first.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
	workerplaybooks "github.com/noixdev/pulsix/internal/worker/playbooks"
	"gopkg.in/yaml.v3"
)

// maxAdhocArgv is the ad-hoc command's size bound; the API enforces it at
// enqueue time (Task 15), the runner defends the same bound.
const maxAdhocArgv = 8 << 10

// The embedded system playbooks' names (internal/worker/playbooks).
const (
	sysBootstrap = "pulsix.bootstrap.yml"
	sysRotateKey = "pulsix.rotate_key.yml"
)

// materializePlaybook resolves the run's playbook file and returns its
// absolute path. An adhoc kind builds its inline playbook; a bootstrap or
// rotate_key job materializes the worker's embedded system playbook; a run
// job with a repo reference fetches the repository (repo cache dir) and
// points at the file inside the clone; an inline playbook is written
// verbatim.
func (r *Runner) materializePlaybook(ctx context.Context, job *workerpb.Job, env worker.RunEnv) (string, error) {
	if job.GetKind() == kindAdhoc {
		text, err := adhocPlaybookText(job.GetKindPayload())
		if err != nil {
			return "", err
		}
		path := filepath.Join(env.WorkDir, projectDirName, playbookFileName)
		if err := writeTextFile(path, text, 0o600); err != nil {
			return "", err
		}
		return path, nil
	}

	// The two system playbooks ride inside the worker binary; their claims
	// carry no playbook field, so the kind decides the file.
	if job.GetKind() == kindBootstrap || job.GetKind() == kindRotateKey {
		name := sysBootstrap
		if job.GetKind() == kindRotateKey {
			name = sysRotateKey
		}
		text, err := workerplaybooks.Get(name)
		if err != nil {
			return "", fmt.Errorf("embedded playbook: %w", err)
		}
		path := filepath.Join(env.WorkDir, projectDirName, playbookFileName)
		if err := writeTextFile(path, string(text), 0o600); err != nil {
			return "", err
		}
		return path, nil
	}

	ref := job.GetPlaybook()
	repoID, _, isRepo, err := parsePlaybookRef(ref)
	if err != nil {
		return "", err
	}
	if !isRepo {
		if strings.TrimSpace(ref) == "" {
			return "", errors.New("the claim carries no playbook")
		}
		path := filepath.Join(env.WorkDir, projectDirName, playbookFileName)
		if err := writeTextFile(path, ref, 0o600); err != nil {
			return "", err
		}
		return path, nil
	}

	spec, secret, err := repoSpec(job, repoID)
	if err != nil {
		return "", err
	}
	return r.fetchRepo(ctx, spec, secret, env.RepoCacheDir, env.WorkDir)
}

// repoSpec reads the repository out of the claim: the kind payload carries
// {"repo": {"id","url","branch","subpath"}} (the claim-time enrichment), the
// repo's git credential rides in the credentials map as `git:<repo-id>`
// (empty when the repo fetches without one).
func repoSpec(job *workerpb.Job, repoID uuid.UUID) (gitRepoSpec, string, error) {
	orgID, err := uuid.Parse(job.GetOrgId())
	if err != nil {
		return gitRepoSpec{}, "", fmt.Errorf("claim org id: %w", err)
	}
	var payload struct {
		Repo *struct {
			ID      string `json:"id"`
			URL     string `json:"url"`
			Branch  string `json:"branch"`
			Subpath string `json:"subpath"`
		} `json:"repo"`
	}
	if err := json.Unmarshal([]byte(job.GetKindPayload()), &payload); err != nil {
		return gitRepoSpec{}, "", fmt.Errorf("kind payload: %w", err)
	}
	if payload.Repo == nil {
		return gitRepoSpec{}, "", fmt.Errorf("the claim carries no repo payload for repo %s", repoID)
	}
	if payload.Repo.ID != "" && payload.Repo.ID != repoID.String() {
		return gitRepoSpec{}, "", fmt.Errorf("kind payload names repo %s, the playbook references %s", payload.Repo.ID, repoID)
	}
	spec := gitRepoSpec{
		OrgID:   orgID,
		RepoID:  repoID,
		URL:     payload.Repo.URL,
		Branch:  payload.Repo.Branch,
		Subpath: payload.Repo.Subpath,
	}
	return spec, job.GetCredentials()["git:"+repoID.String()], nil
}

// parsePlaybookRef splits a claim's playbook reference. An inline playbook
// is everything that does not carry the repo: prefix (isRepo false, no
// error); a repo reference must parse as repo:<uuid>:<subpath> and fails
// the run otherwise — a malformed reference is never run as inline YAML.
func parsePlaybookRef(ref string) (uuid.UUID, string, bool, error) {
	rest, isRepo := strings.CutPrefix(ref, "repo:")
	if !isRepo {
		return uuid.Nil, "", false, nil
	}
	idStr, sub, ok := strings.Cut(rest, ":")
	if !ok {
		return uuid.Nil, "", true, fmt.Errorf("%q must be repo:<repo-id>:<subpath>", "repo:...")
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return uuid.Nil, "", true, fmt.Errorf("repo reference carries no repo id: %w", err)
	}
	if sub == "" {
		return uuid.Nil, "", true, fmt.Errorf("repo reference for %s carries no subpath", id)
	}
	return id, sub, true, nil
}

// adhocPlaybookText builds the one-task playbook from an ad-hoc job's kind
// payload: module shell (v0.2b's only module), the argv as the command with
// executable bash and changed_when true, exactly the plan's Task 15 shape.
// The argv is YAML-marshalled (not templated), so quoting is the YAML
// library's job and the command text can never inject structure.
func adhocPlaybookText(kindPayload string) (string, error) {
	module, argv, err := adhocPayload(kindPayload)
	if err != nil {
		return "", err
	}
	if module != "shell" {
		return "", fmt.Errorf("module %q is not supported (shell is v0.2b's only ad-hoc module)", module)
	}
	if strings.TrimSpace(argv) == "" {
		return "", errors.New("argv is empty")
	}
	if len(argv) > maxAdhocArgv {
		return "", fmt.Errorf("argv exceeds %d bytes", maxAdhocArgv)
	}
	playbook := []map[string]any{{
		"hosts":        "all",
		"gather_facts": false,
		"changed_when": true,
		"tasks": []map[string]any{{
			"name":                  "Ad-hoc command",
			"ansible.builtin.shell": argv,
			"args":                  map[string]any{"executable": "/bin/bash"},
		}},
	}}
	b, err := yaml.Marshal(playbook)
	if err != nil {
		return "", fmt.Errorf("marshal ad-hoc playbook: %w", err)
	}
	return string(b), nil
}

// adhocPayload reads the ad-hoc job's module and argv out of the kind
// payload.
func adhocPayload(kindPayload string) (module, argv string, err error) {
	if strings.TrimSpace(kindPayload) == "" {
		return "", "", errors.New("the claim carries no module and argv")
	}
	var p struct {
		Module string `json:"module"`
		Argv   string `json:"argv"`
	}
	if err := json.Unmarshal([]byte(kindPayload), &p); err != nil {
		return "", "", fmt.Errorf("kind payload: %w", err)
	}
	return p.Module, p.Argv, nil
}
