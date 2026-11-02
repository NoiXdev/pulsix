import type { Messages } from '../types.ts';

export const deCalendar: Messages['calendar'] = {
  label: 'Kalender',
  previous: 'Zurück',
  next: 'Weiter',
  timesIn: (zone: string) => `Zeiten in ${zone}`,
  weekTitle: (from: string, to: string) => `Woche · ${from} – ${to}`,
  views: {
    week: 'Woche',
    month: 'Monat',
    list: 'Liste',
  },
  maintenance: 'Wartung',
  windowChip: (name: string) => `Wartung · ${name}`,
  truncated: 'Einige sehr häufige Zeitpläne oder Wartungsfenster werden nur teilweise angezeigt.',
  more: (n: number) => `+${n} weitere`,
  empty: 'In diesem Zeitraum ist nichts geplant.',
  windowUntil: (name: string, until: string) => `Wartung · ${name} (bis ${until})`,
  state: {
    planned: 'geplant',
    triggered: 'ausgelöst',
    skipped_overlap: 'übersprungen (Überlappung)',
    skipped_maintenance: 'übersprungen (Wartung)',
    missed: 'verpasst',
    failed: 'fehlgeschlagen',
  },
  jobStatus: {
    queued: 'wartend',
    running: 'läuft',
    succeeded: 'erfolgreich',
    failed: 'fehlgeschlagen',
    cancelled: 'abgebrochen',
  },
};
