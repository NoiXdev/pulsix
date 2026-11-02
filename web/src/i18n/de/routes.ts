import type { Messages } from '../types.ts';

export const deRoutes: Messages['routes'] = {
  noOrganization: 'Dieses Konto gehört noch keiner Organisation an.',
  noOrganizationInvite:
    'Dieses Konto gehört noch keiner Organisation an. Ein Admin kann eine Einladung ausstellen.',
  accountSecurity: {
    crumbAccount: 'Konto',
    crumbSecurity: 'Sicherheit',
    secondFactorActive: 'Zweiter Faktor aktiv',
    secondFactorRequired: 'Zweiter Faktor erforderlich',
  },
  enrolment: {
    signOut: 'Abmelden',
    title: 'Zweiten Faktor einrichten',
    signedInAs: (email: string) => `Angemeldet als ${email}.`,
    intro:
      'Jedes Konto braucht eine Authenticator-App oder einen Passkey, bevor es in einer Organisation arbeiten kann – jetzt einen hinzufügen, um fortzufahren.',
  },
};
