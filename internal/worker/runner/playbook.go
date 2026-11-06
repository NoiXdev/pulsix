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
	"os"
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

// systemPlaybooks maps the system kinds to their embedded playbooks
// (internal/worker/playbooks); their claims carry no playbook.
var systemPlaybooks = map[string]string{
	kindBootstrap: workerplaybooks.NameBootstrap,
	kindRotateKey: workerplaybooks.NameRotateKey,
	kindRevokeKey: workerplaybooks.NameRevokeKey,
}

// materializePlaybook resolves the run's playbook file and returns its
// absolute path. An adhoc kind builds its inline playbook; a bootstrap or
// rotate_key job materializes the worker's embedded system playbook; a run
// job with a repo reference fetches the repository (repo cache dir) and
// points at the file inside the clone; an inline playbook is written
// verbatim. rolesDir is the repo's galaxy directory — the roles the sync
// installed from requirements.yml — when a repo playbook has one, and ""
// for everything else; the run's ansible.cfg puts it on the roles path.
func (r *Runner) materializePlaybook(ctx context.Context, job *workerpb.Job, env worker.RunEnv) (playbook, rolesDir string, err error) {
	if job.GetKind() == kindAdhoc {
		text, err := adhocPlaybookText(job.GetKindPayload())
		if err != nil {
			return "", "", err
		}
		path := filepath.Join(env.WorkDir, projectDirName, playbookFileName)
		if err := writeTextFile(path, text, 0o600); err != nil {
			return "", "", err
		}
		return path, "", nil
	}

	// The two system playbooks ride inside the worker binary; their claims
	// carry no playbook field, so the kind decides the file.
	if name, ok := systemPlaybooks[job.GetKind()]; ok {
		text, err := workerplaybooks.Get(name)
		if err != nil {
			return "", "", fmt.Errorf("embedded playbook: %w", err)
		}
		path := filepath.Join(env.WorkDir, projectDirName, playbookFileName)
		if err := writeTextFile(path, string(text), 0o600); err != nil {
			return "", "", err
		}
		return path, "", nil
	}

	ref := job.GetPlaybook()
	repoID, playbookPath, isRepo, err := parsePlaybookRef(ref)
	if err != nil {
		return "", "", err
	}
	if !isRepo {
		if strings.TrimSpace(ref) == "" {
			return "", "", errors.New("the claim carries no playbook")
		}
		path := filepath.Join(env.WorkDir, projectDirName, playbookFileName)
		if err := writeTextFile(path, ref, 0o600); err != nil {
			return "", "", err
		}
		return path, "", nil
	}

	spec, secret, err := repoSpec(job, repoID, playbookPath)
	if err != nil {
		return "", "", err
	}
	path, err := r.fetchRepo(ctx, spec, secret, env.RepoCacheDir, env.WorkDir)
	if err != nil {
		return "", "", err
	}
	clone := r.repoDir(env.RepoCacheDir, worker.RepoSpec{OrgID: spec.OrgID, RepoID: spec.RepoID})
	rolesDir = galaxyRolesDir(clone)
	if env.Config.PlayUID != 0 {
		// A repo playbook is run where it was checked out, not copied into
		// the work dir, so sharing the work dir does nothing for it: the
		// repo cache is the worker's own 0700 tree and the play's uid
		// cannot so much as traverse it. Open the clone — and the path
		// down to it — for reading, never for writing.
		if err := ShareRepoRead(env.RepoCacheDir, clone); err != nil {
			return "", "", fmt.Errorf("share the repo checkout with the play uid: %w", err)
		}
		// The roles the sync installed from requirements.yml sit next to
		// the clone, in the same cache: the play reads them on the roles
		// path, and must not be able to rewrite them for the next job.
		if rolesDir != "" {
			if err := ShareRepoRead(env.RepoCacheDir, rolesDir); err != nil {
				return "", "", fmt.Errorf("share the installed roles with the play uid: %w", err)
			}
		}
	}
	return path, rolesDir, nil
}

// galaxyRolesDir returns the clone's galaxy directory (worker.GalaxyDir)
// when it is a real directory, "" otherwise. A symlink there — or anything
// else that is not a directory — is not a sync's install and is ignored
// rather than followed: the roles path must not lead out of the repo
// cache. A path that cannot be read is treated like a missing one; a role
// the playbook needs from it then fails the run with Ansible's own "role
// not found" error, which lists the searched paths.
func galaxyRolesDir(clone string) string {
	dir := worker.GalaxyDir(clone)
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return ""
	}
	return dir
}

// repoSpec reads the repository out of the claim: the kind payload carries
// {"repo": {"id","url","branch","subpath","repo_subpath"}} (the claim-time
// enrichment, plus the git server key pin, backlog 2.41), the repo's git
// credential rides in the credentials map as `git:<repo-id>` (empty when
// the repo fetches without one). playbookPath is the reference's own path;
// repoSubpath checks the claim's subpath against it.
func repoSpec(job *workerpb.Job, repoID uuid.UUID, playbookPath string) (gitRepoSpec, string, error) {
	orgID, err := uuid.Parse(job.GetOrgId())
	if err != nil {
		return gitRepoSpec{}, "", fmt.Errorf("claim org id: %w", err)
	}
	var payload struct {
		Repo *struct {
			ID            string  `json:"id"`
			URL           string  `json:"url"`
			Branch        string  `json:"branch"`
			Subpath       string  `json:"subpath"`
			RepoSubpath   *string `json:"repo_subpath"`
			HostKey       string  `json:"host_key"`
			HostPublicKey string  `json:"host_public_key"`
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
	repoSub, err := repoSubpath(payload.Repo.RepoSubpath, payload.Repo.Subpath, playbookPath)
	if err != nil {
		return gitRepoSpec{}, "", err
	}
	spec := gitRepoSpec{
		OrgID:         orgID,
		RepoID:        repoID,
		URL:           payload.Repo.URL,
		Branch:        payload.Repo.Branch,
		Subpath:       payload.Repo.Subpath,
		RepoSubpath:   repoSub,
		HostKey:       payload.Repo.HostKey,
		HostPublicKey: payload.Repo.HostPublicKey,
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
		"tasks": []map[string]any{{
			"name":                  "Ad-hoc command",
			"ansible.builtin.shell": argv,
			"args":                  map[string]any{"executable": "/bin/bash"},
			// Task level, exactly the plan's Task 15 shape — changed_when
			// is a task attribute, not a play attribute; at play level
			// ansible-playbook rejects the playbook outright.
			"changed_when": true,
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
