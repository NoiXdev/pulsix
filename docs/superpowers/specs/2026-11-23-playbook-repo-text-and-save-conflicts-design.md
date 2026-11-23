# Playbook page: repository text and save conflicts — design

Backlog 3.127, two follow-ups of the playbook pages (3.123). Owner's decision,
2026-11-23: the repository text is **stored at sync** (not fetched on demand).
Decisions marked *(controller's call)* were not discussed with the owner.

## Goals

- (a) A repository playbook's page shows its YAML as of the last sync in a
  read-only editor, with Check, Variables and the Outline, instead of today's note.
- (b) Saving an editor playbook whose stored text changed since the draft was
  loaded is refused (412) with a notice, instead of silently overwriting the
  other person's change.

## Non-goals

- Editing a repository playbook in Pulsix, or showing its imports, roles or
  other files: only the playbook file itself.
- A dry run of a repository playbook's draft (there is no draft).
- Merging two drafts. The conflict notice offers reload or overwrite, not a merge.
- Fetching the text on demand from a worker.

## Anti-goals

- An older manager storing the text where it should not (in `jobs.stats`) or
  refusing a sync because the report grew.
- A conflict check that a client can trip by accident: a client that sends no
  precondition saves as today.
- Text from one org readable from another, or by someone without `playbooks.read`.

## (a) Repository text

### Worker → manager

- The sync job tells the worker whether the manager wants the texts: a new
  `Job.want_playbook_texts` (proto, additive), set by every claim of a sync job
  from this version on. An older manager never sets it, so a new worker sends
  nothing new to it. An older worker ignores it; its syncs store no text.
- With the flag, after the variable-source read the worker reads each
  discovered playbook file of the report (the same paths, same subpath,
  same clone rules: no leaving the subpath, no `.git`, no links out) and ships
  `{path: text}` under a new result stats key `playbook_texts`, next to
  `var_sources`. Order: the report's path order; per file at most
  `varsource.MaxFileBytes` (512 KiB); all texts together at most **4 MiB**
  (the result must stay under `workerpb.MaxWorkerToManagerBytes`, 16 MiB, beside
  the 8 MiB report). A file over either limit, not valid UTF-8, or unreadable is
  left out and listed in `playbook_texts_left_out: {path: reason}` (reason
  `too_large`, `budget`, `not_utf8`, `unreadable`). *(controller's call: path
  order, no priority for playbooks registered in Pulsix)*
- The manager takes both keys out of the stats before storing them (as it does
  with `var_sources`) and stores each text with its path's row.

### Storage

- Migration (additive): `repo_playbook_sources.content text NULL` and
  `content_left_out text NULL` (the reason). Written in the same transaction
  that replaces the repo's rows today. A sync whose report is Failed keeps the
  previous rows, and with them the previous texts (as today's rule).
- A sync from an older worker stores rows with `content` NULL and no reason.
- The text is stored as is, like the inline text of an editor playbook; the
  backup includes it (backup round-trip fixture updated).

### API

- `GET /api/v1/orgs/{org}/playbooks/{id}/content`, permission `playbooks.read`,
  only for `source=repo` (an editor playbook → 422, its text is in the DTO):
  `{path, commit, synced_at, content?, left_out?: "too_large"|"budget"|"not_utf8"|"unreadable"|"not_sent"}`.
  `not_sent`: the last sync stored no text for it (older worker, or the path was
  not in the last report). No sync yet → 404 with a message. *(controller's call:
  its own route, so the playbook list does not carry texts)*
- The outline route already takes content: the page posts the stored text.

### SPA

- Editor tab of a repository playbook: a read-only code editor with the text, a
  line above it — "As of the sync on <time>, commit <short sha>" with a link to
  the repository —, Check and Variables beside it as for an editor playbook
  (Variables from the stored sources, as the template page reads them), the
  Outline from the text. No Save, no draft checkbox in the Dry run tab.
- Without text: the existing note, plus the reason (`too_large`, `budget`, …;
  `not_sent`: "sync the repository again with a current worker").

## (b) Save conflicts

- Migration (additive): `playbooks.revision bigint NOT NULL DEFAULT 1`; every
  update of an editor playbook's text increments it. The DTO carries `revision`;
  `GET` and `PUT` answer with `ETag` (the existing template pattern:
  `If-Match` optional, a stale one → 412 with the current revision).
- The SPA sends `If-Match` with the revision its saved base came from. On 412 it
  keeps the draft and shows a notice: "Someone saved this playbook since you
  started editing." with **Show their changes** (a diff of the base against the
  current text, the existing `UnifiedDiff`), **Reload** (drop my draft, take
  theirs) and **Save mine anyway** (save with the new revision, overwriting).
  *(controller's call: no merge, overwrite is an explicit choice)*
- `usePlaybookDraft` tracks the base revision with the base text; a clean draft
  still follows a newer stored text (and its revision) as today.
- Version skew: an older manager ignores `If-Match` and does not bump the
  revision; a newer one treats a missing header as today. Nothing else changes.
  Follow `docs/upgrades.md`.

## Tests (the builder adds the usual)

- Worker: texts sent only with the flag; per-file and total limits with the
  left-out reasons; a link leaving the subpath is `unreadable`; order is the
  report's.
- Manager: both keys removed from the stored stats; texts land with their rows;
  a Failed report keeps the previous texts; an older worker's sync stores NULL;
  cross-org read of `/content` 404; editor playbook → 422.
- Save: stale `If-Match` → 412 and the text unchanged; no header → saved;
  revision increments; the SPA notice and its three actions.

## Left to the builder

Component structure, where the read-only state lives in the editor component,
the exact wording, DTO names beyond those above.
