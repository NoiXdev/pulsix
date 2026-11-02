import type { Messages } from '../types.ts';

export const deTemplates: Messages['templates'] = {
  list: {
    title: 'Job-Vorlagen',
    summary: (n: number) =>
      n === 1
        ? '1 Vorlage · ein Playbook, sein Geltungsbereich, seine Extra-Variablen und eine Schaltfläche zum Ausführen'
        : `${n} Vorlagen · ein Playbook, sein Geltungsbereich, seine Extra-Variablen und eine Schaltfläche zum Ausführen`,
    add: 'Vorlage hinzufügen',
    columns: {
      name: 'Name',
      playbook: 'Playbook',
      scope: 'Geltungsbereich',
      credential: 'Standard-Zugangsdaten',
      actions: 'Aktionen',
    },
    managed: 'Von der Installation verwaltet',
    triggersLabel: (name: string) => `Trigger von ${name}`,
    schedulesLabel: (name: string) => `Zeitpläne von ${name}`,
    runLabel: (name: string) => `${name} ausführen`,
    run: 'Ausführen',
    started: (name: string) => `Lauf von ${name} gestartet`,
    startFailed: 'Der Lauf konnte nicht gestartet werden',
    empty: 'Noch keine Job-Vorlagen.',
  },
  playbook: {
    notSynced: 'noch nicht synchronisiert',
    repo: (path: string) => `Repository: ${path}`,
    editor: 'Editor-Playbook',
  },
  scope: {
    whole: 'ganze Organisation',
    hosts: (n: number) => (n === 1 ? '1 Host' : `${n} Hosts`),
    groupAndHosts: (group: string, n: number) => `${group} + ${n === 1 ? '1 Host' : `${n} Hosts`}`,
  },
  picker: {
    legend: 'Geltungsbereich',
    group: 'Gruppe',
    wholeOrganization: 'Ganze Organisation',
    hosts: (n: number) => `Hosts (${n} ausgewählt)`,
    searchPlaceholder: 'Verwaltete Hosts suchen…',
    include: (name: string) => `${name} einschließen`,
    noMatch: 'Kein verwalteter Host passt.',
    hint: 'Ohne Auswahl läuft die Vorlage gegen jeden verwalteten Host.',
  },
  dialog: {
    saved: (name: string) => `Vorlage ${name} gespeichert`,
    created: (name: string) => `Vorlage ${name} angelegt`,
    saveFailed: 'Speichern fehlgeschlagen',
    newTitle: 'Neue Job-Vorlage',
    managedNote: 'Name, Playbook, Limit und Extra-Variablen gehören zur Definition der Installation.',
    name: 'Name',
    namePlaceholder: 'deploy-site',
    playbook: 'Playbook',
    choosePlaybook: 'Playbook wählen…',
    limit: 'Limit-Muster',
    limitPlaceholder: 'z. B. web*:&!web-03',
    limitHint: 'Freies Ansible-Limit-Muster, leer = kein Limit.',
    schemaSummary: 'Schema der Extra-Variablen',
    extraVars: 'Extra-Variablen',
    credential: 'Standard-SSH-Zugangsdaten',
    credentialNone: '— keine —',
    newCredential: 'Neue Zugangsdaten…',
    credentialHint: 'SSH-Schlüssel oder Passwort als Zugangsdaten für Hosts ohne eigene.',
    vaultHelp:
      'Vault-Passwörter mit Bezeichnung, die diese Vorlage zusätzlich zu denen ihres Repositorys ergänzt; eine Bezeichnung, die auch das Repository hat, wird zu dieser aufgelöst.',
    confirmDelete: (name: string) => `Vorlage ${name} löschen?`,
    deleted: (name: string) => `Vorlage ${name} gelöscht`,
    deleteFailed: 'Löschen fehlgeschlagen',
    save: 'Vorlage speichern',
    create: 'Vorlage anlegen',
  },
};
