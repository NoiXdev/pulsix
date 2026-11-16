# Follow-ups of approved run requests (backlog 2.122) — design

Status: approved by the owner on 2026-11-16 ("passt"). Decided with the owner on 2026-11-16:

- **Option C:** an organization setting decides whether an approved run request
  starts the template's follow-ups. One value shows the follow-ups on the approval
  page and approves them together with the run (option A); the other starts none
  (option B).
- **B is the default**, for every organization without a stored choice, existing
  ones included after the upgrade. An admin turns A on deliberately, as assistant
  access itself is opt-in.
- A follow-up edited **after** the approved run's claim is not caught; that is
  documented as a limit, not closed (option i). The owner weighed storing each
  follow-up's approved revision and having the starter skip a changed one, and
  chose not to.

Every decision below not listed here is **(Claude's call)**.

## Why

Assistant access (backlog 2.116) lets an AI assistant file run requests that a
person approves. The approval binds to a fingerprint of the *run picture* the page
showed; the claim recomputes it, so nothing runs that the person did not see.
Job chaining (backlog 2.121) landed right after it and lets a template name
follow-up templates that start when a run succeeds. The two met only at the
rebase: an approved run request launches through `EnqueueRunWith`, which now also
plans the template's follow-ups (pinned by
`TestApprovedRunRequestPlansFollowUpsInItsGroup`, commit b941c76a). So:

- an approver who approves template X also starts X's follow-ups, and theirs
  (a chain is up to 10 runs deep), without the page showing any of them;
- the fingerprint does not cover the follow-up list, so a change to it between the
  page load and the click, or before the claim, is not caught;
- the follow-up runs are their own run groups, so the claim guard
  (`runrequests.Service.CheckRunClaim`) never sees them.

That breaks an anti-goal of the assistant-access spec: "an approval that runs
something other than what the person saw".

The owner's own chain (cloudpanel, then listmonk on the hosts cloudpanel finished
on) is the reason not to simply drop follow-ups for every organization: a second
request for listmonk would run on listmonk's whole scope, not on the hosts
cloudpanel played on, which is what chaining exists to prevent.

## Goals

- An organization chooses whether approved run requests start follow-ups.
- With follow-ups off (the default) an approval starts none, and the approval page
  says which ones it leaves out.
- With follow-ups on, the approval page shows every follow-up the approval would
  start, the follow-ups of those included, and the person may deselect direct
  follow-ups before approving, as in the run dialog.
- The fingerprint covers the setting and the follow-ups shown, so a change between
  the page load and the click refuses the approval, and a change before the claim
  fails the run, as for every other part of the picture.
- Run requests of templates without follow-ups keep their exact fingerprint, so an
  approved run queued across the upgrade does not fail for nothing.

## Non-goals

