/**
 * The shared components in src/ui that say something themselves: the
 * combobox, the full-page error and loading states, the query guard's
 * toasts, the "Load more" button, the slug field, the metric chart, the
 * date and time pickers and the list component (DataList).
 */
export const enUi = {
  loadMore: 'Load more',
  select: {
    open: 'Open',
    noOptions: 'No options',
    noMatch: 'No match',
    unavailable: (value: string) => `${value} (unavailable)`,
  },
  multiSelect: {
    remove: (label: string) => `Remove ${label}`,
  },
  picker: {
    calendar: 'Choose a date',
    today: 'Today',
    clear: 'Clear',
    time: 'Time',
    timeOf: (name: string) => `${name}, time`,
    timePlaceholder: 'hh:mm',
    invalidTime: 'Enter a time from 00:00 to 23:59, such as 09:30.',
    incomplete: 'Enter both a date and a time, or leave both empty.',
    required: 'Choose a date.',
    // Read out with a required date field: a button cannot be aria-required.
    requiredHint: 'Required',
    // The calendar's own names, handed to the calendar library so that
    // nothing a screen reader hears in it is the library's English.
    monthNav: 'Change the month',
    previousMonth: 'Previous month',
    nextMonth: 'Next month',
    dayToday: (day: string) => `Today, ${day}`,
    daySelected: (day: string) => `${day}, selected`,
  },
  query: {
    signingOutTitle: 'Signing you out',
    signingOutDetail: 'Your session has expired. Redirecting to sign in…',
    forbiddenTitle: 'You do not have access',
    forbiddenDetail: 'Ask an organization owner or admin to grant you access to this page.',
    notFoundTitle: 'Not found',
    notFoundDetail: 'This page does not exist, or you do not have access to it.',
    failedTitle: 'Something went wrong',
    failedDetail: 'An unexpected error occurred. Please try again.',
  },
  guard: {
    loadMoreFailed: 'Could not load more — showing what is already loaded.',
    refreshFailed: 'Could not refresh this page — showing the last loaded data.',
  },
  confirm: {
    title: 'Are you sure?',
    cancel: 'Cancel',
    confirm: 'Confirm',
  },
  settings: {
    unsaved: 'Unsaved changes',
    discard: 'Discard',
    save: 'Save',
  },
  formPage: {
    actions: 'Form actions',
    unsaved: 'Leave this page? Your unsaved changes will be lost.',
  },
  slug: {
    generateLabel: 'Generate slug from the name',
    generateTitle: 'Generate from the name',
  },
  dataList: {
    columns: 'Columns',
    columnsHint: 'Saved for you in every browser. Drag a column or use the arrows to change the order.',
    alwaysShown: 'always',
    moveUp: (column: string) => `Move ${column} up`,
    moveDown: (column: string) => `Move ${column} down`,
    restoreDefaults: 'Restore defaults',
    actions: 'Actions',
    actionsFor: (row: string) => `Actions for ${row}`,
    openInNewTab: 'Open in new tab',
    copyLink: 'Copy link',
    linkCopied: 'Link copied',
    range: (from: number, to: number, count: string) => `${from}–${to} of ${count}`,
    perPage: 'Rows per page',
    pages: 'Pages',
    page: (n: number) => `Page ${n}`,
    previousPage: 'Previous page',
    nextPage: 'Next page',
  },
  listToolbar: {
    search: 'Search',
    clearSearch: 'Clear the search',
    addFilter: '+ Filter',
    moreFilters: 'More filters',
    removeFilter: (label: string) => `Remove filter ${label}`,
    reset: 'Reset',
    resetLabel: 'Reset every filter',
    countOf: (shown: number, all: string) => `${shown} of ${all}`,
  },
  chart: {
    ranges: {
      '1h': 'over the last hour',
      '24h': 'over the last 24 hours',
      '7d': 'over the last 7 days',
      '30d': 'over the last 30 days',
      '90d': 'over the last 90 days',
    },
    label: (what: string, range: string, samples: number) => `${what} ${range}, ${samples} samples`,
    empty: 'No samples in this range.',
  },
};
