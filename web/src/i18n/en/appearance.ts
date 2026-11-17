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
    none: 'No list has its own columns yet.',
    reset: 'Reset',
    resetAria: (list: string) => `Reset ${list}`,
  },
  saveFailed: 'Your settings could not be saved',
};
