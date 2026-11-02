import type { Messages } from '../types.ts';

export const deTokens: Messages['tokens'] = {
  title: 'API-Tokens',
  create: 'Token erstellen',
  count: (n: number) => (n === 1 ? '1 Token' : `${n} Tokens`),
  columns: {
    name: 'Name',
    role: 'Rolle',
    lastUsed: 'Zuletzt verwendet',
    expires: 'Läuft ab',
  },
  revoke: 'widerrufen',
  revokeConfirm: (name: string) => `Token ${name} widerrufen?`,
  revoked: (name: string) => `Token ${name} widerrufen`,
  revokeFailed: 'Token konnte nicht widerrufen werden',
  createFailed: 'Token konnte nicht erstellt werden',
  empty: 'Noch keine Tokens.',
  shownOnce: 'Dieses Token jetzt kopieren – es wird genau einmal angezeigt und kann nicht erneut abgerufen werden.',
  copy: 'Kopieren',
  done: 'Fertig',
  nameLabel: 'Name',
  roleLabel: 'Rolle',
  roleHint: 'Auf die eigene Rolle begrenzt – der Server erstellt nie ein Token mit mehr Rechten, als die eigene Rolle hat.',
};
