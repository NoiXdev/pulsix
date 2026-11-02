import type { Messages } from '../types.ts';

export const deLanguage: Messages['language'] = {
  label: 'Sprache',
  instanceDefault: (language: string) => `Standard der Instanz (${language})`,
  instanceDefaultUnknown: 'Standard der Instanz',
  account: {
    hint: 'Die Sprache dieser Oberfläche für das eigene Konto, in jedem Browser und auf jedem Gerät.',
    saveFailed: 'Sprache konnte nicht gespeichert werden',
  },
  wizard: {
    hint: 'Die Vorgabe für alle auf dieser Installation. Jede Person kann später eine eigene wählen.',
  },
  instance: {
    tab: 'Sprache',
    title: 'Standardsprache',
    hint: 'Für die ganze Instanz: gilt auf den Anmeldeseiten und für alle, die keine eigene Sprache gewählt haben.',
    field: 'Standardsprache',
    save: 'Standardsprache speichern',
    saved: 'Standardsprache gespeichert',
    saveFailed: 'Standardsprache konnte nicht gespeichert werden',
  },
};
