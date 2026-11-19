# Playbook pages, clickable rows everywhere, resending invitations — design

Owner's requests, 2026-11-19: playbooks move from dialogs to their own create and
edit pages with helpful extras ("validate, dry run, latest runs"); the whole row of
a list opens its item ("gerne für alles"); and on pending invitations, "resend
invitation". Mockup https://claude.ai/artifact/T9Mr66pttSSspSkqgyF2Bv (owner asked
for it; the plan below follows the controller's proposed order: pages first, dry
run later). Backlog 3.123 (playbook pages), 3.124 (clickable rows), 3.125 (resend
invitation), 3.126 (dry run and outline — planned, not in this branch).

## 1. Playbook pages (3.123)

- Routes: `/o/$org/playbooks/new` and `/o/$org/playbooks/$id` with `?tab=` —
  `editor` (default), `runs`, `used`. The list's rows open the page; "New playbook"
  opens the new page. The dialog (`PlaybookEditor`) goes away; the shared
  `CodeEditor` is reused.
- **Header:** name (mono), status pills — *Valid* / the validation problem, *Unsaved*
  while dirty, the source (*Editor* or *<repo> @ <branch>*, shared marker as in the
  list); summary line ("<summary> · updated <when>"); actions: **Run…** (opens the
  existing run flow with this playbook's templates; if none, links to creating a
  template), **Save** (disabled until dirty and valid; Mod-S), **⋯** (Delete …, with
  today's rules: refused while templates use it — show why).
- **Editor tab:** the code editor at page height (no full-screen toggle needed, keep
  Mod-F search, Esc-then-Tab hint), and a side column: **Check** (the client
  preview's message and line as today; the server's 422 message after a failed save
  shown there too), **Variables** (Jinja names used in the YAML — `{{ name }}`,
  top-level identifier only — each marked "set in vars" when the playbook's own
  `vars:` defines it, else "needs a value from the template or inventory"; a pure
  helper with tests; clearly labelled as a hint), **Latest runs** (3, link to the
  Runs tab). Repository playbooks: the editor is read-only with a note naming the
  repository and branch and that changes come through Git; no Save.
- **Runs tab:** the runs list filtered to this playbook (new server filter, below),
  with the shared list toolbar (status filter, search) and paging as the runs page.
- **Used by tab:** the templates using the playbook (link each; "Create template…"
  opens the template creation with this playbook preselected if the template form
  supports a preset, else just the templates page), and their schedules and webhook
  triggers (read through the existing per-template endpoints).
- **New page:** a source choice — *Write in the editor* (name + editor, starter
  snippets: empty play, install packages, file from template, restart service) or
  *Choose from a repository* (today's repository picker flow moved onto the page).
  After creating, navigate to the playbook's page.
- Leaving a page with unsaved changes asks (router blocker, the way the group page
  does since 3.115); tab switches keep the draft.

## 2. Server: runs by playbook

`GET /orgs/{org}/runs` gains `playbook_id` (runs whose template uses that playbook,
plus ad-hoc runs never). Validation like `template_id`; RLS unchanged; test that
another org's playbook id returns nothing. Additive.

## 3. Clickable rows everywhere (3.124)

`DataList` rows already support a stretched row link (`rowLink`, 16 lists). Every
list whose row has a detail page gets it. A list whose row has no page but an edit
dialog (tokens, channels, routes, …) gets a row activation that opens that dialog
(new optional `onRowActivate` on `DataList`, keyboard reachable: the name cell
becomes a button with the same stretched-overlay pattern; Enter/Space activate).
Lists with neither stay as they are; the report lists every list and what it got.
Cell links and buttons keep working on top of the overlay; right-click menus as
today.

## 4. Resend an invitation (3.125)

- Server: `POST /orgs/{org}/invites/{inviteId}/resend` — same permission as creating
  invites; issues a fresh token (the old link stops working), resets the expiry to
  the normal validity, sends the invitation mail again through the existing mail
  path, audited like create. Works for pending and expired invitations; 404 for an
  accepted or revoked one. Rate-limit like invite creation if a limiter exists.
- SPA: pending invitations get **Resend invitation** in the row menu (and as a
  button on expired rows), with a toast; the expiry column updates.

## 5. Later (3.126, planned)

Dry run (`ansible-playbook --check --diff` through a worker, with tag selection and
limit) and the server-side outline (plays, tasks, tags). Recorded as a backlog row
with the mockup link; not built here.
