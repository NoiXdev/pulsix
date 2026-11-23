const variables = (n: number) => (n === 1 ? '1 variable' : `${n} variables`);

/** The Editor tab of a repository playbook: its text as of the last sync
 * (backlog 3.127). */
export const enPlaybookRepoText = {
  asOf: (when: string, commit: string) => `As of the sync on ${when}, commit ${commit}`,
  earlier: (when: string) => `From an earlier sync: the last one, on ${when}, left this playbook unread`,
  readOnly: 'Read-only: changes come through Git, and the next sync brings them in.',
  loading: 'Reading the text of the last sync…',
  ready: 'Parses as a list of plays.',
  checkHint: 'Checked in the browser, on the text of the last sync.',
  notSynced: 'The repository has not been synced yet: sync it to see the text here.',
  readFailed: (message: string) => `The text could not be read: ${message}`,
  leftOut: {
    too_large: 'The last sync stored no text for this playbook: the file is over 512 KiB.',
    budget: "The last sync stored no text for this playbook: the sync's texts were full (4 MiB in all).",
    not_utf8: 'The last sync stored no text for this playbook: the file is not UTF-8 text.',
    unreadable: 'The last sync stored no text for this playbook: the file could not be read.',
    not_sent:
      'The last sync stored no text for this playbook: the file may be gone from the repository, or the sync ran on an older worker. Sync again once workers are updated.',
  },
  sources: {
    none: 'The last sync recorded no variable sources for this playbook.',
    failed: 'The last sync could not read its variable sources.',
    leftOut: (n: number) => `A limit left its variable sources unread at the last sync; the read before found ${variables(n)}.`,
    read: (n: number) => `Its variable sources at the last sync: ${variables(n)}, which a template using it offers as fields.`,
  },
};

/** The notice after a save the server refused because someone saved the
 * playbook meanwhile (412, backlog 3.127). */
export const enPlaybookConflict = {
  title: 'Someone saved this playbook since you started editing.',
  body: 'Your changes are still here, not saved. Reload drops them and takes the saved text; Save mine anyway replaces it with yours.',
  show: 'Show their changes',
  hide: 'Hide their changes',
  before: 'when you started editing',
  after: 'saved since',
  reload: 'Reload',
  saveAnyway: 'Save mine anyway',
  savedAgain: 'They saved again in the meantime: check their changes once more.',
};
