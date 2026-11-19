# Group membership from the host page, and a real code editor — design

Owner's requests, 2026-11-19: "kann ich unter host im tab inventory auch gruppen
zuweisen?" (answered: not yet; proposal accepted with "ebenfalls …") and "unter
Playbooks sollte der Editor auch ein echter Code-Editor sein". Backlog 3.119
(host groups) and 3.120 (code editor). SPA only; no API change expected.

## 1. Host page, Inventory tab: manage the host's groups (3.119)

Today `HostInventoryCard` only lists the host's direct groups with a link each;
assigning happens on the group page ("Assign host", `AssignHostDialog`).

- **Add to group …** (button on the card, only with the role that may assign on
  the group page): opens the assignment dialog with the **host fixed** and a
  **group picker** instead of the host picker (groups the host is already a
  direct member of are not offered; `all` is not offered). After choosing a
  group, the dialog shows that group's host-scoped fields and its own values
  read-only, exactly as `AssignHostDialog` does today. Reuse that dialog (make
  host or group the fixed side), don't fork it.
- **Per direct group row** (same role): **Edit values …** (the same dialog in
  member mode) and **Remove from group …** (confirm, the existing unassign
  hook; the confirm names host and group). Rows keep the link to the group.
- **Inherited groups** (the host is in them only through a subgroup) are listed
  under the direct ones, muted, with "via <subgroup>", no actions — only if the
  data the SPA already has gives them cheaply (the group tree from the groups
  list); otherwise leave them out and say so in the report.
- Readers without the role see the list as today. Unmanaged hosts keep the
  management-off hint.
- Problems on a membership (missing required per-host values) show on the row,
  as the group page's member list shows them.

## 2. A real code editor (3.120)

- **Editor:** CodeMirror 6 in a presentational `web/src/ui/code-editor.tsx`
  (`CodeEditor({ value, onChange, language: 'yaml' | 'json' | 'text', label | labelledBy,
  readOnly?, minHeight?, diagnostics?: { line?: number; message: string }[] })`).
  Not Monaco (size, workers, CSP). Pin exact versions (no ranges), each released
  at least two weeks ago; add only the packages needed (`@codemirror/state`,
  `view`, `commands`, `language`, `lang-yaml`, `lang-json`, `search`, `lint`,
  `autocomplete` only if used). Load it lazily (dynamic import / `React.lazy`)
  so the main bundle does not grow; the fallback is the plain textarea.
- **Behaviour:** line numbers, syntax highlighting with the app's colour tokens
  in light and dark (CSS variables, no fixed palette), active-line highlight,
  bracket matching, code folding, search (Mod-F), undo/redo, Tab and Shift-Tab
  indent by two spaces (no tab characters in YAML), auto-indent on Enter,
  soft-wrap off with horizontal scroll, JetBrains Mono. Escape leaves the
  editor's keyboard trap (Tab must not trap keyboard users: follow CodeMirror's
  recommended Escape-then-Tab behaviour and say so in a hint under the editor).
- **Playbook editor:** the dialog uses `CodeEditor` (YAML) with a taller area
  (about 60 % of the viewport) and a **full-screen** toggle; the existing
  client-side validation (`validate.ts`) shows its message as today and, where
  the validator can tell the line cheaply, as a lint marker on that line (extend
  `validateYAML` to return an optional line only if simple; otherwise message
  only). Ctrl/Cmd-S saves when valid. Closing with unsaved changes asks first.
- **Other code fields:** switch the fields that hold code or structured text to
  `CodeEditor` where it clearly helps and the change is small: the ad-hoc
  command dialog's script/command if multi-line, JSON variable inputs
  (`variables/inputs.tsx` JSON/object type), check params given as JSON
  (`checks/ParamFields.tsx`) — list what was switched and what was left in the
  report. Plain-text notes (acknowledge, maintenance reason, …) stay textareas.
- **Tests:** jsdom cannot lay out CodeMirror fully; test the wrapper's contract
  (value in, onChange out, readOnly, label, diagnostics rendered) with
  CodeMirror's own DOM where it works, and keep the existing playbook editor
  tests green (adapt selectors to the editor's textbox role). e2e specs that type
  into the playbook textarea must be adapted.
