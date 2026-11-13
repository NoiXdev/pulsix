import type { Messages } from '../types.ts';

export const deSettings: Messages['settings'] = {
  nav: {
    label: 'Einstellungen',
    organization: 'Organisation',
    alerting: 'Alarmierung',
    instance: 'Instanz',
  },
  tabs: {
    users: 'Benutzer',
    organizations: 'Organisationen',
    general: 'Allgemein',
    members: 'Mitglieder',
    activity: 'Aktivität',
    tokens: 'API-Tokens',
    notifications: 'Benachrichtigungen',
    alertRoutes: 'Alarmrouten',
    rotations: 'Bereitschaftsrotationen',
    mail: 'E-Mail-Versand',
    sharedRepos: 'Gemeinsame Repositories',
    sharedTemplates: 'Gemeinsame Job-Vorlagen',
    sharedChecks: 'Gemeinsame Check-Definitionen',
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
  monitoring: {
    title: 'Überwachung',
    clientAbsentDays: 'Warnung nach N Tagen ohne Kontakt (0 = aus)',
    clientAbsentDaysHint:
      'Für Client-Hosts mit Agent: Hatte ein Client so viele Tage keinen Kontakt, öffnet sich eine Warnung. Sie schließt sich, wenn der Client zurück ist. 0 schaltet sie ab.',
    invalid: 'Gib eine ganze Zahl von Tagen zwischen 0 und 365 ein.',
    readOnlyHint: 'Nur Inhaber und Admins können die Überwachungseinstellungen ändern.',
    save: 'Speichern',
    saveLabel: 'Überwachungseinstellungen speichern',
    saved: 'Überwachungseinstellungen gespeichert',
    saveFailed: 'Die Überwachungseinstellungen konnten nicht gespeichert werden',
    loadFailed: 'Die Überwachungseinstellungen konnten nicht geladen werden',
  },
};
