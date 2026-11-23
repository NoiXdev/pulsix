import type { Messages } from '../types.ts';

const variables = (n: number) => (n === 1 ? '1 Variable' : `${n} Variablen`);

export const dePlaybookRepoText: Messages['playbooks']['page']['repoText'] = {
  asOf: (when: string, commit: string) => `Stand der Synchronisierung vom ${when}, Commit ${commit}`,
  syncedOn: (when: string) => `Stand der Synchronisierung vom ${when}`,
  earlier: (when: string) => `Aus einer früheren Synchronisierung: die letzte, am ${when}, hat dieses Playbook nicht gelesen`,
  readOnly: 'Nur lesbar: Änderungen kommen über Git, die nächste Synchronisierung holt sie.',
  loading: 'Lese den Text der letzten Synchronisierung…',
  ready: 'Wird als Liste von Plays gelesen.',
  checkHint: 'Im Browser geprüft, am Text der letzten Synchronisierung.',
  notSynced: 'Das Repository wurde noch nicht synchronisiert: synchronisiere es, um den Text hier zu sehen.',
  noRoute: 'Dieser Server liefert den Repository-Text noch nicht.',
  readFailed: (message: string) => `Der Text konnte nicht gelesen werden: ${message}`,
  leftOut: {
    too_large: 'Die letzte Synchronisierung hat keinen Text für dieses Playbook gespeichert: die Datei ist größer als 512 KiB.',
    budget: 'Die letzte Synchronisierung hat keinen Text für dieses Playbook gespeichert: die Texte der Synchronisierung waren voll (4 MiB insgesamt).',
    not_utf8: 'Die letzte Synchronisierung hat keinen Text für dieses Playbook gespeichert: die Datei ist kein UTF-8-Text.',
    unreadable: 'Die letzte Synchronisierung hat keinen Text für dieses Playbook gespeichert: die Datei konnte nicht gelesen werden, oder ein Limit der Synchronisierung hat sie ausgelassen.',
    not_sent:
      'Die letzte Synchronisierung hat keinen Text für dieses Playbook gespeichert: die Datei fehlt vielleicht im Repository, oder ein älterer Worker hat synchronisiert. Synchronisiere erneut, sobald die Worker aktualisiert sind.',
  },
  sources: {
    none: 'Die letzte Synchronisierung hat keine Variablenquellen für dieses Playbook erfasst.',
    failed: 'Die letzte Synchronisierung konnte seine Variablenquellen nicht lesen.',
    leftOut: (n: number) => `Ein Limit hat seine Variablenquellen bei der letzten Synchronisierung ausgelassen; die davor fand ${variables(n)}.`,
    read: (n: number) => `Seine Variablenquellen bei der letzten Synchronisierung: ${variables(n)}, die eine Vorlage mit diesem Playbook als Felder anbietet.`,
  },
};

export const dePlaybookConflict: Messages['playbooks']['page']['conflict'] = {
  title: 'Jemand hat dieses Playbook gespeichert, seit du mit dem Bearbeiten begonnen hast.',
  body: 'Deine Änderungen sind noch da, aber nicht gespeichert. Neu laden verwirft sie und übernimmt den gespeicherten Text; Trotzdem meine speichern ersetzt ihn durch deinen.',
  show: 'Änderungen seitdem zeigen',
  hide: 'Änderungen seitdem ausblenden',
  before: 'als du begonnen hast',
  after: 'seitdem gespeichert',
  reload: 'Neu laden',
  saveAnyway: 'Trotzdem meine speichern',
  savedAgain: 'Inzwischen wurde erneut gespeichert: sieh dir die Änderungen seitdem noch einmal an.',
};
