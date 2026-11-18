/** The personal Appearance page: density, page size, language, saved columns. */
export const enAppearance = {
  title: 'Appearance',
  subtitle: 'Applies to your account, in every browser.',
  density: {
    title: 'Density',
    description: 'How tightly lists are laid out.',
    comfortable: 'Comfortable',
    standard: 'Standard',
    compact: 'Compact',
    comfortableHint: 'Address under the name, plenty of air',
    standardHint: 'As it has been',
    compactHint: 'Tight rows, smaller type',
  },
  lists: 'Lists',
  listsDescription: 'How many rows a list shows per page.',
  pageSize: {
    title: 'Page size',
    label: 'Rows per page',
  },
  columns: {
    title: 'Saved column choices',
    hint: 'Lists remember the columns you show. Reset a list to bring back its default columns.',
    state: (n: number, total: number) => `${n} of ${total} columns`,
    own: (n: number) => (n === 1 ? '1 column' : `${n} columns`),
    standard: 'Standard',
    reset: 'Reset',
    resetAria: (list: string) => `Reset ${list}`,
  },
  loadFailed: 'Your saved settings could not be loaded, so they cannot be changed right now. Reload the page to try again.',
  saveFailed: 'Your settings could not be saved',
};
