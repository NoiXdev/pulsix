/** The personal Appearance page: density, page size, language, saved columns. */
export const enAppearance = {
  title: 'Appearance',
  subtitle: 'Applies to your account, in every browser.',
  density: {
    title: 'Density',
    comfortable: 'Comfortable',
    standard: 'Standard',
    compact: 'Compact',
    comfortableHint: 'Address under the name, plenty of air',
    standardHint: 'As it has been',
    compactHint: 'Tight rows, smaller type',
  },
  pageSize: {
    title: 'Page size',
    label: 'Rows per page',
    hint: 'How many rows a list shows per page, in every list.',
  },
  columns: {
    title: 'Saved column choices',
    hint: 'Lists remember the columns you show. Reset a list to bring back its default columns.',
    state: (n: number, total: number) => `${n} of ${total} columns`,
    own: (n: number) => `${n} columns`,
    standard: 'Standard',
    reset: 'Reset',
    resetAria: (list: string) => `Reset ${list}`,
  },
  loadFailed: 'Your saved settings could not be loaded, so they cannot be changed right now. Reload the page to try again.',
  saveFailed: 'Your settings could not be saved',
};
