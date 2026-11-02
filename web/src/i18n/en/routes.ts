/** What the route-connected layouts (src/routes) say around their pages. */
export const enRoutes = {
  noOrganization: 'This account does not belong to any organization yet.',
  noOrganizationInvite: 'This account does not belong to any organization yet. Ask an administrator to invite you.',
  accountSecurity: {
    crumbAccount: 'Account',
    crumbSecurity: 'Security',
    secondFactorActive: 'Second factor active',
    secondFactorRequired: 'Second factor required',
  },
  enrolment: {
    signOut: 'Sign out',
    title: 'Set up a second factor',
    signedInAs: (email: string) => `Signed in as ${email}.`,
    intro:
      'Every account needs an authenticator app or a passkey before it can work inside an organization — add one now to continue.',
  },
};
