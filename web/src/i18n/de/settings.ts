import type { Messages } from '../types.ts';

export const deSettings: Messages['settings'] = {
  nav: {
    label: 'Einstellungen',
    organization: 'Organisation',
    alerting: 'Alarmierung',
    instance: 'Instanz',
  },
  tabs: {
    general: 'Allgemein',
    members: 'Mitglieder',
    activity: 'Aktivität',
    tokens: 'API-Tokens',
    notifications: 'Benachrichtigungen',
    alertRoutes: 'Alarmrouten',
    rotations: 'Bereitschaftsrotationen',
    mail: 'E-Mail-Versand',
    sharedResources: 'Geteilte Ressourcen',
  },
  general: {
    title: 'Allgemein',
    hint: 'Name und Adresse dieser Organisation.',
    name: 'Name der Organisation',
    slug: 'Slug',
    slugHint: 'Der Slug ist Teil jeder URL dieser Organisation und lässt sich nicht ändern.',
    readOnlyHint: 'Nur Inhaber und Admins können die Organisation umbenennen.',
    save: 'Speichern',
    saved: 'Organisation gespeichert',
    saveFailed: 'Die Organisation konnte nicht gespeichert werden',
  },
};
