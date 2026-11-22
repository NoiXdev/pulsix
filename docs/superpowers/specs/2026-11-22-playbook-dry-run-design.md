# Playbook dry run and outline — design

Backlog 3.126, the second half of the playbook pages (spec
`2026-11-19-playbook-pages-design.md` §5). Owner's request, 2026-11-19: playbook pages
with "validate, dry run, latest runs"; the owner approved the mockup
https://claude.ai/artifact/T9Mr66pttSSspSkqgyF2Bv and the order "pages first, dry run
later". This document turns the mockup's **Probelauf** tab, the **Gliederung** side
panel, the header's tag chips and the runs tab's **Probeläufe** filter into a buildable
design. Decisions marked *(controller's call)* were not discussed with the owner.

## Goals

- From a playbook's page, run it in Ansible's check mode against the hosts of one of
  the templates that use it, optionally narrowed to some hosts and some tags, with
  diffs, optionally on the unsaved draft, and see what would change.
- A server-side outline of an editor playbook: plays, tasks, handlers, tags, lines.

## Non-goals

- Dry runs from schedules, webhooks, chains, run requests (assistant approval), or
  the templates list. Only the playbook page starts one.
- An outline of repository playbooks (their text is not in Pulsix yet: backlog
  3.127 (a)); their page shows the outline panel's "not available for repository
  playbooks" note.
- Skip-tags, free-text `--limit`, extra-vars overrides, forks.

## Anti-goals (what would make this a failure)

- A dry run that changes a host without the user being told it can. Ansible's check
  mode is not a sandbox: a task with `check_mode: false` runs for real, and modules
  without check support are skipped. The form says so, and the outline names the
  tasks that will run for real.
- An older worker receiving a dry-run job, ignoring the flag and running the play
  for real.
- A dry run reaching hosts the template could not reach, or a draft running with
  less permission than saving it needs.

## 1. Server: starting a dry run

`POST /api/v1/orgs/{org}/playbooks/{id}/dry-run`, body:

| field | meaning |
|---|---|
| `template_id` (required) | a template whose playbook is `{id}` — else 422 |
| `host_ids` (optional) | narrow to these hosts; they must be among the template's resolved targets (the `OnlyHosts` semantics of `RunRequest`); none left → 422 |
| `tags` (optional) | `--tags`; at most 32, each `^[A-Za-z0-9_.:-]{1,64}$` — else 422 |
| `diff` (bool, default true) | `--diff` |
| `draft` (optional string) | the unsaved YAML of an **editor** playbook; validated like a save (`playbooks.ValidateYAML`, `MaxInlineBytes`); a repository playbook with `draft` → 422 |

- **Permission:** `jobs.run` (as running the template); with `draft`, also
  `playbooks.write` (a draft is an edit that has not been saved). An interactive
  session is not required (a token with both permissions may dry-run). *(controller's call)*
- Response: as the template run route (`jobStartOutput`), status 202.
- It enqueues through `EnqueueRunWith` with `Trigger: manual`, no follow-ups, no
  chain; the request's check flag, diff flag, tags and draft travel in the sealed
  payload (`RunOverrides` gains `Check`, `Diff`, `Tags`, `Playbook` — the last only
  with `Check`). The service refuses a `Playbook` override without `Check`
  (defence in depth: a draft never runs for real).
- **Job row:** migration adds `jobs.check_mode boolean NOT NULL DEFAULT false`
  (additive), set from `Check`, so lists and filters see it without unsealing.
- **Claim:** the claim reads the override; with `Playbook` set the worker gets the
  draft text instead of `playbookRef(tmpl)`; the worker job (`workerpb.Job`) gains
  `check`, `diff` and `tags`.
- **Version skew:** the worker announces support on every claim
  (`ClaimJobRequest.supports_check_mode`, the pattern of `cancel_stops_play`), and the
  claim never hands a `check_mode` job to a worker that does not announce it — the job
  stays queued (and the usual "no worker" signals apply). Test: an old-style claim
  without the field never receives a check-mode job. Follow `docs/upgrades.md`.
- No follow-ups, no chains, no retry-failed for a dry run (the retry route answers
  409 with a reason); a dry run's outcome does not fire run-outcome notifications
  or chain steps if such hooks exist *(the builder checks which exist)*.
- **Audit:** like a template run, with `check_mode`, tags, host count and, for a
  draft, the SHA-256 of the draft (not its text).

## 2. Worker

- With `check`: `--check`; with `diff`: `--diff`; with tags: `--tags a,b`. Passed
  through ansible-runner 2.4.3 (`deploy/worker/Dockerfile`) as argv, never through a
  shell — the builder verifies the runner's options (`--cmdline` or `env/cmdline`)
  against the pinned version with a test of the built command.
- Diffs arrive in the event payload (`event_data.res.diff`); they go through the same
  redaction as every event payload (test: a credential's secret inside a diff is
  redacted in the stored event).
- **Recap counts:** `buildStats` additionally stores per-host counts
  `host_counts: {<host-id>: {ok, changed, skipped, failed, unreachable}}` from
  `playbook_on_stats` (additive in `jobs.stats`; existing keys unchanged). Every run
  gets them, not just dry runs.

## 3. Server: outline

`POST /api/v1/orgs/{org}/playbooks/outline` with `{content}` (the editor's text,
`MaxInlineBytes`), permission `playbooks.read`; pure function
`playbooks.Outline(content) (Outline, error)` in `internal/playbooks`, yaml.v3 nodes:

- plays: name (or `hosts` when unnamed), hosts, `become`, line, tags;
- per play, in order, sections `pre_tasks`, `roles`, `tasks`, `post_tasks`,
  `handlers`; entries: name (or the module key when unnamed), line, depth (inside
  `block`/`rescue`/`always`, to depth 64), effective tags (own plus inherited from
  play and blocks), `runs_in_check_mode: false` when `check_mode: false` is set on
  the entry or an enclosing block/play; includes, imports and roles as entries of
  their own (not followed);
- `tags`: the sorted union of all tags; counts (plays, tasks, handlers).
- Caps: 2 000 entries (then `truncated: true`). Invalid YAML → 422 with the
  validation message, as save does.
- Table-driven tests for each rule above.

## 4. SPA

- **Header:** tag chips from the outline (editor playbooks) and the summary line
  gains "· N plays · N tasks · N handlers". A **Dry run…** button beside **Run…**
  opens the Dry run tab. Shown to anyone with `jobs.run`.
- **Editor tab side column:** **Outline** below Check: the play line, then entries
  with line, name, tags; a click moves the editor's cursor to the line and focuses it.
  Entries that run for real in check mode carry a warning marker. Fetched debounced
  from the draft (about 400 ms), keeping the last good outline while the YAML is
  invalid.
- **Dry run tab** (`?tab=dry-run`, between Editor and Runs): the explanation (check
  mode changes nothing except tasks marked to run anyway; modules without check
  support are skipped), template select (templates using the playbook; none → link
  to creating one), hosts (optional, chosen from the template's hosts with the
  existing host picker; empty = all), tag toggles from the outline, **Show
  differences** (on), **Check the unsaved draft** (only for editor playbooks, only
  while dirty, needs `playbooks.write`), the warning listing tasks that run for
  real, **Start dry run**. Then the **latest dry run** of this playbook (the newest
  run with `check_mode` among its runs; follows live while running): counts ok /
  would change / skipped / failed / unreachable summed over hosts, then each
  changed task per host with its diff (`res.diff`: `before`/`after` as a unified
  diff computed by a small tested line-diff helper, `prepared` shown as is; large
  diffs collapsed), a link to the run.
- **Runs:** `GET /runs` gains `check_mode=true|false`; the runs list and the
  playbook's Runs tab get a **Dry runs** filter and a *Dry run* badge in the status
  or kind cell; the run detail page shows a *Dry run* banner (with tags and
  "draft" when it ran one), counts from `host_counts`, and renders diffs in the
  event log the same way.
- en + de ("Probelauf", German "du").

## 5. Left to the builder

Component structure, the exact DTO names beyond those above, how the run detail
folds counts into its existing host table, the diff helper's algorithm.
