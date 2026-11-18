import type { Messages } from '../types.ts';

export const deLanguage: Messages['language'] = {
  label: 'Sprache',
  instanceDefault: (language: string) => `Standard der Instanz (${language})`,
  instanceDefaultUnknown: 'Standard der Instanz',
  account: {
    row: 'Sprache der Oberfläche',
    description: 'Die Sprache dieser Oberfläche für dein Konto.',
    saveFailed: 'Sprache konnte nicht gespeichert werden',
  },
  wizard: {
    hint: 'Die Vorgabe für alle auf dieser Installation. Jede Person kann später eine eigene wählen.',
  },
  instance: {
    tab: 'Sprache',
    title: 'Standardsprache',
    hint: 'Für die ganze Instanz: gilt auf den Anmeldeseiten und für alle, die keine eigene Sprache gewählt haben.',
    description: 'Die Sprache, die alle sehen, bis sie eine eigene wählen.',
    field: 'Standardsprache',
    save: 'Standardsprache speichern',
    saved: 'Standardsprache gespeichert',
    saveFailed: 'Standardsprache konnte nicht gespeichert werden',
  },
};
