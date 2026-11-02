/**
 * The Maintenance page and its window dialog. Window names, notes, host
 * and group names, cron expressions, durations and time zones are data and
 * never keys.
 */
export const enMaintenance = {
  title: 'Maintenance',
  active: (n: number) =>
    `${n} active now · maintenance lets schedules skip or leave out hosts; silence only mutes alerts`,
  newWindow: 'New window',
  columns: {
    window: 'Window',
    why: 'Why',
    appliesTo: 'Applies to',
    when: 'When',
    status: 'Status',
  },
  reason: {
    maintenance: 'maintenance',
    silence: 'silence',
  },
  activeNow: 'active now',
  edit: 'Edit',
  editAria: (name: string) => `Edit ${name}`,
  deleteAria: (name: string) => `Delete ${name}`,
  deleteConfirm: {
    maintenance: (name: string) => `Delete maintenance window ${name}?`,
    silence: (name: string) => `Delete silence window ${name}?`,
  },
  deleteFailed: 'Deleting failed',
  empty: 'No maintenance or silence windows.',
  when: {
    recurring: (what: string, duration: string, timezone: string) => `${what} · for ${duration} · ${timezone}`,
  },
  target: {
    org: 'Whole organization',
    host: 'Host',
    group: 'Group',
  },
  dialog: {
    editTitle: (name: string) => `Edit ${name}`,
    newTitle: 'New window',
    name: 'Name',
    why: 'Why',
    reasons: {
      maintenance: 'Maintenance — work is happening; scheduled runs can skip these hosts',
      silence: 'Silence — do not raise alerts; scheduled runs are unaffected',
    },
    appliesTo: 'Applies to',
    targetKinds: {
      org: 'The whole organization',
      host: 'One host',
      inventory_group: 'An inventory group (and its subgroups)',
    },
    findHost: 'Find host',
    findHostPlaceholder: 'Filter hosts by name',
    host: 'Host',
    chooseHost: 'Choose a host…',
    group: 'Group',
    chooseGroup: 'Choose a group…',
    when: 'When',
    recurring: 'Recurring',
    once: 'Once',
    cron: 'Starts (cron)',
    lasts: 'Lasts',
    timezone: 'Timezone',
    from: 'From',
    until: 'Until',
    note: 'Note',
    saveFailed: 'Saving failed',
  },
};
