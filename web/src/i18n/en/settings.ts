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
    saveFailed: 'failed to save the organization',
  },
};
