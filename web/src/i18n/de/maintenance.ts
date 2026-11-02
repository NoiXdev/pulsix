import type { Messages } from '../types.ts';

export const deMaintenance: Messages['maintenance'] = {
  title: 'Wartung',
  active: (n: number) =>
    `Jetzt aktiv: ${n} · Wartung lässt Zeitpläne Hosts überspringen oder auslassen; Stummschaltung schaltet nur Alarme stumm`,
  newWindow: 'Neues Fenster',
  columns: {
    window: 'Fenster',
    why: 'Grund',
    appliesTo: 'Gilt für',
    when: 'Wann',
    status: 'Status',
  },
  reason: {
    maintenance: 'Wartung',
    silence: 'Stummschaltung',
  },
  activeNow: 'jetzt aktiv',
  edit: 'Bearbeiten',
  editAria: (name: string) => `${name} bearbeiten`,
  deleteAria: (name: string) => `${name} löschen`,
  deleteConfirm: {
    maintenance: (name: string) => `Wartungsfenster ${name} löschen?`,
    silence: (name: string) => `Stummschaltungsfenster ${name} löschen?`,
  },
  deleteFailed: 'Löschen fehlgeschlagen',
  empty: 'Keine Wartungs- oder Stummschaltungsfenster.',
  when: {
    recurring: (what: string, duration: string, timezone: string) => `${what} · für ${duration} · ${timezone}`,
  },
  target: {
    org: 'Gesamte Organisation',
    host: 'Host',
    group: 'Gruppe',
  },
  dialog: {
    editTitle: (name: string) => `${name} bearbeiten`,
    newTitle: 'Neues Fenster',
    name: 'Name',
    why: 'Grund',
    reasons: {
      maintenance: 'Wartung – es wird gearbeitet; geplante Läufe können diese Hosts überspringen',
      silence: 'Stummschaltung – keine Alarme auslösen; geplante Läufe bleiben unberührt',
    },
    appliesTo: 'Gilt für',
    targetKinds: {
      org: 'Die ganze Organisation',
      host: 'Ein Host',
      inventory_group: 'Eine Inventargruppe (und ihre Untergruppen)',
    },
    findHost: 'Host suchen',
    findHostPlaceholder: 'Hosts nach Name filtern',
    host: 'Host',
    chooseHost: 'Host auswählen…',
    group: 'Gruppe',
    chooseGroup: 'Gruppe auswählen…',
    when: 'Wann',
    recurring: 'Wiederkehrend',
    once: 'Einmalig',
    cron: 'Beginn (Cron)',
    lasts: 'Dauer',
    timezone: 'Zeitzone',
    from: 'Von',
    until: 'Bis',
    note: 'Notiz',
    saveFailed: 'Speichern fehlgeschlagen',
  },
};
