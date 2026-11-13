/** The Settings section: the side navigation (SettingsLayout) and the
 * General page (OrgGeneralPage). `tabs` holds the page names, used as nav
 * labels and page titles. The Language and Backups labels are
 * `language.instance.tab` and `backups.tab`. */
export const enSettings = {
  nav: {
    label: 'Settings',
    organization: 'Organization',
    alerting: 'Alerting',
    instance: 'Instance',
  },
  tabs: {
    users: 'Users',
    organizations: 'Organizations',
    general: 'General',
    members: 'Members',
    activity: 'Activity',
    tokens: 'API tokens',
    notifications: 'Notifications',
    alertRoutes: 'Alert routes',
    rotations: 'On-call rotations',
    mail: 'Outgoing mail',
    sharedRepos: 'Shared repositories',
    sharedTemplates: 'Shared job templates',
    sharedChecks: 'Shared check definitions',
  },
  general: {
    title: 'General',
    hint: 'The name and address of this organization.',
    name: 'Organization name',
    slug: 'Slug',
    slugHint: 'The slug is part of every URL of this organization and cannot be changed.',
    readOnlyHint: 'Only owners and admins can rename the organization.',
    save: 'Save',
    saved: 'Organization saved',
    saveFailed: 'Failed to save the organization',
  },
  // Backlog 3.57: the organization's monitoring settings, on the General page.
  monitoring: {
    title: 'Monitoring',
    clientAbsentDays: 'Warn after N days without contact (0 = off)',
    clientAbsentDaysHint:
      'For client hosts with an agent: when a client has not been in contact for this many days, a warning opens. It closes when the client is back. 0 switches it off.',
    invalid: 'Enter a whole number of days from 0 to 365.',
    retention: 'Keep measurements for',
    retentionDefault: (days: number) => `Instance default (${days} days)`,
    retentionDays: (days: number) => `${days} days`,
    retentionAboveMax: (days: number) => `The instance keeps at most ${days} days`,
    retentionHint: 'How long measurements and state history are kept for this organization. The instance maximum, set by the operator, caps the choice.',
    retentionLowerWarning: (days: number) => `Measurements and state history older than ${days} days will be deleted within the hour.`,
    retentionLowerTitle: 'Delete older measurements?',
    readOnlyHint: 'Only owners and admins can change the monitoring settings.',
    save: 'Save',
    saveLabel: 'Save monitoring settings',
    saved: 'Monitoring settings saved',
    saveFailed: 'Failed to save the monitoring settings',
    loadFailed: 'Failed to load the monitoring settings',
  },
};
