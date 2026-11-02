/**
 * The first-run setup wizard. Its language field is `language.label` /
 * `language.wizard.hint`; the SMTP step reuses `instance.smtp` for its
 * field labels, and the sign-in words come from `auth`.
 */
export const enSetup = {
  title: 'Set up this installation',
  intro: 'Runs once. Afterwards this page is gone and everything happens inside the organization.',
  stepOf: (n: number, total: number) => `Step ${n} of ${total}`,
  steps: {
    admin: { label: 'Instance administrator', sub: 'email and password' },
    org: { label: 'First organization', sub: 'name and URL slug' },
    smtp: { label: 'Outgoing mail', sub: 'optional, can be set later' },
    mfa: { label: 'Second factor', sub: 'authenticator app or passkey' },
  },
  admin: {
    title: 'Create the instance administrator',
    name: 'Name',
    repeatPassword: 'Repeat password',
  },
  org: {
    title: 'Create the first organization',
    name: 'Organization name',
    slug: 'URL slug',
  },
  smtp: {
    intro: 'Optional, can be set later. Without SMTP configured, invitation e-mails cannot be sent.',
    configureNow: 'Configure SMTP now',
  },
  mfa: {
    title: 'Enrol a second factor now',
    intro:
      'The instance administrator account is created next. You will be asked to add an authenticator app or a passkey right away — every account needs a second factor before it can do anything else.',
    creating: 'Creating…',
    create: 'Create administrator & continue',
  },
  failed: 'failed to complete setup',
};
