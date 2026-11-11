import type { Messages } from '../types.ts';

export const deCommon: Messages['common'] = {
  save: 'Speichern',
  copied: 'Kopiert',
  copyUnavailable: 'Kopieren ist hier nicht möglich. Den Text markieren und Strg+C (auf dem Mac Cmd+C) drücken.',
  cancel: 'Abbrechen',
  rowActions: (name: string) => `Aktionen für ${name}`,
  sections: 'Abschnitte',
  open: 'Öffnen',
  close: 'Schließen',
  back: 'Zurück',
  continue: 'Weiter',
  remove: 'Entfernen',
  delete: 'Löschen',
  loading: 'Wird geladen…',
  never: 'nie',
};
