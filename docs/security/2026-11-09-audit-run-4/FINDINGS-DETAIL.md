# Findings detail — confirmed medium, high and critical

## Existing worker clones ignore a changed repository URL and keep fetching (with the current credential) from the old origin

Fingerprint `internal/worker/runner/gitrepo.go:fetchGit:update-fetches-stale-origin` — severity **medium**

Once a worker holds a clone of a repository (<RepoCacheDir>/<org>/<repo-id>/.git), every later sync and repository-playbook run updates it with 'git fetch --quiet -- origin <branch>' and 'git reset --hard FETCH_HEAD'. The URL from the claim is used on that path only as the redaction string, and nothing on the worker reads, compares or rewrites remote.origin.url. When an org admin edits the repository URL (repos.Service.Update), or an instance admin edits a shared repository definition that propagates the URL to every projection, the manager clears the host-key pin and the next claim carries the new URL, but workers that already cloned it keep pulling from the old source. Runs execute the old source's playbooks against the organization's hosts, syncs install its requirements and report its variable sources, and the repository's current git credential is in the fetch's environment (token via GIT_ASKPASS, or ssh key via GIT_SSH_COMMAND), so git offers it to the old host if that host asks for authentication. For ssh remotes this holds while the repository is unpinned or the old and new URLs name the same ssh host (for example a repository moved to another path on the same provider): once a pin for a different new host exists (the URL change clears the pin, and only a worker that freshly clones the new URL can capture a new one), the stale worker's strict host-key check against the seeded new-host entry fails before ssh authenticates, so that worker's syncs and runs error out instead. https remotes have no pin and are not limited this way. Workers without a clone clone the new URL, so workers in the same zone can run different code for the same repository. The repo cache sweep removes clones only for repositories that no longer exist, so the stale clone stays for the life of the worker's data dir.

### Trace

1. **entrypoint** `internal/repos/service.go:357` (repos.Service.Update): An org admin's repository edit sets next.URL to the new URL under the same repository id. The only side effect of a URL change is clearing the host-key pin (lines 367-369). Shared-definition edits propagate the URL to projections through UpdateAndPropagate in the same way (internal/db/queries/shared.sql:196-200).
2. **propagation** `internal/jobs/queue_sql.go:1183` (SQLQueue.repoKindPayload): The next run or sync claim carries the new url in the repo payload, and the repository's current credential is placed under git:<repo-id> (line 1176).
3. **propagation** `internal/worker/sync.go:147` (SyncExecutor.Run): The sync passes the spec (new URL) and the git:<repo-id> credential to FetchRepoDir. The run path does the same through Runner.fetchRepo (internal/worker/runner/playbook.go:96 -> gitrepo.go:81).
4. **propagation** `internal/worker/runner/gitrepo.go:185` (Runner.FetchRepoDir): A git_token credential is exposed through GIT_ASKPASS (a git_key through GIT_SSH_COMMAND at line 166) for whatever host git contacts, then fetchGit is called (line 193).
5. **propagation** `internal/worker/runner/gitrepo.go:228` (Runner.fetchGit): An existing <cacheRoot>/<org>/<repo-id>/.git selects the update path.
6. **sink** `internal/worker/runner/gitrepo.go:231` (Runner.fetchGit): 'git fetch --quiet -- origin <branch>' contacts the clone's stored origin URL, not spec.URL. 'reset --hard FETCH_HEAD' (line 234) then checks out that remote's content for the run or sync.

### Evidence

- `internal/worker/runner/gitrepo.go:231`: The update path fetches the remote name 'origin'. spec.URL is passed to runGit only as redactURL.
- `internal/worker/runner/gitrepo.go:250`: Only the fresh-clone branch passes spec.URL to git, and git records it as remote.origin.url.
- `internal/repos/service.go:367`: URL changes are allowed and treated as a server change (the pin is cleared), but nothing invalidates or re-points worker clones.
- `internal/worker/cachesweep.go:186`: The sweep removes an org's clone only when the repository id is no longer listed, so a URL change never removes the old clone.
- `internal/db/queries/shared.sql:196`: A shared-definition URL edit rewrites every projection's url under the same repository id (clearing pins), so the same stale clone applies to every organization's workers.
- `internal/worker/runner/gitrepo.go:149`: A pinned claim seeds the run's known_hosts for the claim URL's host and fetches with StrictHostKeyChecking=yes (internal/worker/gitssh.go:77-78), so a stale ssh fetch to a different old host fails before authentication once the new URL is pinned; https fetches and same-host ssh moves are unaffected.

### Principal, resource and conditions

