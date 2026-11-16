# Job chaining: follow-up runs (backlog 2.116) — design

Status: approved by the owner on 2026-11-16 ("Freigegeben"). Decided with the owner on 2026-11-16:

- A follow-up starts only when the **whole run** succeeded (option A). A run
  that failed on one host of five starts no follow-up at all.
- A follow-up runs on **its own scope, narrowed to the hosts the first run
  actually ran on**. "Ran on" is read from the run's per-host results, not from
  the hosts it targeted (see "Host selection").
- At a manual launch the run dialog shows the template's follow-ups
  preselected; the operator may **deselect them and add others** for this run.
- The build is an **order table plus a background starter** (approach 1 below).
- **Retry failed** starts the original run's follow-ups on the hosts the retry
  itself played on (option a); the hosts that succeeded the first time get the
  follow-up at the next regular run.

The owner did not object to these proposals made in the same conversation, so
they stand unless the review changes them: cycles are refused when a template is
saved and checked again at run time, with a depth limit; a follow-up honours the
maintenance guard of the schedule its chain started from, and a chain started by
hand or by a webhook ignores windows as those runs do today; a follow-up whose
template is already running is queued, not dropped; a cancelled run starts no
follow-up; follow-ups of a scheduled chain notify through that schedule's rules;
the run detail links parent and children; a background audit event
`chain.run`; success is the only condition in this cut.

