import type { Messages } from '../types.ts';

export const deAppearance: Messages['appearance'] = {
  title: 'Darstellung',
  subtitle: 'Gilt für dein Konto, in jedem Browser.',
  density: {
    title: 'Dichte',
    comfortable: 'Komfortabel',
    standard: 'Standard',
    compact: 'Kompakt',
    comfortableHint: 'Adresse unter dem Namen, viel Luft',
    standardHint: 'Wie bisher',
    compactHint: 'Enge Zeilen, kleinere Schrift',
  },
  pageSize: {
    title: 'Seitengröße',
    label: 'Zeilen pro Seite',
    hint: 'Wie viele Zeilen eine Liste pro Seite zeigt, in jeder Liste.',
  },
  columns: {
    title: 'Gespeicherte Spaltenauswahl',
    hint: 'Listen merken sich die Spalten, die du anzeigst. Setze eine Liste zurück, um ihre Standardspalten wiederzuholen.',
    state: (n: number, total: number) => `${n} von ${total} Spalten`,
    own: (n: number) => `${n} Spalten`,
    standard: 'Standard',
    reset: 'Zurücksetzen',
    resetAria: (list: string) => `${list} zurücksetzen`,
  },
  loadFailed: 'Deine gespeicherten Einstellungen konnten nicht geladen werden und lassen sich gerade nicht ändern. Lade die Seite neu, um es erneut zu versuchen.',
  saveFailed: 'Deine Einstellungen konnten nicht gespeichert werden',
};
