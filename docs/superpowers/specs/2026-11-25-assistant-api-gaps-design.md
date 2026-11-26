# Assistant API gaps from the a3b rollout — design

Date: 2026-11-25. Source: the owner's briefing "missing Pulsix API / MCP capabilities" (rolling out the a3b cloud with an assistant driving Pulsix through the MCP server). Research of the current code: see "Current state" per part (file references as of `776215e6`).

## Intent

An assistant driving Pulsix through the MCP server must be able to carry a real rollout end to end — enrol agents on existing hosts, set up the recurring safety-net run and the webhook trigger, run a dry run first, know exactly which commit will run, wait for jobs, read the result of one task, and set per-host values — **without** weakening the two principles the assistant surface was built on:

1. Anything that runs a playbook (now or later, once or recurring) needs a person's approval in the GUI.
2. Secrets are never handed to or set by the assistant: no secret appears in an MCP response, a job record, a log or an activity entry.

Success: the briefing's acceptance — "an assistant can request one run that installs and enrols the agent on N existing hosts; nobody copies a token; no token appears in a log, a job record or an MCP response" — plus each P1/P2 item usable through MCP tools.

Anti-goals: an assistant that can create anything that runs unattended without a person approving it; a token or trigger secret in an assistant transcript; a run whose code differs from what the approver saw.

## Owner decisions (2026-11-25)

