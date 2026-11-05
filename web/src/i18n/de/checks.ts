import type { Messages } from '../types.ts';

export const deChecks: Messages['checks'] = {
  title: 'Checks',
  tabs: {
    checks: 'Checks',
    groups: 'Überwachungsgruppen',
    rules: 'Regeln',
    items: 'Elemente',
  },
  interval: {
    seconds: (n: number) => `${n} s`,
    minutes: (n: number) => `${n} Min.`,
    hours: (n: number) => `${n} Std.`,
  },
  enabled: 'aktiviert',
  disabled: 'deaktiviert',
  enabledField: 'Aktiviert',
  organization: 'Organisation',
  edit: 'Bearbeiten',
  copy: 'Kopieren',
  deleteItem: 'Löschen…',
  deleteFailed: 'Löschen fehlgeschlagen',
  saveFailed: 'Speichern fehlgeschlagen',
  name: 'Name',
  note: 'Notiz',
  host: 'Host',
  matching: 'Abgleich läuft…',
  origin: {
    seed: 'Standard',
    custom: 'eigen',
    shared: 'geteilt',
  },
  severity: {
    warn: 'Warnung',
    crit: 'kritisch',
  },
  scope: {
    org: 'Die ganze Organisation',
    group: 'Eine Überwachungsgruppe',
    groupNone: 'Eine Überwachungsgruppe (noch keine)',
    host: 'Ein Host',
    item: 'Ein Element auf einem Host',
  },
  appliesTo: 'Gilt für',
  monitoringGroup: 'Überwachungsgruppe',
  chooseGroup: 'Überwachungsgruppe auswählen…',
  groupFallback: (id: string) => `Gruppe ${id}`,
  groupCurrent: (id: string) => `Gruppe ${id} (aktuelle Bindung)`,
  currentBinding: (name: string) => `${name} (aktuelle Bindung)`,
  hostPicker: {
    searchLabel: 'Überwachte Hosts durchsuchen',
    searchPlaceholder: 'Überwachte Hosts durchsuchen…',
    choose: 'Überwachten Host auswählen…',
    moreMatch: 'Es passen mehr Hosts, als aufgelistet sind – die Suche eingrenzen.',
    loadFailed: 'Die überwachten Hosts konnten nicht geladen werden.',
  },
  list: {
    noAccess: 'Checks sind für Mitglieder dieser Organisation sichtbar.',
    summary: (n: number) =>
      `${n === 1 ? '1 Definition' : `${n} Definitionen`} · ein engerer Geltungsbereich überschreibt einen weiteren unter demselben Schlüssel`,
    newCheck: 'Neuer Check',
    columns: {
      check: 'Check',
      plugin: 'Plugin',
      bindsTo: 'Gebunden an',
      interval: 'Intervall',
      state: 'Zustand',
    },
    empty: 'Noch keine Check-Definitionen.',
    deleteConfirm: (key: string) => `Check ${key} löschen? Die Hosts, die er erreicht, messen ihn nicht mehr.`,
    deleteSeeded: ' Er wird mit jeder Organisation ausgeliefert und kommt nicht von selbst zurück.',
  },
  dialog: {
    editTitle: (key: string) => `${key} bearbeiten`,
    copyTitle: (key: string) => `${key} kopieren`,
    newTitle: 'Neuer Check',
    plugin: 'Plugin',
    plugins: {
      cpu: 'CPU',
      memory: 'Arbeitsspeicher',
      disk: 'Datenträger',
      service: 'Dienst-Units',
    },
    key: 'Schlüssel',
    keyTitle: 'Kleinbuchstaben, Ziffern und Bindestriche; höchstens 63 Zeichen, nicht mit einem Bindestrich beginnend',
    seededNote:
      'Diese Definition wird mit jeder Organisation ausgeliefert; Schlüssel und Plugin sind fest. Um sie zu ändern, eine Kopie anlegen – Intervall, Parameter, Bindung und Aktivierung lassen sich hier bearbeiten.',
    sharedNote:
      'Dieser Check wird von der Installation verwaltet; Schlüssel, Plugin, Intervall und Parameter sind hier fest. Zum Ändern eine Kopie anlegen – Bindung und Aktivierung gehören dieser Organisation und lassen sich hier bearbeiten.',
    copyNote:
      'Eine Kopie braucht entweder einen eigenen Schlüssel oder eine engere Bindung als die Zeile, von der sie stammt: Ein engerer Geltungsbereich mit demselben Schlüssel überschreibt diesen Check für die betroffenen Hosts.',
    units: 'Units (eine pro Zeile)',
    mounts: 'Einhängepunkte (einer pro Zeile, leer bedeutet jeden Einhängepunkt)',
    bindsTo: 'Gebunden an',
    interval: 'Intervall (Sekunden)',
  },
  groups: {
    noAccess: 'Überwachungsgruppen sind für Mitglieder dieser Organisation sichtbar.',
    deleteConfirm: (name: string) =>
      `Überwachungsgruppe ${name} löschen? Die daran gebundenen Check-Definitionen werden mit gelöscht.`,
    summary: (n: number) =>
      `${n === 1 ? '1 Gruppe' : `${n} Gruppen`} · die höhere Priorität gewinnt, wenn zwei Gruppen denselben Check-Schlüssel an einen Host binden`,
    newGroup: 'Neue Gruppe',
    columns: {
      group: 'Gruppe',
      selector: 'Selektor',
      priority: 'Priorität',
    },
    empty: 'Noch keine Überwachungsgruppen.',
    editTitle: (name: string) => `${name} bearbeiten`,
    newTitle: 'Neue Überwachungsgruppe',
    selector: 'Selektor',
    priority: 'Priorität',
    typeSelector: 'Einen Selektor eingeben, um zu sehen, welche Hosts er trifft.',
    resolveFailed: 'Dieser Selektor konnte nicht aufgelöst werden',
    noMatch: 'Kein Host trifft auf diesen Selektor zu',
    matches: (n: number) => (n === 1 ? 'Trifft auf 1 Host zu' : `Trifft auf ${n} Hosts zu`),
    notMonitored: (name: string) => `${name} (nicht überwacht)`,
  },
  rules: {
    noAccess: 'Regeln sind für Mitglieder dieser Organisation sichtbar.',
    deleteConfirm: (name: string) => `Regel ${name} löschen? Hosts werden dann nicht mehr danach bewertet.`,
    deleteSeeded: ' Sie wird mit jeder Organisation ausgeliefert und kommt nicht von selbst zurück.',
    summary: (n: number) =>
      `${n === 1 ? '1 Regel' : `${n} Regeln`} · ein engerer Geltungsbereich überschreibt einen weiteren für dieselbe Metrik`,
    newRule: 'Neue Regel',
    noMetricsTitle: 'Noch keine Metriken für eine Regel',
    noMetricsBody: 'Eine Regel bewertet einen gemessenen Wert, daher bleibt „Neue Regel“ nicht verfügbar, bis ein Agent seine erste Metrik meldet.',
    columns: {
      rule: 'Regel',
      severity: 'Schweregrad',
      appliesTo: 'Gilt für',
      for: 'Dauer',
      state: 'Zustand',
    },
    empty: 'Noch keine Regeln.',
    origin: {
      seed: 'Standard',
      user: 'eigen',
    },
  },
  ruleDialog: {
    editTitle: (name: string) => `${name} bearbeiten`,
    newTitle: 'Neue Regel',
    metric: 'Metrik',
    severity: 'Schweregrad',
    severityWarn: 'Warnung',
    severityCrit: 'Kritisch',
    forSeconds: 'Dauer (Sekunden)',
    seededNote:
      'Diese Regel wird mit jeder Organisation ausgeliefert; Metrik, Geltungsbereich und Schweregrad sind fest. Schwellenwert, Rückkehrbedingung, Dauer und Aktivierung lassen sich hier bearbeiten.',
    toExpression: 'Zum Ausdruckseditor wechseln',
    toForm: 'Zum Formulareditor wechseln',
    confirmSwitch:
      'Der Wechsel zum Ausdruckseditor ist von hier aus eine Einbahnstraße. Der Rückweg zum Formulareditor bleibt nur offen, solange der ' +
      'Ausdruck genau die Form behält, die das Formular erzeugt – ein Vergleich mit dieser Metrik, keine Arithmetik, kein Zeitfenster, kein „or“. ' +
      'Sobald er diese Form verlässt, lässt sich diese Regel künftig nur noch als Ausdruck bearbeiten. Fortfahren?',
    confirmDropClear: (op: string, value: string) =>
      `Diese Regel kehrt bei „${op} ${value}“ in den Normalzustand zurück. Der Ausdruckseditor kennt keine Syntax für eine Rückkehrbedingung, ` +
      'daher entfernt das Speichern als Ausdruck sie – die Regel wechselt dann bei jedem Überschreiten ihres Schwellenwerts den Zustand, ' +
      'statt zu halten, bis die Rückkehrbedingung erfüllt ist. Rückkehrbedingung entfernen und fortfahren?',
    expression: 'Ausdruck',
    item: 'Element',
    itemPlaceholder: '/var, sshd, …',
    form: {
      operator: 'Operator',
      value: 'Wert',
      labels: 'Labels (eines pro Zeile)',
      label: 'Label',
      clearCondition: 'Rückkehrbedingung',
      noClearCondition: 'Keine Rückkehrbedingung',
      clearValue: 'Rückkehrwert',
    },
  },
  items: {
    noAccess: 'Element-Entscheidungen sind für Mitglieder dieser Organisation sichtbar.',
    deleteConfirm: (decision: string, pattern: string) =>
      `Diese Entscheidung (${decision}) für das Muster „${pattern}“ löschen? Die Elemente, die sie nennt, werden wieder normal bewertet.`,
    summary: (n: number) =>
      `${n === 1 ? '1 Entscheidung' : `${n} Entscheidungen`} · eine Host-Entscheidung hat Vorrang vor einer Organisationsentscheidung für denselben Check`,
    newDecision: 'Neue Entscheidung',
    noMetricsTitle: 'Noch keine Checks für eine Entscheidung',
    noMetricsBody: 'Eine Entscheidung nennt ein Element eines Checks, daher bleibt „Neue Entscheidung“ nicht verfügbar, bis ein Agent seine erste Metrik meldet.',
    columns: {
      target: 'Ziel',
      check: 'Check',
      pattern: 'Muster',
      verdict: 'Entscheidung',
      note: 'Notiz',
      created: 'Erstellt',
    },
    verdict: {
      ignore: 'ignorieren',
      watch: 'überwachen',
    },
    matchKind: {
      exact: 'exakt',
      glob: 'Glob',
    },
    editDecision: (pattern: string) => `Entscheidung für ${pattern} bearbeiten`,
    decisionActions: (pattern: string) => `Aktionen für Entscheidung ${pattern}`,
    empty: 'Noch keine Element-Entscheidungen.',
  },
  itemDialog: {
    newTitle: 'Neue Element-Entscheidung',
    appliesTo: 'Gilt für',
    check: 'Check',
    noChecks: 'Noch keine Checks',
    matchKind: 'Mustertyp',
    exact: 'Exakt – ein buchstäblicher Elementname',
    glob: 'Glob – ein Muster mit „*“',
    pattern: 'Muster',
    tooLong: (bytes: number, max: number) =>
      `Dieses Muster ist ${bytes} Bytes lang; das Limit liegt bei ${max}. Elementnamen außerhalb des ASCII-Bereichs benötigen jeweils mehr als ein Byte, daher kann ein Muster die Zeichenzählung des Felds bestehen und trotzdem zu lang sein.`,
    verdict: 'Entscheidung',
    verdictIgnore: 'Ignorieren – dieses Element nicht mehr bewerten',
    verdictWatch: 'Überwachen – bewerten, auch wenn eine weitere Entscheidung es ignoriert',
    verdictWatchStored: 'Überwachen (außerhalb dieses Editors gespeichert – ohne Wirkung auf Organisationsebene)',
    watchNotOffered:
      'Überwachen steht auf Organisationsebene nicht zur Wahl: Überwachen ist ohnehin der Standard, eine organisationsweite Überwachung würde nichts ändern. Verfügbar ist es, sobald diese Entscheidung auf einen einzelnen Host beschränkt ist – dafür ist es als Gegenregel gedacht.',
    watchMismatch:
      'Diese Entscheidung ist als organisationsweite Überwachung gespeichert, die keine Wirkung hat und die der Server jetzt nicht mehr speichert. Ignorieren wählen oder auf einen Host beschränken, damit Überwachen die Gegenregel bleibt, die es sein soll.',
    notePlaceholder:
      'Warum muss dieses Element nicht bewertet werden? Die nächste Person im Betrieb liest das, bevor sie es wieder einschaltet.',
    confirmBroad: (total: number) =>
      `Dieses Muster trifft derzeit ${total === 1 ? '1 Element' : `${total} Elemente`} – mehr, als die Vorschau vollständig auflistet. Das Speichern ändert jedes einzelne davon. Fortfahren?`,
    typePattern: 'Ein Muster eingeben, um zu sehen, welche Elemente es trifft.',
    resolveFailed: 'Dieses Muster konnte nicht aufgelöst werden',
    noMatch: 'Trifft auf kein derzeit bekanntes Element zu – gilt aber, sobald eines erscheint',
    matches: (n: number) => (n === 1 ? 'Trifft auf 1 Element zu' : `Trifft auf ${n} Elemente zu`),
    showingFirst: (n: number) =>
      n === 1 ? 'Unten wird das erste angezeigt – das Muster eingrenzen, um den Rest zu sehen.' : `Unten werden die ersten ${n} angezeigt – das Muster eingrenzen, um den Rest zu sehen.`,
  },
};
