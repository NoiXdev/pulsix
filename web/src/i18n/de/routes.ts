import type { Messages } from '../types.ts';

export const deRoutes: Messages['routes'] = {
  noOrganizationInvite:
    'Dieses Konto gehört noch keiner Organisation an. Für eine Einladung bitte an die Administration der Organisation wenden.',
  accountSecurity: {
    secondFactorActive: 'Zweiter Faktor aktiv',
    secondFactorRequired: 'Zweiter Faktor erforderlich',
    secondFactorOptional: 'Zweiter Faktor optional',
  },
  enrolment: {
    signOut: 'Abmelden',
    title: 'Zweiten Faktor einrichten',
    signedInAs: (email: string) => `Angemeldet als ${email}.`,
    intro:
      'Jedes Konto braucht eine Authenticator-App oder einen Passkey, bevor es in einer Organisation arbeiten kann. Zum Fortfahren jetzt einen zweiten Faktor einrichten.',
  },
};