- **D1 Schedules and triggers** created or changed by the assistant go through a **request and approval**, like `request_run`. A schedule or trigger becomes active only once a person approves it in the GUI. A trigger's secret is shown only to that person, at approval. Pausing/disabling and deleting are allowed directly (they only stop runs).
- **D2 Agent enrollment** happens **only inside an approved run**: a job-template option. No standalone enrollment tool.
- **D3 Commit pin**: the approval picture shows the commit of the repository's last successful sync, exactly that commit runs, and it is recorded on the job. Approval is refused while a sync of that repository is running. `sync_first` syncs and then requests the fresh head.
- **D4 Dry runs** (`check_mode`) need approval like any run; the picture says "dry run" prominently.
- **D6 Inventory values written by an assistant** (2026-11-26, owner decision "both"): values an assistant writes into the inventory (group variables, membership variables, schema defaults) are marked and passed to Ansible as plain text, never evaluated; ~~a person saving the value clears the mark.~~ **Controller's refinement (2026-11-26, Task 1 fix round 1):** a person's save clears the mark of a key only when the saved value differs from the stored one (they changed it); a value resent unchanged keeps its mark, whatever form or client sent it. To make an assistant's value live unchanged, a person accepts it explicitly (a REST route per object — group values, schema defaults, membership values — taking the keys, interactive session only, logged), shown as "Accept as live" on each marked value and on marked defaults in the fields editor. In addition, such a change pauses every schedule and disables every trigger whose run would receive the changed values, with a review request as in C2. Hosts joining or leaving a group stay excluded. Backlog 2.136.
- **D7 Membership changes by an assistant** (2026-11-26, owner decision): an assistant adding a host to a group, removing one, moving a group (parent change), or creating a host that falls into a template's scope also pauses the schedules and triggers whose runs would then reach other hosts, with a review request, as in D6. A person's inventory maintenance pauses nothing. This supersedes "hosts joining or leaving a group stay excluded" in D6 and C2 for changes made by an assistant. Controller's calls in the same round: a host address must be an IP address or a host name, and `ansible_host` is always rendered as plain text; reach is decided by the template's scope and zone, not by limits (a limited run still receives every scoped host's values through `hostvars`).
- **D5 Changes after approval**: when an assistant changes something run-relevant on a job template or repository that an active schedule or trigger uses, those schedules and triggers pause themselves and a review request is created; a person's approval resumes them. A change made by a person keeps them running as today.

## Parts

### A. Run requests carry overrides and a pinned commit (P2 items 1–4; foundation for B)

Current state: `request_run` takes `template_id`, `host_id?`, `reason` (`internal/mcp/tools_jobs.go`); the REST body only `host_id`, `reason`. The core already has `jobs.RunOverrides{Check, Diff, Tags, Playbook, limit, extra_vars}` (sealed, merged at claim) and `templates.ValidateOverrides`, used by schedules and webhooks but not by run requests. The picture (`runrequests/picture.go`) has repository and branch but no commit; the worker fetches the branch tip at run time (`worker/runner/gitrepo.go`). Request TTL is a constant hour.

Behaviour:
- `request_run` (and the REST route) accept: `check_mode` (bool) and `diff` (bool, default true with check_mode); `extra_vars` (object, non-secret only — validated with `ValidateOverrides` against the template's `extra_vars_schema`; a key whose schema type is `secret` is refused with a clear error); `host_ids` (list) or `limit` (string), each narrowing the template's own scope (never widening; a host outside it is refused at request time as today for `host_id`); `expires_in` (minutes).
- The overrides are stored on the request (sealed like other overrides), shown in the picture (extra vars by value, they are non-secret; hosts listed; "Dry run (check mode, diff)" as a banner), included in the fingerprint, and passed as `RunOverrides` when the approval enqueues the run.
- **Commit pin (D3):** while the request is pending, its picture shows the repository's last successful sync commit (`repo_var_sources.commit_sha`), live (a newer sync changes the picture, and the approver reviews again as for any picture change). If there is none (never synced, or the worker did not report it — `worker_reads=false`), or the repository's URL, branch or subpath changed after that sync, the picture says so and approval is refused until the repository is synced (or the request was made with `sync_first`). Approval is refused (409, clear message) while a `sync_repo` job of that repository is queued or running. **At approval** the commit (and B's host list) are stored on the request and the job; the claim guard compares the stored values instead of recomputing them, so a later sync does not fail an approved run. The worker fetches and checks out exactly that SHA (not the branch tip) and fails the job with a clear message if the SHA is no longer reachable. The job detail and `get_job` show the commit. Workers announce support with a claim flag (like `SupportsCheckMode`), and the claim query never hands a pinned job to a worker without it — an old worker would otherwise silently run the branch tip; such a job waits for an updated worker (upgrade note).
- **Which runs are pinned:** runs from approved requests and their retries (a retry runs its original job's commit; a retry of a job without a recorded commit runs the current last-sync commit and shows it). Manual runs by a person, schedule and trigger runs keep running the branch tip; a template whose playbook lives in the editor (no repository) has nothing to pin and needs no sync.
- **Hosts:** `host_ids` is stored as a list on the request; the job's hosts are its intersection with the template's scope under the template's limit (a host outside is refused at request time). `limit` is a run override as in `RunOverrides`.
- **States:** a request is `waiting_for_sync` (only with `sync_first`), `pending`, `approved`, `rejected`, `expired`, or `failed` (its sync failed). The expiry clock starts when it becomes pending; waiting requests count against the per-token pending cap.
- **`sync_first`:** the request starts a sync (deduplicated, see G) and stays in a "waiting for sync" state; when the sync succeeds its picture (with the new commit) is computed and it becomes pending; if the sync fails, the request fails with the sync's error. Approval is impossible before. *(Claude's call: the request is created at once so the assistant gets its link immediately.)*
- **Expiry:** `expires_in` between 5 minutes and 24 hours, default 60 minutes. *(Claude's call: a fixed maximum of 24 h, no new organization setting.)*
- Retries (`request_retry`) keep today's behaviour; a retry runs the commit its original job ran.

### B. Agent enrollment inside an approved run (P1)

Current state: enrollment tokens can only be minted with an interactive session (`api/agent_handlers.go`, backlog 7.49), host-bound single-use tokens are what the host's Agent card creates (`agents.Service.CreateToken`, `ClaimToken` binds the host). Secret template variables are sealed into the job payload at claim and redacted by the worker (`jobs/redact.go`, `worker/runner/redact.go`). There is no per-host secret variable at run start.

Behaviour:
- A job-template option **Enrol agents** (`agent_enrollment`, boolean, default off), editable in the GUI and the template API (and therefore `update_job_template`; changing it changes the template, which existing approvals of that template's requests already fingerprint).
- The picture of a run request for such a template lists "Will enrol agents on: <hosts>" — the target hosts that have no agent at request time — and includes that list in the fingerprint.
- When the job is claimed, the manager mints, for each host of the approved list that still has no agent, a host-bound single-use enrollment token in the job's zone with a fixed lifetime of 24 hours. The manager renders the inventory at claim: each token goes into its own host's hostvars as `pulsix_agent_enroll_token` and into the inventory's secrets, which already feed the job's redact list, so it never reaches a log, an event or the job record in clear; no worker or protocol change. Hosts that have an agent by then get none; a host not in the approved list gets none. The variable names Pulsix sets (`pulsix_agent_enroll_token` and the non-secret `pulsix_agent_manager_url` and, where the install needs it, `pulsix_agent_server_name`) are reserved: inventory variables of those names are refused, so nothing can shadow them. A docs page states this contract for roles that install the agent.
- When the job reaches any terminal state, every token it minted that is still unused is revoked. A re-claim (the reaper requeued the job) revokes the earlier tokens and mints fresh ones. A retry mints again for its hosts that still have no agent. A dry run mints nothing.
- **Who may:** minting today needs `agents.write` and a person (backlog 7.49). So the person who approves a request for such a template, starts such a run by hand, or approves a schedule of such a template also needs `agents.write`; otherwise the approval is refused with a clear message.
- Which runs mint: ~~runs started by a person (manual run), by an approved run request, and by schedules (which, by D1, a person approved). Runs started by a webhook trigger do **not** mint (the role then simply finds no token). *(Claude's call.)*~~ **Controller's revised call (2026-11-25, Task 1 fix round 1):** tokens are minted only for a job that carries an explicit list of hosts a person signed in (no API or assistant token) with `agents.write` confirmed: the approval of a run request (the picture's list, stored on the request and its jobs), or a manual run or retry of such a template whose start sends the `enroll_host_ids` its person confirmed. The start route answers 409 with the current list when the field is missing or differs from the targets without an agent then (order-free); a preview route lists them for the confirmation. Schedules, chained follow-ups, webhook runs and playbook-page dry runs carry no list and never mint, so they need no more than today (a schedule of such a template needs no `agents.write`); an assistant's dry-run request of such a template still needs an approver with `agents.write`, and its approval stores an empty list. Switching the flag, approving such a request, and starting or retrying such a run need a signed-in person with `agents.write`.
- Activity log: "enrollment tokens minted for <n> hosts by job <id>" and "revoked <n> unused" — names and counts only.

### C. Schedule and trigger requests (P1)

Current state: schedules and triggers have full REST CRUD (`api/schedule_handlers.go`, `api/trigger_handlers.go`) gated by `templates.write` / `repos.write`; neither has any approval; MCP has only schedule read tools; a trigger's secret is returned once in the create/rotate response.

Behaviour:
- The run-request concept is generalised to **requests** of three kinds: `run` (today), `schedule`, `trigger`. Same lifecycle (pending → approved / rejected / expired / withdrawn), same approvals page and badge, same expiry rules, same per-token pending cap, same fingerprint and "the picture changed, review again" behaviour. *(Claude's call: one table and one approvals page rather than three.)*
- MCP tools:
  - `create_schedule`, `update_schedule`, `resume_schedule` → a schedule request. Fields as the REST body today: template, name, mode cron/simple, cron or simple, time zone, guards (overlap skip/queue, catch-up, maintenance), limit, non-secret extra vars. The picture shows the schedule as it will be (and for an update, what changes). On approval the schedule is created/updated/resumed exactly as pictured.
  - `pause_schedule`, `delete_schedule` act directly.
  - `create_trigger`, `update_trigger`, `rotate_trigger_secret` → a trigger request. Fields as the REST body today: kind (template run / repository sync), target, preset, timestamp header, IP allowlist, on_active, mapping, rate limit, enabled. On approval the trigger is created/updated/rotated; **the secret is shown to the approving person once in the approval dialog** and never returned to the MCP caller, who gets the trigger URL and settings (`get_trigger`).
  - `list_triggers`, `get_trigger` read; `delete_trigger` and disabling (`update_trigger` with only `enabled: false`) act directly.
- A new token area **`automation.request`** grants these tools (reads need `templates.read` / `repos.read` as today). *(Claude's call.)*
- REST: the existing routes keep working for people and API tokens with the right permission (unchanged); the request kinds are reached through the run-request routes generalised to requests.
- **Approving** a schedule or trigger request needs an interactive session plus the permission the direct REST route needs (`templates.write`, or `repos.write` for a repository-sync trigger), `jobs.run` where a template will run, and `agents.write` when the template enrols agents (B). `automation.request` also grants the direct pause, delete and disable tools. Extra vars in schedule requests are non-secret only (a secret key keeps its stored value via `{"$secret":"keep"}` on update).
- **Request states** as in A (no `waiting_for_sync`).

### C2. Changes after approval (D5)

- A change made **through an assistant token** to a run-relevant field of a job template (playbook, inventory scope, limit, extra vars, credentials, `agent_enrollment`, any other field the run uses) or a repository (URL, branch, subpath, credentials) pauses every active schedule and every enabled trigger that runs that template or syncs/uses that repository, with the reason "changed by an assistant, needs review", and creates one review request per paused schedule/trigger whose picture shows the change. Approving it resumes the schedule/trigger; rejecting leaves it paused. A person's change does nothing of the kind.
- ~~Not covered (same as today, stated in the docs): inventory changes — hosts joining or leaving a group a template targets — change who a schedule runs on without a review. *(Claude's call: inventory churn is everyday work; pausing on it would make schedules useless.)*~~ **Superseded by D6 and D7 for changes made by an assistant:** an assistant's change of inventory values (group values, schema defaults and any schema edit that changes the values a run receives, membership values), a host it adds to or removes from a group, a group it moves or deletes with its members, and a managed host it creates (or makes managed) pause the schedules and triggers whose runs then receive other values or reach other hosts, with a review request as above. Reach is decided by the template's scope (and the follow-ups that start it), never narrowed by a limit: a limited run still renders every scoped host of its zones. A person's inventory maintenance still pauses nothing.

### D. Waiting instead of polling (P2)

- `get_job` and `get_run_request` (and the generalised request read) take `wait_seconds` (0–25). The call returns as soon as the job's status (or the request's state) differs from the `status`/`state` the caller passes as `unless`, or when the time is up. Implemented as a database poll (every 500 ms) inside the request, not the in-process broker, so it works with several managers. *(Claude's call: 25 s cap, below common proxy timeouts.)*

### E. Searching the job log (P2)

Current state: `get_job_log` fetches every event of the job and filters `host`/`failed_only` in Go; the REST route has only `after` and no limit (a memory risk on large logs). `job_events` stores `host`, `task`, `state` columns; the role is the "role : task" prefix of `task`.

Behaviour:
- REST `/jobs/{id}/events` gains `limit` (server-side, max 1000), `host`, `task` (substring, case-insensitive), `role` (matches the "<role> : " prefix), `state` (list of `ok`, `changed`, `skipped`, `failed`, `unreachable`), all applied in SQL within the job's rows (bounded by the job's creation time for chunk pruning).
- `get_job_log` passes them through and adds `changed_only` → a per-host summary of changed tasks (host → list of task names) instead of events.

### F. Host variables (P2)

Current state: per-host values already exist as **membership variables** of a group (fields with scope `host`, or `overridable` group fields), with the same secret rules; `add_group_member` upserts them without a revision; there is no host-level variable store and no per-host effective-vars tool.

Behaviour:
- `set_member_vars(group_id, host_id, vars, revision)` — replaces the membership's variables with the group schema's rules (only declared host-scope or overridable keys; secret fields only `{"$secret":"keep"}`), guarded by the membership's revision.
- `get_host_vars(host_id)` — the host's effective, merged variables as the inventory renders them (secrets masked), with which group each value comes from.
- No new host-level variable store: a per-host value is a host-scope (or overridable) field on the group whose schema owns it. The briefing's "group schema field marked per host" is exactly the existing `scope: host` / `overridable`. *(Claude's call.)*

### G. Smaller items (P3)

- `get_repo` shows the last successful sync's commit and time (`last_synced_commit`, `last_synced_at`) next to `last_sync`.
- `sync_repo` on a repository with a sync queued or running returns that job (200, `already_running: true`) instead of queuing another (REST and MCP).
- Host rename: the host form says the name is the inventory name roles derive names from.

## Constraints

- Layers, tests, migrations (additive), version skew and upgrade notes per `CLAUDE.md` and `docs/upgrades.md`. Worker/manager skew: only the commit pin needs worker support (a claim flag; see A); enrollment needs none.
- `create_job_template` and `update_job_template` merge only whitelisted keys (`mcp/tools_write.go`): add `agent_enrollment` there.
- MCP tools follow the existing pattern: each tool maps to one REST route through `mcp/caller.go`; authorization stays in REST.
- Public docs never name the tech stack; user docs in the docs repo for every visible change.

## Left to the builder

Table and column names, the exact generalisation of `run_requests` (columns vs. a kind + payload), how the picture renders each kind, the capability flag names, how the per-host secret travels through inventory rendering, test layout.

## Delivery order

1. A (overrides, commit pin, sync_first, expiry) — foundation for B's picture and fingerprint.
2. B (enrollment) — the P1 rollout blocker.
3. C (schedule and trigger requests).
4. D, E, F, G — independent, small.
