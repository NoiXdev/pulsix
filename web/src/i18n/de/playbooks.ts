import type { Messages } from '../types.ts';

export const dePlaybooks: Messages['playbooks'] = {
  list: {
    title: 'Playbooks',
    summary: (n: number) =>
      n === 1
        ? '1 Playbook · eine Job-Vorlage führt eines davon gegen ihren Geltungsbereich aus'
        : `${n} Playbooks · eine Job-Vorlage führt eines davon gegen ihren Geltungsbereich aus`,
    add: 'Neues Editor-Playbook',
    columns: {
      source: 'Quelle',
      summaryPath: 'Zusammenfassung / Pfad',
      repository: 'Repository',
      updated: 'Aktualisiert',
    },
    sourceRepo: 'Repository',
    sourceEditor: 'Editor',
    empty: 'Noch keine Playbooks – ein Repository hinzufügen und synchronisieren oder eines im Editor schreiben.',
    unnamed: '(unbenanntes Play)',
  },
  editor: {
    saved: 'Playbook gespeichert',
    created: 'Playbook angelegt',
    saveFailed: 'Speichern fehlgeschlagen',
    confirmDelete: (name: string) => `Playbook ${name} löschen?`,
    fallbackName: 'Editor-Playbook',
    deleted: 'Playbook gelöscht',
    deleteFailed: 'Löschen fehlgeschlagen',
    newTitle: 'Neues Editor-Playbook',
    yaml: 'Playbook-YAML',
    ready: 'Wird als Liste von Plays gelesen – bereit zum Speichern.',
    save: 'Playbook speichern',
  },
  validate: {
    tooLarge: 'das Playbook überschreitet 512 KiB',
    malformed: (detail: string) => `fehlerhaftes YAML: ${detail}`,
    couldNotParse: 'konnte nicht gelesen werden',
    expectedList: 'eine Liste von Plays wird erwartet',
    playMapping: 'Play 1: muss ein Mapping sein',
    missingHosts: 'Play 1: hosts fehlt',
    hostsString: 'Play 1: hosts muss ein String sein',
  },
};
