import type { Messages } from '../types.ts';

export const deSession: Messages['session'] = {
  title: 'Deine Sitzung läuft gleich ab',
  intro: 'Bestätige, dass du es bist, um angemeldet zu bleiben. Was du geöffnet hast, bleibt unverändert.',
  remaining: 'Verbleibende Zeit',
  passwordLabel: 'Passwort',
  codeLabel: 'Authenticator- oder Wiederherstellungscode',
  renew: 'Sitzung verlängern',
  renewing: 'Verlängere …',
  wrongCredentials: 'Das Passwort oder der Code ist falsch.',
  passkeyFailed: 'Der Passkey konnte nicht überprüft werden. Versuche es erneut.',
  failed: 'Die Sitzung konnte nicht verlängert werden. Versuche es erneut.',
  tooManyAttempts: 'Zu viele Versuche. Warte einen Moment und versuche es erneut.',
  expired: 'Deine Sitzung ist beendet. Melde dich neu an.',
};
