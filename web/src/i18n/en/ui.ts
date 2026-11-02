/**
 * The shared components in src/ui that say something themselves: the
 * combobox, the full-page error and loading states, the query guard's
 * toasts, the "Load more" button, the slug field and the metric chart.
 */
export const enUi = {
  loadMore: 'Load more',
  select: {
    open: 'Open',
    noOptions: 'No options',
    noMatch: 'No match',
    unavailable: (value: string) => `${value} (unavailable)`,
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
  slug: {
    generateLabel: 'Generate slug from the name',
    generateTitle: 'Generate from the name',
  },
  chart: {
    ranges: {
      '1h': 'the last hour',
      '24h': 'the last 24 hours',
      '7d': 'the last 7 days',
    },
    label: (what: string, range: string, samples: number) => `${what} over ${range}, ${samples} samples`,
    empty: 'No samples in this range.',
  },
};
