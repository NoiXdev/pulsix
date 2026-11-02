/**
 * The schedule calendar (week, month and list views). Schedule and window
 * names are data and never keys. Occurrence states and run statuses the
 * API sends are mapped to a label.
 */
export const enCalendar = {
  label: 'Calendar',
  previous: 'Previous',
  next: 'Next',
  timesIn: (zone: string) => `Times in ${zone}`,
  weekTitle: (from: string, to: string) => `Week · ${from} – ${to}`,
  views: {
    week: 'week',
    month: 'month',
    list: 'list',
  },
  maintenance: 'maintenance',
  windowChip: (name: string) => `Maintenance · ${name}`,
  truncated: 'Some very frequent schedules or maintenance windows are shown in part.',
  more: (n: number) => `+${n} more`,
  empty: 'Nothing scheduled in this range.',
  windowUntil: (name: string, until: string) => `Maintenance · ${name} (until ${until})`,
  state: {
    planned: 'planned',
    triggered: 'triggered',
    skipped_overlap: 'skipped_overlap',
    skipped_maintenance: 'skipped_maintenance',
    missed: 'missed',
    failed: 'failed',
  },
  jobStatus: {
    queued: 'queued',
    running: 'running',
    succeeded: 'succeeded',
    failed: 'failed',
    cancelled: 'cancelled',
  },
};