- Showing a follow-up's own run picture (playbook, targets, credentials, vars).
  The page shows each follow-up by name and revision. (The run dialog of a manual
  launch shows follow-ups by name only; the approval page's tree is new UI.)
- Guarding a follow-up run itself at its claim or start (owner's choice). It
  starts later, when its parent has succeeded, as an ordinary chained run; edits
  to it after the parent's claim are not caught (see "Limits").
- Letting the assistant choose follow-ups (`request_run` takes no `follow_ups`).
  The assistant cannot set a template's follow-ups either: that needs `jobs.run`,
  which no area grants.
- Adding follow-ups at approval that the template does not name.
- A per-token or per-template setting.

## Anti-goals

- An approval that starts a follow-up the page did not show, in either mode.
- An approval in the default mode that starts any follow-up.
- A setting that an assistant token (or any token) can change.
- A fingerprint change that fails approved runs of templates without follow-ups.

## How it works today (the parts this touches)

- **Settings:** `org_assistant_settings (org_id PK, enabled, updated_at)`, absent
  row = off (migration 00100). `GET/PUT /orgs/{org}/settings/assistant`, read
  `org.settings.read`, write `org.settings.write` in an interactive session only
  (`internal/api/assistant_settings_handlers.go`). Service in
  `internal/assistant`. SPA: `web/src/features/settings/AssistantCard.tsx` on
  Settings → General.
- **Picture and fingerprint:** `runrequests.Resolver.Picture`
  (`internal/runrequests/resolve.go`) builds a `Picture`
  (`internal/runrequests/picture.go`); `Fingerprint` hashes the request id and the
  canonical JSON of the picture. The detail route returns the picture verbatim as
  JSON with the fingerprint. `Service.Approve` recomputes and compares, records
  the decision with the fingerprint (`approved_fingerprint`, migration 00103),
  then enqueues: `Enqueuer.Run` → `jobs.Service.EnqueueRunWith` (trigger manual,
  actor = approver, `RunGroupID` = request id, idempotency key
  `run-request:<id>`), or `Enqueuer.RetryFailed` → `jobs.Service.RetryFailedInto`.
  `CheckRunClaim` recomputes the picture at every claim of a job in the request's
  run group and fails the job on a mismatch.
- **Follow-ups at launch:** `EnqueueRunWith` calls `planFollowUps`
  (`internal/jobs/follow_ups.go`) before creating jobs: `RunRequest.FollowUps`
  nil plans the template's own list, a non-nil list (manual trigger only, checked
  by `checkFollowUpOverride`) replaces it. `RetryFailedInto` calls
  `FollowUpPlanner.CopyFollowUps`, which copies every `run_follow_ups` row of the
  original job's run group into the retry's group (`CopyRunFollowUps`,
  `internal/db/queries/chain.sql`). The rows are a snapshot: editing the template
  later does not change them.
- **Deeper levels:** when a follow-up's run starts, the starter plans *its*
  follow-ups from the follow-up template's list at that moment, depth + 1, and
  skips a template already on the chain's path or at depth 10
  (`internal/chain/starter_decide.go`). At most 10 follow-ups per template.
- **Template revision** moves on with every template write, the follow-up list
  included.

## Approaches

The owner weighed three:

- **(A) Show and approve.** The page shows the follow-up tree, the fingerprint
  covers it, the person may deselect. Keeps chains usable through the assistant;
  a follow-up's own content stays unseen, as in the run dialog.
- **(B) Never.** Approved requests start no follow-ups. Strictest and smallest,
  but a chain cannot be started through the assistant, and a separate request
  for the follow-up runs on its whole scope.
- **(C) An organization setting choosing A or B (picked).** More surface (a
  column, a field on the settings route, a switch in the GUI) in exchange for the
  owner choosing per organization. B as the default keeps every organization
  strict until an admin decides otherwise.

## Design

### Data model

Migration 00106 (the next free number on `main`):

```sql
ALTER TABLE org_assistant_settings
  ADD COLUMN approved_runs_start_follow_ups boolean NOT NULL DEFAULT false;
```

An absent row still means "assistant off, follow-ups off". Additive only.

### Setting service and route

- `internal/assistant` gains `StartsFollowUps(ctx, org) (bool, error)` and
  `SetStartsFollowUps(ctx, org, on bool) error` (upsert keeping `enabled`, as
  `SetEnabled` keeps the new column). **(Claude's call)**
- The route DTO gains `approved_runs_start_follow_ups` (bool, doc: "Whether an
  approved run request also starts the template's follow-ups"). GET always
  returns it. On PUT the field is optional (a pointer); omitted keeps the stored
  value, so a PUT from an older SPA that only sends `assistant_enabled` changes
  nothing else. `assistant_enabled` stays required as today. **(Claude's call)**
- Same permissions as the switch: `org.settings.write`, interactive session only.
  The audit summary names each change ("approved run requests start follow-ups" /
  "approved run requests start no follow-ups"); when both fields change in one PUT
  both are named.
- Turning assistant access off does not touch this setting; the setting applies
  to requests that are still pending either way.

### Picture

`Picture` gains one field, last in the struct:

```go
FollowUps *PictureFollowUps `json:"follow_ups,omitempty"`
```

```go
// PictureFollowUps is what an approval does about the template's follow-ups.
type PictureFollowUps struct {
    // Start is the organization setting: true when an approval starts them.
    Start bool `json:"start"`
    // Templates are the direct follow-ups in start order.
    Templates []PictureFollowUp `json:"templates"`
    // Truncated is set when the tree had more than MaxPictureFollowUps nodes;
    // an approval can then start none of them.
    Truncated bool `json:"truncated,omitempty"`
}

// PictureFollowUp is one follow-up template and, when Start is set, the
// follow-ups its own run would start.
type PictureFollowUp struct {
    TemplateID       string            `json:"template_id"`
    TemplateName     string            `json:"template_name"`
    TemplateRevision int64             `json:"template_revision,omitempty"`
    // Stop says why this follow-up would not start: "deleted" (the
    // template is gone, retry only), "in_chain" (already on the chain's
    // path), "too_deep" (past the chain's depth limit).
    Stop             string            `json:"stop,omitempty"`
    FollowUps        []PictureFollowUp `json:"follow_ups,omitempty"`
}
```

- `FollowUps` is nil when there are no direct follow-ups. `encoding/json` then
  omits it, so the canonical JSON, and the fingerprint, of a request without
  follow-ups is byte-identical to today's. A test pins that against a fingerprint
  computed by the current code.
- **Which direct follow-ups:** for `kind = run`, the template's current list
  (`templates.Template.FollowUps`, in position order). For `kind = retry_failed`,
  the `run_follow_ups` rows of the original job's run group, in position order:
  exactly what `RetryFailedInto` copies. A retry row whose template no longer
  exists shows with `Stop: "deleted"` and the empty name the list query returns
  (the SPA shows the short id).
- **Start = false (default mode):** `Templates` holds the direct follow-ups with
  id and name only: no revision, no subtree. The page needs nothing more to say
  which ones the approval leaves out, and keeping revisions out means an edit of
  a follow-up template does not fail an approved run that starts none.
- **Start = true:** each node carries the template's revision and, recursively,
  the follow-ups its own run would start: the follow-up template's current list.
  The walk mirrors the starter: path = [request template, …, node]; a template
  already on the path is a leaf with `Stop: "in_chain"`; a node at depth 10 (the
  request's template is depth 1) has no children and its children would be
  `too_deep` — the walk emits them as leaves with `Stop: "too_deep"` and stops.
  `MaxPictureFollowUps = 100` nodes in total; the walk stops there and sets
  `Truncated`. **(Claude's call)** The rule is the starter's own
  (`internal/chain/starter_decide.go`): a row is skipped when its parent's depth
  is ≥ 10 or its template is on the path. For a run request the walk starts at
  depth 1 with path = [template]. For a retry of a run that was itself a chained
  run, the copied rows keep their `depth` and `path`, so the walk starts from the
  original group's rows' own depth and path, not from 1.
- **Fingerprint:** no change to `Fingerprint` beyond hashing the new field. Order
  matters (start order), so the slices are not sorted.
- **Sources:** a retry picture needs the original job's run group, which
  `JobRunner.RetryTargets` does not return; `RetryTargets` gains it as a third
  return value (its two callers are in `internal/runrequests`). The resolver also
  gains two narrow sources:
  `FollowUpSettings` (`StartsFollowUps`) and `RunFollowUpSource`
  (`ListForGroup(ctx, org, group) ([]chain.FollowUp, error)`, already on
  `chain.SQLStore`). Follow-up templates are read through the existing
  `TemplateSource.Get`; a template that vanishes mid-walk becomes `Stop:
  "deleted"`.

### Approve

- The approve body gains `follow_ups` (optional array of template ids). Omitted
  means "every direct follow-up the picture lists as startable" in mode A and
  "none" in mode B.
- `Service.Approve` takes the selection (`*[]uuid.UUID`). After the fingerprint
  matches, it computes the list to start from the picture it just recomputed,
  never from a fresh read:
  - mode B (`Start = false`) or no follow-ups: an empty list; a non-empty
    selection is `ErrInvalidInput` ("approved run requests start no follow-ups in
    this organization");
  - mode A: the selection must be a subset of the direct follow-ups without
    `Stop`; anything else is `ErrInvalidInput` ("follow-up <id> is not one this
    request would start"). The result keeps the picture's order. With
    `Truncated`, an omitted selection means **none** (not "all"), and only an empty
    selection is accepted when one is sent ("too many follow-ups to show; approve
    without follow-ups or start them by hand"). So no client, an older SPA or a
    direct API call included, can start a tree nobody could see. **(Claude's
    call)**
- The validation runs before `Decide`, so a refused selection leaves the request
  pending.
- **A re-approval after a failed enqueue.** `planFollowUps` and `CopyFollowUps`
  write `run_follow_ups` rows into the request's group *before* the jobs exist,
  and every insert is `ON CONFLICT (parent_group_id, template_id) DO NOTHING`.
  When the enqueue then fails, `RevertToPending` keeps the group (the request id),
  so a second approval would inherit the first attempt's rows: a follow-up
  deselected the second time would still start, or a row the sweep has meanwhile
  marked `skipped` would block the newly selected one. So every approval, before
  it enqueues, deletes the request group's rows in state `pending` or `skipped`
  (rows that became `due` or `started` belong to jobs that ran and are kept). A
  new planner call `ClearUnstartedFollowUps(ctx, org, group)` with its own query;
  the resolver's `Run` and `RetryFailed` call it first. The first approval finds
  none. **(Claude's call)**
- `Enqueuer` changes:
  - `Run(…, followUps []uuid.UUID)`: the resolver passes `FollowUps: &followUps`
    to `EnqueueRunWith`, always non-nil. So the template's list is never read
    again at enqueue: what starts is what the picture showed, even if the template
    changed in the instant between the picture and the enqueue. An empty list
    plans none.
  - `RetryFailed(…, followUps []uuid.UUID)`: `jobs.Service.RetryFailedInto` gains
    a `followUps *[]uuid.UUID` argument; nil copies every row (the existing
    callers: the retry route), non-nil copies only rows whose `template_id` is in
    the list (`FollowUpPlanner.CopyFollowUps` gains the filter; a new query
    `CopyRunFollowUpsOf` with `template_id = ANY(@template_ids)`, or a nullable
    array parameter on the existing one). An empty list copies none.
- Audit: the approval summary gains the follow-ups that start, by name: "approved
  run request (template revision 7; follow-ups: listmonk)" or "…; no follow-ups"
  when the template has follow-ups and none start. Unchanged when the template
  has none.

### Claim

`CheckRunClaim` is unchanged: it recomputes the picture, now with the follow-ups,
and compares. Consequences, all intended:

- mode A: a follow-up template edited (revision moves), added, removed or
  reordered between the approval and the claim of the approved run fails that run
  with the existing "what the run uses changed after it was approved" — even
  though the follow-ups were snapshotted at approval and the edit would not change
  them. Strict, but it is the same rule as for every other field, and the person
  asks again.
- either mode: switching the setting between the approval and the claim fails the
  approved run of a template with follow-ups.
- mode B: renaming, adding or removing a direct follow-up fails the approved run
  (names are in the picture). Accepted as rare.

### API surface

- `GET /orgs/{org}/run-requests/{id}`: `picture.follow_ups` as above.
- `POST /orgs/{org}/run-requests/{id}/approve`: body `{fingerprint, follow_ups?}`;
  422 on an invalid selection.
- `GET/PUT /orgs/{org}/settings/assistant`: `approved_runs_start_follow_ups`.
- OpenAPI and `web/src/api/schema.d.ts` regenerated (`npm run api:gen`).
- MCP: `get_run_request` passes the detail through unchanged, so the assistant
  sees which follow-ups an approval would start. The `request_run` description
  gains one sentence: "Whether an approval also starts the template's follow-ups
  is the organization's setting; the request detail says." **(Claude's call)**

### SPA

- **Approval page** (`RunRequestDetailPage.tsx`, `RunPictureSections.tsx`): a
  "Follow-ups" section, and nav item, when `picture.follow_ups` is present.
  - Mode B: a note "Approving starts none of these follow-ups. Your organization
    has turned that off in Settings → General." and the list of names.
  - Mode A: each direct follow-up with a checkbox, checked by default (as in the
    run dialog), name and revision; under it, indented and read-only, its own
    follow-ups, recursively, each with its revision; a `Stop` shows as a muted
    label ("already in this chain", "chain too long", "template deleted") and
    such a direct follow-up has no checkbox. `Truncated` shows "More follow-ups
    than this page can show; approving starts none of them" and disables the
    checkboxes (unchecked).
  - A one-line hint: "Follow-ups start later, when this run succeeds, on the hosts
    it ran on. This page shows them by name and revision, not what they run."
  - Approve sends `follow_ups` only when the selection differs from the default
    (all startable ones), so a plain approval sends the same body as today.
  - Deselection is local state; a refetch after "changed" resets it to the
    default and the changed notice is shown as today.
- **Settings → General** (`AssistantCard.tsx`): a second switch under the access
  switch, "Approved runs start follow-ups", hint "When on, the approval page shows
  the template's follow-ups and approving starts them too. They start later with
  whatever their templates hold then, so an assistant allowed to edit templates
  can still change them after the approval. When off, an approved run starts
  none." Same write gate as the access switch; usable whether access is
  on or off.
- en and de catalogue strings.

### Limits (documented, not fixed)

- A follow-up is reviewed by name and revision only. Its playbook content,
  repository, inventory and vars are not on the page; a change to them is not
  caught. The same holds for the run dialog.
- A follow-up template edited after the approved run's claim is not caught: the
  follow-up starts later as a normal chained run with whatever the template is
  then (its own follow-ups included). Chained runs are not run requests. In
  particular an assistant token with the `templates.write` area can, in mode A,
  request X, wait for the approval and X's claim, then change a follow-up
  template's playbook, limit, scope or vars; the follow-up runs that content,
  unreviewed, with the approver as `configured_by`. (The same token can already
  edit a template a schedule runs unreviewed.) The settings hint and the user docs
  say so, so an admin knows it before turning mode A on. Owner's decision
  (option i).
- Hosts are not part of the follow-up picture: a follow-up runs on its own scope
  narrowed to the hosts the approved run played on, which nobody can know at
  approval time.

### Upgrades (`docs/upgrades.md`)

Migration 00106 adds one boolean column, default false; nothing is dropped or
rewritten. Behaviour change: **an approved run request no longer starts the
template's follow-ups** unless an admin turns the new setting on. A request's
fingerprint changes only when its picture has follow-ups: a run request whose
template names follow-ups, or a retry request whose original run had follow-ups
planned (whatever the template names now). Every other fingerprint is unchanged.
An approved run with follow-ups in its picture that is still queued when the new
version starts fails its claim ("changed after it was approved") and must be
requested again. Skew: an older manager computes the old picture; such a request
approved on one version and claimed on the other fails its claim, and an older
manager approving starts the follow-ups unseen as before. **Approve run requests
with follow-ups only once every manager runs this version.** Workers and agents are not affected. A backup carries the new
column with `org_assistant_settings`.

### Docs (noix-docs)

The assistant-access page exists only on branch `docs/pulsix-assistant-access`
(not yet merged into `pulsix`). Work happens on `docs/pulsix-approved-follow-ups`
cut from it: `user/assistant-access.md` gains the setting, what the approval page
shows in each mode, deselecting, and the limits above. **(Claude's call)** The
follow-up section of `user/playbooks-and-jobs.md` lives on `pulsix`; a one-line
pointer there ("an approved assistant run request starts follow-ups only when the
organization allows it") is added on a second small branch from `pulsix`,
`docs/pulsix-approved-follow-ups-note`. No tech stack named.

## Tests

- **runrequests unit (fakes):** picture without follow-ups has no `follow_ups` and
  the fingerprint equals a golden value from the current code, for a run request
  and for a retry whose original group has no rows; mode B lists ids
  and names only; mode A builds the tree with revisions, `in_chain`, `too_deep`,
  `Truncated` at 100, `deleted`; retry pictures come from the original group's
  rows; fingerprint changes when the setting, a follow-up revision, the order or
  the list changes. Approve: default selection per mode, subset accepted in order,
  foreign id / stopped id / non-empty in mode B / non-empty when truncated
  refused before `Decide`; omitted when truncated starts none; a re-approval
  after a failed enqueue clears the first attempt's unstarted rows (a deselected
  follow-up does not start, a swept `skipped` row does not block); the enqueuer receives the computed list (never nil);
  `CheckRunClaim` fails after a follow-up revision change in mode A and after a
  setting switch.
- **jobs:** `RetryFailedInto` with nil copies all, with a list only those, with an
  empty list none; `TestApprovedRunRequestPlansFollowUpsInItsGroup` updated to the
  new signature.
- **chain store integration (`testdb`):** the filtered copy query; the clear
  query deletes `pending` and `skipped` rows of the group and keeps `due` and
  `started`.
- **assistant store integration:** the new column, upsert keeps the other field,
  absent row reads false.
- **API (`apitest`):** settings GET/PUT round trip, omitted field keeps the value,
  token refused, operator refused; run request detail with follow-ups in both
  modes; approve in mode B starts no follow-up (the run detail lists none);
  approve in mode A starts them, with a deselection only the rest; 422 cases;
  changed-setting refusal (409) between detail and approve.
- **MCP:** `get_run_request` shows `follow_ups`.
- **SPA (Vitest):** follow-ups section in both modes, checkbox default and
  deselection sends `follow_ups`, plain approve sends no `follow_ups`, stop labels,
  truncated state; the settings switch; en and de.
- **Playwright:** the existing run-request journey extended, if its fixture has a
  template with a follow-up; otherwise Vitest coverage only. **(Claude's call)**

## Left to the builder

- Exact names of the new store methods and query.
- Whether the tree walk lives in `resolve.go` or a new `follow_ups.go` in
  `internal/runrequests` (the latter, if `resolve.go` would pass ~400 lines).
- Catalogue wording beyond the strings quoted here.