Corrected after the builder check (the owner had accepted the earlier wording,
which rested on a wrong reading of the queue): a follow-up whose template is
already running does not "wait in the queue" on its own — nothing in the queue
serialises runs of one template. The starter therefore **waits** until no run of
the follow-up template is active, and only then starts it. **(Claude's call)**

Every decision below not listed here is **(Claude's call)**.

## Why

The noidee-cloud playbooks are being split into one playbook per feature, with
inventory groups deciding where each runs (`cloudpanel.yml` for group
`cloudpanel`, `listmonk.yml` for group `listmonk`). Today listmonk runs in the
same play right after cloudpanel, so a listmonk update only ever runs on a host
that has already gone through its OS updates and reboot. Two playbooks lose that
order unless Pulsix can start the second one when the first one succeeds.

Pulsix has schedules, webhook triggers and failure notifications, but nothing
that starts a run because another run finished.

## Goals

- A job template names follow-up templates. When a run of it succeeds, Pulsix
  starts each follow-up, exactly once, on the hosts the run covered.
- A manual launch can change the follow-ups for that one run.
- A chain is visible: a follow-up run says which run started it, and a run lists
  its follow-ups with their state and the reason when one did not start.
- Unattended chains (started by a schedule) behave like the schedule: they honour
  its maintenance guard and notify through its rules.
- Cycles (A → B → A) cannot be configured and cannot run.

## Non-goals

- Follow-ups "on failure" or "always" (the data model leaves room; the host
  question for a failed run is unsettled, see "Later").
- Passing variables, overrides or results from a run to its follow-up.
- Follow-ups per host (option B): a host that succeeded in a partly failed run
  gets no follow-up.
- Follow-ups of ad-hoc commands, connection tests, syncs or key rotations: only
  template runs (`kind = 'run'`) chain.
- Delays between parent and follow-up, or waiting for several parents (fan-in).
- Per-template permissions (Pulsix has none today).

## Anti-goals

- A follow-up that runs on a host the first run did not finish on. That is the
  whole point of the feature: listmonk only on a settled host.
- A follow-up that silently goes missing. Every planned follow-up ends in a
  visible state: started, or skipped with a reason.
- A follow-up that starts twice (several managers, a crash, a retry).

## How it works today (the parts this touches)

- A run of a template is a **run group**: one job per zone sharing
  `run_group_id`. Every template launch goes through
  `jobs.Service.EnqueueRunWith(ctx, org, template, RunRequest)`
  (`internal/jobs/run_request.go`), except **Retry failed**: `RetryFailed`
  (`internal/jobs/service.go`) works on one job (one zone), takes only that job's
  failed and unreachable hosts, and calls `enqueueRunScope` directly as a manual
  run with the template's limit.
- The claim renders an inventory of the job's own `host_ids` only. But
  `host_ids` is **not** the set of hosts a run touches: a limit (the template's or
  an override) never narrows it — the job keeps the zone's full set and Ansible
  applies the limit (`scopeResolver.Resolve`); hosts with a changed host key or
  behind a changed jump host are dropped at claim time but stay in `host_ids`;
  and the playbook's own `hosts:` pattern narrows further. Only `OnlyHost`
  (a run from the host page) narrows `host_ids` itself.
- What a run did touch is in each job's `stats`, written by the worker from the
  recap (`internal/worker/runner/stats.go`): `ok_hosts` (every host the recap
  counted, failed or not), `changed_hosts`, `skipped_hosts`, `failed_hosts`,
  `unreachable_hosts`, as host UUIDs where the claim knew one and bare names
  otherwise (an implicit localhost). `runGroupQuery` and `failedHostIDs` already
  intersect these lists with `host_ids`.
- When the last job of a group becomes terminal, `SQLQueue.terminate`
  (`internal/jobs/terminal.go`) calls the single `RunGroupSink` inside the
  finishing transaction, exactly once per group (`run_group_reported`). A sink
  error is logged and dropped: the sink is at most once.
  `SweepUnreportedRunGroups` re-reports groups a crash skipped. The only sink is
  `notify.Outbox`, which acts on `trigger = 'schedule'` only.
- Starting another run from inside the sink is not possible: the sink runs in the
  finishing transaction and `EnqueueRunWith` opens its own on a second
  connection, which can block on the first one's row locks.
- Schedules apply a maintenance guard (`ignore | skip | exclude_hosts`) and an
  overlap guard before they enqueue (`internal/schedules/decide.go`) and record
  each decision in `schedule_runs`. Manual and webhook launches ignore windows.
- `RunRequest.IdempotencyKey` makes an enqueue a no-op replay of an earlier one
  with the same key (migration 00067). Schedules use it.
- `jobs.depends_on` orders single jobs (key rotation); it is not used here.

## Approaches

1. **Order table plus a background starter (chosen).** A launch writes its
   follow-ups into `run_follow_ups`, each `pending`. When the run finishes, a new
   run-group sink marks them `due` or `skipped` in the finishing transaction. A
   background starter picks up `due` rows and starts each follow-up through
   `EnqueueRunWith` with an idempotency key per parent and template. The same
   table drives the run detail. A sweep catches rows the at-most-once sink
   missed.
2. **Create the follow-up jobs at launch, gated by `depends_on`.** Rejected:
   `depends_on` links single jobs, not groups; the follow-up's hosts and its zone
   split are only known once the parent has finished; the follow-up would sit in
   the run list as `queued` for the parent's whole duration.
3. **Enqueue directly from the sink.** Rejected: it deadlocks against the
   finishing transaction, and a sink error would lose the follow-up silently.

## Data model

One migration (number taken at implementation time; `main` is at 00098, and an
unmerged branch may take 00099).

### `job_template_follow_ups`

The follow-ups configured on a template.

| column | type | notes |
|---|---|---|
| `org_id` | uuid | RLS like every org table |
| `template_id` | uuid | FK `job_templates`, `ON DELETE CASCADE` |
| `follow_up_template_id` | uuid | FK `job_templates`, `ON DELETE RESTRICT` |
| `condition` | text | `CHECK (condition IN ('success'))`, default `'success'` |
| `position` | int | display and start order |

PK `(template_id, follow_up_template_id)`; a template cannot name itself
(`CHECK (template_id <> follow_up_template_id)`).

`ON DELETE RESTRICT` on the follow-up side: deleting a template that another
template names as a follow-up is refused with 409 naming those templates.
Deleting it silently would break the chain unnoticed (cloudpanel keeps running,
listmonk never does). The same holds for an installation admin deleting a shared
template definition whose projection an organization names as a follow-up:
`DeleteTemplateWithProjections` (`internal/shared/store_sql.go`) gets an explicit
check returning `InUseError{Reasons: ["a follow-up"]}`, like its job-trigger
check. **(Claude's call)**

### `run_follow_ups`

The follow-ups of one run group, snapshotted at launch, and what became of each.

| column | type | notes |
|---|---|---|
| `id` | uuid | PK |
| `org_id` | uuid | RLS |
| `parent_group_id` | uuid | the run group that triggers it |
| `template_id` | uuid | the follow-up template; no FK (outlives it, like `jobs.template_id`) |
| `condition` | text | `'success'` |
| `position` | int | |
| `state` | text | `pending`, `due`, `started`, `skipped`, `dismissed` |
| `reason` | text | why it was skipped or dismissed; empty otherwise |
| `child_group_id` | uuid null | the run group it started |
| `host_ids` | uuid[] | set when it becomes `due`: the parent's hosts |
| `root_trigger` | text | the trigger of the chain's first run (`manual`, `schedule`, `webhook`) |
| `root_ref` | uuid null | the schedule or webhook trigger id of the chain's first run |
| `depth` | int | 1 for a follow-up of a non-chained run |
| `path` | uuid[] | template ids from the chain's first run down to the parent |
| `configured_by` | uuid null | the user who launched the root run, or who set the run's own follow-ups |
| `created_at`, `decided_at`, `started_at` | timestamptz | |

Unique `(parent_group_id, template_id)`. Partial index on `state = 'due'` for
the starter and on `state = 'pending'` for the sweep.

### `jobs.trigger`

The CHECK gains `chain`. A chained run has `trigger = 'chain'` and
`trigger_ref = <parent run group id>`. `RunRequest.validate` requires the ref for
`chain`. The SPA's `TRIGGERS` list gains it ("Follow-up").

### Audit actor kind

A new actor kind `chain` for the starter's audit events, actor id = the parent
run group. (`audit_log.actor_kind` is plain text, no CHECK.)

## Behaviour

### Configuring follow-ups on a template

- The template's create and update bodies gain `follow_ups: [{template_id}]`
  (order = position). The read DTO returns them with each template's name. An
  update that omits the field keeps the stored list; an empty list clears it.
  The template's `revision` covers the list, so the 412 three-way merge applies.
- Validation, inside the template write transaction, under an org-scoped
  advisory lock so two concurrent saves cannot each close half a cycle
  (the pattern of `WithTreeLock`/`WithChainLock`, backlog 2.15):
  - every named template exists in the org and is not the template itself;
  - no duplicates; at most 10 follow-ups per template;
  - the graph with the new edges stays acyclic. A cycle is refused with 422 and
    the path in the message: "follow-ups would form a cycle: cloudpanel →
    listmonk → cloudpanel".
- Permission: `templates.write` as for any template change, plus `jobs.run`,
  because a follow-up starts runs. Both are operator today; checking both keeps
  the rule right if the matrix ever splits them. **(Claude's call)**
- Shared (projected) templates: follow-ups are an org-local field and may be set
  on a projection; `refuseSharedFieldChange` does not cover them. **(Claude's call)**

### Launching a run

- `RunRequest` gains `FollowUps *[]uuid.UUID`: nil means "the template's", a
  non-nil list replaces them for this run (deselect and add). Only the manual run
  route accepts it in its body (`follow_ups`, optional). Schedules and webhooks
  always use the template's.
- A manual override is validated like the template list (existence, no self,
  no duplicates, at most 10) and refused with 422 if it would put a template on
  the chain's path twice. Permission: `jobs.run`, already required to launch.
- After the run group's jobs are created, one helper shared by `EnqueueRunWith`
  and `RetryFailed` writes one `run_follow_ups` row per follow-up, `pending`, with `depth = 1`,
  `path = [template]`, `root_trigger`/`root_ref` = the request's trigger and ref,
  `configured_by` = the actor. A launch that created no job (empty scope, every
  host excluded) writes none.
- Snapshot semantics: editing the template after the launch does not change the
  running run's follow-ups.
- **Retry failed** (owner's choice a): `RetryFailed` copies the follow-ups of
  the original job's group (template, condition, position, root, path, depth)
  into the retry group as `pending`. When the retry succeeds they become `due` on
  the hosts the retry played on, which are at most the failed hosts it retried.
  The original group's own rows stay `skipped` ("the run failed"). A failed run
  across two zones needs two retries; each starts the follow-ups for its own
  hosts. A retry whose original group had no follow-ups writes none;
  `configured_by` is the user who retried.

### When a run group finishes

A new `RunGroupSink`, `chain.Sink`, runs alongside `notify.Outbox` through a
small fan-out sink (`jobs.RunGroupSinks{a, b}`), each in its own savepoint so one
failing does not undo the other. For the group's `pending` rows:

- group `succeeded` → each row becomes `due`, `host_ids` = the hosts the run
  played on (see "Host selection");
- group `failed`, `error` or `cancelled` → each row becomes `skipped` with reason
  "the run failed" / "the run was cancelled".

The sink only updates rows in the finishing transaction; it starts nothing.

### The starter

`chain.Starter`, a ticker in every manager (the schedules ticker pattern, default
tick 5 s), each tick:

1. **Sweep:** `pending` rows whose parent group has no non-terminal job any more
   and that are older than a minute are resolved as the sink would have
   (covers a sink error, which is dropped, and a crash).
2. **Start:** locks a batch of `due` rows `FOR NO KEY UPDATE SKIP LOCKED`, and for
   each row:
   1. If the follow-up template no longer exists → `skipped`, "the template was
      deleted".
   2. If `depth` ≥ 10, or the template is already on `path` → `skipped`,
      "the chain is too long" / "the template is already in this chain". (Save
      time refuses cycles; this catches manual overrides and templates edited
      while a chain is in flight.)
   3. **Maintenance**, only when `root_trigger = 'schedule'` and that schedule
      still exists: its guard applies to the follow-up's hosts as in
      `Decide` — `skip` skips the follow-up if any of them is in a window,
      `exclude_hosts` leaves those out (all out → `skipped`), `ignore` does
      nothing. A chain started by hand or by a webhook ignores windows, as those
      launches do today. A deleted root schedule falls back to `ignore`.
   4. **Wait for an active run:** while any run of the follow-up template is
      active (`HasActiveTemplateJob`), the row stays `due` with reason "waiting
      for the active run of <template>", shown on the run detail, and is tried
      again next tick. This does not count towards the attempt limit and has no
      timeout: an active run always ends, the reaper sees to stuck ones.
      Dropping the follow-up would leave the host without the second step;
      enqueueing it at once could run the same playbook twice on one host at the
      same time, because the queue does not serialise runs of one template.
      **(Claude's call)**
   5. Enqueue through `EnqueueRunWith` with `Trigger: chain`,
      `TriggerRef: parent group`, `Actor: nil`, `OnlyHosts: row.host_ids` (new,
      below), `IdempotencyKey: "chain:<row id>"`.
   6. Outcome:
      - jobs created → `started`, `child_group_id` set; the child group's own
        follow-ups are written as in "Launching a run", from the follow-up
        template's list, with `depth + 1`, `path + [template]` and the root
        carried over;
      - `ErrEmptyScope` / `ErrHostNotInScope` / every host excluded →
        `skipped`, "none of the hosts is in this template's scope" (for example
        web-node-1 after cloudpanel: it is not in the listmonk group);
      - other errors → the row stays `due` and is retried next tick with
        backoff; after 5 attempts → `skipped` with the error.
   7. Audit `chain.run` (actor kind `chain`, object `job_template` = follow-up,
      meta: parent group, child group, root trigger/ref, depth, outcome,
      reason, configured_by).

### Host selection

The hosts a follow-up inherits are the hosts the parent **played on**, read when
the parent's group has succeeded: for each job of the group, the UUIDs in
`ok_hosts ∪ changed_hosts ∪ skipped_hosts`, minus `failed_hosts ∪
unreachable_hosts`, intersected with the job's own `host_ids` (as
`runGroupQuery` does); bare names (implicit localhost) are ignored. This covers
every way a run is narrowed — template limit, override limit, `hosts:` pattern,
claim-time exclusions — because only hosts the play actually reached appear in
the recap. A host whose every task was skipped still went through the play and
counts. **(Claude's call)** An empty set skips the follow-up with "the run played
on no host".

`RunRequest` gains `OnlyHosts []uuid.UUID`: the resolved per-zone targets of the
follow-up template (its own scope and its own limit) are intersected with it,
exactly like `onlyHost` but for a set. Zones left empty are dropped; nothing
left → `ErrHostNotInScope`. Changed-key exclusion and the pinned-host rule run
after it as for every run. The follow-up's own limit is unchanged; nothing is
written into the sealed limit, because the inventory only ever holds the job's
`host_ids`.

Example: cloudpanel runs on `k10001-cp-01` and `web-node-1` and succeeds.
listmonk's scope is group `listmonk` = {`k10001-cp-01`}. The follow-up runs on
`k10001-cp-01` only. Had cloudpanel been launched with limit `web-node-1` (from
the run dialog, a schedule override or a webhook), only `web-node-1` is in its
recap and the follow-up is skipped with "none of the hosts is in this template's
scope", even if `k10001-cp-01` shares the zone.

### Dismissing pending follow-ups

On a run that is still active, an operator (`jobs.run`) can dismiss its pending
follow-ups ("Do not start follow-ups"): `pending` → `dismissed`, reason
"dismissed by <user>", audited as `POST dismissFollowUps`. Cancelling a run needs
no extra step: a cancelled group skips its follow-ups. A follow-up already
`started` is an ordinary run and is cancelled as one. **(Claude's call)**

### Notifications

`notify.Outbox.RunGroupFinished` today ignores everything but
`trigger = 'schedule'`. It also handles `trigger = 'chain'`: it looks up the
`run_follow_ups` row whose `child_group_id` is the group; when its
`root_trigger = 'schedule'` and the schedule still exists, it enqueues the run
event under that schedule's rules, with the follow-up template as `Template` and
a "follow-up of <parent template>" line. Skips notify like schedule decisions,
by reason **(Claude's call)**:

- skipped for maintenance → the schedule's `skipped` category;
- deleted template, cycle or depth stop, start given up after 5 attempts →
  `failure` (someone has to act);
- no host in scope, played on no host, parent failed or cancelled, dismissed →
  nothing (routine, or the parent already notified).

Dedupe key `chain:<row id>`. Chains started by hand or by a
webhook notify nothing, as their first runs do today (backlog 2.72).

### API

- Templates: `follow_ups` on create/update bodies and on the read DTO
  (`[{template_id, template_name, position}]`).
- `POST /orgs/{org}/job-templates/{id}/run`: optional `follow_ups: [uuid]`.
- `GET /orgs/{org}/jobs/{id}` (and the run group read the run detail uses) gains
  `chain: {parent: {group_id, first_job_id, template_id, template_name} | null,
  follow_ups: [{template_id, template_name, state, reason, child_first_job_id,
  depth}]}`.
- `POST /orgs/{org}/jobs/{id}/follow-ups/dismiss` (`jobs.run`).
- Template delete: 409 `follow-up of: <names>` when referenced.
- The OpenAPI and `web/src/api/schema.d.ts` are regenerated (`npm run api:gen`).

## SPA

- **Template editor** (`TemplateDialog.tsx`): a "Follow-ups on success" field, a
  `MultiSelect` of the org's templates without the one being edited, in order,
  with a short help text ("Started on the same hosts when a run of this template
  succeeds"). A 422 cycle message shows inline on the field.
- **Run dialog** (`RunTemplateDialog.tsx`): a "Follow-ups" section, the
  template's follow-ups preselected, editable with the same picker; sent only
  when it differs from the template's.
- **Run detail** (`RunDetailPage.tsx`):
  - a "Started as a follow-up of <template> run" line with a link to the parent
    run when `trigger = 'chain'`;
  - a "Follow-ups" section listing each follow-up: template, state badge
    (waiting / started / skipped / dismissed), reason, and a link to the child
    run; a "Do not start follow-ups" action in the sidebar while some are
    pending and the run is active.
- **Runs list:** trigger "Follow-up" in the trigger column and filter.
- en and de catalogue strings for all of it.

## Docs (noix-docs, branch `pulsix`)

`user/playbooks-and-jobs.md` gains a "Follow-up runs" section: what it does,
the hosts rule, cycles, the delete refusal, the run-dialog override, the run
detail's chain lines and dismissing. `user/schedules.md`,
`user/maintenance-windows.md` and `user/notifications.md` each get a short note
that follow-ups of a scheduled run follow the schedule's maintenance guard and
notification rules, and that chains started by hand ignore windows. Work happens
on a docs branch cut from `pulsix`, merged into `pulsix` when the feature
ships. No tech stack named.

## Tests

- **Unit** (`internal/chain`, fakes): sink transitions per group status; starter
  outcomes (started, skipped for deleted template, depth, cycle on path, no hosts
  in scope, maintenance skip and exclude, retry then give up); idempotent replay
  of a start; cycle detection on save (direct, indirect, self, concurrent saves).
- **jobs:** `OnlyHosts` intersection (several zones, empty result), validate
  `chain` trigger, follow-up rows written on launch and on retry.
- **Integration** (`testdb`): the migration, RLS on both tables, the delete
  refusal, the sweep, `SKIP LOCKED` with two starters.
- **API** (`apitest`): template follow-ups round trip, cycle 422, delete 409,
  run override, run detail chain block, dismiss, permissions.
- **E2E** (`internal/e2e`): template A with follow-up B; B's scope holds the
  scenario host and a second managed host that A's scope also holds but A's
  limit leaves out (so it is in A's `host_ids` but not in its recap); run A, await
  A, await B; assert B's trigger is `chain`, its ref is A's group, its
  `host_ids` is only the scenario host, and A's run detail lists B as started.
  Second case: A fails → B skipped with reason. Third: saving B → A is refused.
- **SPA** (Vitest): editor field and cycle error, run dialog override, run detail
  parent link and follow-ups section, dismiss.
- **Playwright:** one journey through the template editor, a run, and the
  follow-up's link on the run detail, if the e2e fixture's fake runner supports
  two runs in a row (it does for retries).

## Later

- Follow-ups on failure or always (rollback or report playbooks), with an answer
  to which hosts they run on.
- Follow-ups per host (option B), once Pulsix can tell a host that finished the
  play from one an aborted play left "ok".
- Passing variables from parent to child.