- Attacker: Controller of a repository source that an organization used to configure and has since replaced. The effect was observed through the worker's own git invocations in sandboxed harnesses.
- data_state: The worker already holds a clone of the repository from before the URL change (any earlier sync or run of that repository on that worker's data dir).
- authorization_role: The repository URL has to be changed by an org admin (or by an instance admin for a shared definition). The party who benefits needs no Pulsix role: it is whoever controls, or later gains control of, the content or host at the old URL, for example an abandoned, compromised, mistyped or squatted source that the admin replaced.
- third_party_dependency: Standard git semantics: 'git fetch origin' resolves the remote URL stored by 'git clone'. The verifier observed this with the git 2.47.3 shipped in noixdev/pulsix-worker:dev.
- system_configuration: The credential-disclosure half applies only when the repository has a git_token and the old https host requests HTTP authentication, or when a git_key is used against an old ssh host while no pin for a different new ssh host is set (a pin seeds known_hosts for the new URL's host and StrictHostKeyChecking=yes then refuses the old host before authentication; a stale-clone worker never captures a new pin itself because ObservedGitHostKey looks up the new URL's host name). That half follows from the environment construction in source and was not observed against a live authenticating server.

### Bounded local reproduction

Inputs:
- `Hunter harnesses (golang sandbox, fake git): FetchRepoDir(spec{URL:"https://old-host.example.invalid/team/playbooks.git",Branch:"main"}, "") followed by FetchRepoDir(spec{same org and repo id, URL:"https://new-host.example.invalid/team/playbooks.git"}, `{"token":"TOKEN-FOR-NEW-HOST"}`)`
- `Verifier check (noixdev/pulsix-worker:dev, real git 2.47.3, --network none, read-only, local file:// bare repos A and B in tmpfs): 'git clone --quiet --branch=main --single-branch -- file:///tmp/bareA.git repo', push a new commit to A, then the exact update argv 'git fetch --quiet -- origin main' and 'git reset --hard FETCH_HEAD' as issued for a claim URL of file:///tmp/bareB.git`

Steps:
1. Source: confirm in internal/worker/runner/gitrepo.go:228-237 that the update path passes 'origin' rather than spec.URL to git, and that spec.URL reaches git only in the clone at line 250.
2. Hunter harness: in the approved golang:1.27.1-alpine sandbox, run the runner-package tests TestAuditChangedRepoURLKeepsOldOrigin and TestH10FetchExistingCloneIgnoresClaimURL (fake git that logs argv and the GIT_ASKPASS answer). Check that the second fetch issues 'fetch --quiet -- origin main' with the new token answered by askpass, and that the new URL never reaches git.
3. Real-git check: in the worker image, create two local bare repositories with different site.yml files. Clone A with fetchGit's clone argv, commit new content to A, then run fetchGit's update argv in the clone. Compare site.yml, remote.origin.url and HEAD with the tips of A and B.

Observed: Fake-git harnesses: the second FetchRepoDir ran 'fetch --quiet -- origin main' and 'reset --hard FETCH_HEAD' with GIT_ASKPASS answering the new token, and the claim URL never reached git (PASS; agents/h04-req-cache/artifacts/evidence.txt, agents/h10-runner/artifacts/evidence.txt). Real git 2.47.3 in the worker image: after the update with claim URL file:///tmp/bareB.git, site.yml contained 'NEW content pushed to OLD source A', remote.origin.url was still file:///tmp/bareA.git, and clone HEAD 54641d94 equalled A's tip, not B's 388ca8ad (agents/v07-req-cache/artifacts/evidence.txt).

Invariant proven: Every fetch should come from the URL currently configured on the repository (the claim payload's url), and the repository's credential should only be offered to that source. A URL change, which the manager already treats as a move to a different server by clearing the host-key pin (internal/repos/service.go:367-369, shared.sql UpdateProjectedRepo), should take effect on every worker, including workers that already hold a clone.

### Remediation

On the update path, fetch from the claim's URL instead of the stored remote (or compare remote.origin.url with spec.URL and re-clone on a mismatch), so the credential, the pin check and the fetched content all go with the URL the manager currently has configured. Add a regression test where a second FetchRepoDir with a different URL hands that URL to git and never contacts the old one.

`internal/worker/runner/gitrepo.go`:

```go
if _, statErr := os.Stat(filepath.Join(target, ".git")); statErr == nil {
		// Fetch from the URL the manager sent, never the clone's stored
		// remote: a changed repository URL must take effect on every worker
		// that already holds a clone, and the credential must only go there.
		if err := runGit(ctx, gitBin, target, env, spec.URL, "fetch", "--quiet", "--", spec.URL, spec.Branch); err != nil {
			return wrap(err)
		}
		if err := runGit(ctx, gitBin, target, env, spec.URL, "reset", "--hard", "FETCH_HEAD"); err != nil {
			return wrap(err)
		}
		return nil
	}
```

