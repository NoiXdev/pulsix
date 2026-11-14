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
  rowActions: (key: string, binding: string) => `Aktionen für ${key} (${binding})`,
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
      network: 'Netzwerk',
      uptime: 'Uptime',
      icmp: 'Ping (ICMP)',
      tcp: 'TCP-Port',
      http: 'HTTP(S)',
      tls: 'TLS-Zertifikat',
      dns: 'DNS',
      snmp: 'SNMP-Gerät',
      docker: 'Docker-Container',
      temperature: 'Temperatur',
      smart: 'SMART-Festplattenzustand',
      raid: 'RAID-Arrays',
      postgres: 'PostgreSQL',
      mysql: 'MySQL / MariaDB',
      redis: 'Redis / Valkey',
      nginx: 'nginx',
      apache: 'Apache httpd',
      caddy: 'Caddy',
      phpfpm: 'PHP-FPM',
      script: 'Eigenes Skript',
    },
    key: 'Schlüssel',
    keyTitle: 'Kleinbuchstaben, Ziffern und Bindestriche; höchstens 63 Zeichen, nicht mit einem Bindestrich beginnend',
    seededNote:
      'Diese Definition wird mit jeder Organisation ausgeliefert; Schlüssel und Plugin sind fest. Um sie zu ändern, eine Kopie anlegen – Intervall, Parameter, Bindung und Aktivierung lassen sich hier bearbeiten.',
    sharedNote:
      'Dieser Check wird von der Installation verwaltet; Schlüssel, Plugin, Intervall und Parameter sind hier fest. Zum Ändern eine Kopie anlegen – Bindung und Aktivierung gehören dieser Organisation und lassen sich hier bearbeiten.',
    copyNote:
      'Eine Kopie braucht entweder einen eigenen Schlüssel oder eine engere Bindung als die Zeile, von der sie stammt: Ein engerer Geltungsbereich mit demselben Schlüssel überschreibt diesen Check für die betroffenen Hosts.',
    bindsTo: 'Gebunden an',
    interval: 'Intervall (Sekunden)',
  },
  pluginsLoading: 'Die Check-Plugins werden geladen…',
  pluginsLoadFailed: 'Die Check-Plugins konnten nicht geladen werden.',
  synthetic: {
    pluginHint: 'läuft auf dem Pulsix-Server',
    runsFromServer: 'Läuft auf dem Pulsix-Server, nicht auf dem Host.',
    rulesKey: (plugin: string) => `Alarmregeln für ${plugin}.*-Metriken gelten für jeden Check dieses Typs, unabhängig von seinem Schlüssel.`,
  },
  params: {
    disk: {
      mounts: { label: 'Einhängepunkte (einer pro Zeile, leer bedeutet jeden Einhängepunkt)', placeholder: '/\n/var' },
    },
    service: {
      units: {
        label: 'Units (eine pro Zeile)',
        hint: 'systemd-Units unter Linux, Dienstnamen unter Windows, launchd-Labels unter macOS – einer pro Zeile',
        placeholder: 'sshd',
      },
    },
    network: {
      interfaces: {
        label: 'Schnittstellen (eine pro Zeile, leer bedeutet jede echte Schnittstelle)',
        hint: 'Loopback sowie Container- und virtuelle Schnittstellen werden standardmäßig übersprungen. Eine hier genannte Schnittstelle wird immer gemessen.',
        placeholder: 'eth0\nbond0',
      },
    },
    docker: {
      selection: { label: 'Container', options: { all: 'Alle Container', labeled: 'Nur Container mit Label' } },
      label: {
        label: 'Label',
        hint: 'Ein Label-Schlüssel oder Schlüssel=Wert. Leer bedeutet pulsix.monitor=true. Werden alle Container geprüft, wird ein Container ausgelassen, dessen Label mit diesem Schlüssel auf false steht.',
      },
      socket: {
        label: 'Docker-Socket',
        hint: 'Ein absoluter Pfad oder eine Windows-Pipe. Leer bedeutet den Standard der Plattform: /var/run/docker.sock, unter Windows npipe:////./pipe/docker_engine.',
      },
    },
    temperature: {
      sensors: {
        label: 'Sensoren (einer pro Zeile, leer bedeutet jeden Sensor)',
        hint: 'Sensornamen, wie der Host sie meldet; leer bedeutet jeden Sensor.',
        placeholder: 'coretemp_core_0\nnvme_composite',
      },
    },
    smart: {
      devices: {
        label: 'Geräte (eines pro Zeile, leer bedeutet jede Platte)',
        hint: 'Gerätepfade wie /dev/sda oder /dev/nvme0; leer bedeutet jede Platte, die smartctl findet. Benötigt smartmontools auf dem Host.',
        placeholder: '/dev/sda\n/dev/nvme0',
      },
    },
    raid: {
      arrays: {
        label: 'Arrays (eines pro Zeile, leer bedeutet jedes Array)',
        hint: 'Array-Namen wie md0; leer bedeutet jedes Array. Nur Linux-Software-RAID.',
        placeholder: 'md0\nmd1',
      },
    },
    postgres: {
      secret: {
        label: 'Secret',
        hint: 'Name eines Secrets in der agent.yaml jedes Hosts: es enthält Serveradresse und Login. Der Wert verlässt den Host nie.',
        placeholder: 'db-main',
      },
      databases: {
        label: 'Datenbanken (eine pro Zeile, leer bedeutet jede Datenbank)',
        hint: 'Datenbanken, deren Größe gemeldet wird; leer bedeutet jede Datenbank, die Verbindungen annimmt und keine Vorlage ist.',
        placeholder: 'app\nanalytics',
      },
    },
    mysql: {
      secret: {
        label: 'Secret',
        hint: 'Name eines Secrets in der agent.yaml jedes Hosts: es enthält Serveradresse und Login. Der Wert verlässt den Host nie.',
        placeholder: 'db-main',
      },
    },
    redis: {
      secret: {
        label: 'Secret (optional)',
        hint: 'Name eines Secrets in der agent.yaml jedes Hosts: es enthält Serveradresse und Login. Der Wert verlässt den Host nie. Ohne Secret liest der Check 127.0.0.1:6379 ohne Login.',
        placeholder: 'db-main',
      },
    },
    nginx: {
      url: { label: 'URL der Status-Seite', hint: 'Die stub_status-Seite von nginx, die der Agent auf jedem Host liest. Ohne Wert liest der Check http://127.0.0.1/nginx_status.' },
      secret: {
        label: 'Secret (optional)',
        hint: 'Name eines Secrets in der agent.yaml jedes Hosts: es enthält den HTTP-Basic-Auth-Login (basic_auth_user, basic_auth_password). Der Wert verlässt den Host nie. Ohne Secret wird kein Login gesendet.',
        placeholder: 'status-login',
      },
      verify_tls: { label: 'TLS-Zertifikat prüfen' },
    },
    apache: {
      url: { label: 'URL der Status-Seite', hint: 'Die mod_status-Seite des Apache httpd, die der Agent auf jedem Host liest; fehlt der Query-Schlüssel auto, hängt der Agent ?auto an. Ohne Wert liest der Check http://127.0.0.1/server-status?auto.' },
      secret: {
        label: 'Secret (optional)',
        hint: 'Name eines Secrets in der agent.yaml jedes Hosts: es enthält den HTTP-Basic-Auth-Login (basic_auth_user, basic_auth_password). Der Wert verlässt den Host nie. Ohne Secret wird kein Login gesendet.',
        placeholder: 'status-login',
      },
      verify_tls: { label: 'TLS-Zertifikat prüfen' },
    },
    caddy: {
      url: { label: 'URL der Metriken', hint: 'Die Prometheus-Metrikseite von Caddy, die der Agent auf jedem Host liest: der Admin-Endpunkt oder ein metrics-Handler auf einer Site. Die HTTP-Metriken von Caddy müssen eingeschaltet sein. Ohne Wert liest der Check http://127.0.0.1:2019/metrics.' },
      secret: {
        label: 'Secret (optional)',
        hint: 'Name eines Secrets in der agent.yaml jedes Hosts: es enthält den HTTP-Basic-Auth-Login (basic_auth_user, basic_auth_password). Der Wert verlässt den Host nie. Ohne Secret wird kein Login gesendet.',
        placeholder: 'status-login',
      },
      verify_tls: { label: 'TLS-Zertifikat prüfen' },
    },
    phpfpm: {
      urls: {
        label: 'URLs der Status-Seiten (eine pro Zeile, je ein Pool)',
        hint: 'Die Status-Seite (pm.status_path) jedes PHP-FPM-Pools, ausgeliefert vom Webserver davor und gelesen vom Agent auf jedem Host; fehlt der Query-Schlüssel json, hängt der Agent ?json an. Jede URL ist ein Pool und benennt seine Messwerte. Leer heißt ein Pool unter http://127.0.0.1/status.',
      },
      secret: {
        label: 'Secret (optional)',
        hint: 'Name eines Secrets in der agent.yaml jedes Hosts: es enthält den HTTP-Basic-Auth-Login (basic_auth_user, basic_auth_password), der an jeden Pool gesendet wird. Der Wert verlässt den Host nie. Ohne Secret wird kein Login gesendet.',
        placeholder: 'status-login',
      },
      verify_tls: { label: 'TLS-Zertifikat prüfen' },
    },
    script: {
      commands: {
        label: 'Befehle (einer pro Zeile)',
        hint: 'Namen von Befehlen, die in der agent.yaml jedes Hosts unter commands: definiert sind; gesendet wird nur der Name, was läuft, bestimmt der Host. Ein Host, der einen Namen nicht definiert, meldet ihn als unbekannt.',
        placeholder: 'backup_age\ndisk_root',
      },
      format: {
        label: 'Ausgabeformat',
        options: { nagios: 'Nagios-Plugin (Exit-Code und Perfdata)', json: 'JSON (status, message, metrics)' },
      },
      timeout_seconds: {
        label: 'Timeout (Sekunden)',
        hint: 'Ein Befehl, der dann noch läuft, wird samt aller von ihm gestarteten Prozesse beendet und als unbekannt gemeldet.',
      },
    },
    icmp: {
      count: { label: 'Pings pro Durchlauf' },
      timeout_s: { label: 'Timeout (Sekunden)' },
    },
    tcp: {
      port: { label: 'Port' },
      send: {
        label: 'Nach dem Verbinden senden',
        hint: '\\r\\n steht für einen Zeilenumbruch, \\t für einen Tabulator und \\\\ für einen Backslash.',
      },
      expect: { label: 'In der Antwort erwartet', hint: 'Wird in den ersten 4 KiB gesucht, die der Server sendet.' },
      timeout_s: { label: 'Timeout (Sekunden)' },
    },
    http: {
      url: { label: 'URL', hint: '{address} wird durch die Adresse des Hosts ersetzt.' },
      method: { label: 'Methode' },
      body_contains: { label: 'Inhalt enthält', hint: 'Wird im ersten MiB des Inhalts gesucht.' },
      timeout_s: { label: 'Timeout (Sekunden)' },
      follow_redirects: { label: 'Weiterleitungen folgen (höchstens 10)' },
      verify_tls: { label: 'TLS-Zertifikat prüfen' },
    },
    tls: {
      port: { label: 'Port' },
      server_name: { label: 'Servername (SNI)', hint: 'Leer bedeutet die Adresse des Hosts.' },
      timeout_s: { label: 'Timeout (Sekunden)' },
    },
    dns: {
      name: { label: 'Aufzulösender Name', hint: 'Leer bedeutet die Adresse des Hosts; {address} wird durch sie ersetzt.' },
      type: { label: 'Record-Typ' },
      server: {
        label: 'DNS-Server',
        hint: 'Eine IP-Adresse mit optionalem Port, eine IPv6-Adresse mit Port in eckigen Klammern. Leer bedeutet den eigenen Resolver des Pulsix-Servers.',
      },
      expect: {
        label: 'Erwartete Antwort',
        hint: 'Eine Antwort muss genau so lauten; Groß- und Kleinschreibung und ein abschließender Punkt zählen nicht.',
      },
      timeout_s: { label: 'Timeout (Sekunden)' },
    },
    snmp: {
      secret: {
        label: 'Secret',
        hint: 'Name eines Secrets dieser Organisation unter Einstellungen → Secrets: es enthält die Community (v2c) oder den SNMPv3-Login. Seine Werte werden nie wieder angezeigt und verlassen den Pulsix-Server nie.',
        placeholder: 'switch-core',
      },
      version: { label: 'SNMP-Version', options: { v2c: 'v2c (Community)', v3: 'v3 (Benutzername und Passwörter)' } },
      port: { label: 'Port' },
      timeout_seconds: { label: 'Timeout pro Anfrage (Sekunden)' },
      oids: {
        label: 'Werte (einer pro Zeile, Name=OID)',
        hint: 'Jede Zeile liest einen Wert vom Gerät und meldet ihn unter seinem Namen. Nur numerische OIDs wie 1.3.6.1.2.1.1.3.0.',
        placeholder: 'ups_load=1.3.6.1.2.1.33.1.4.4.1.5.1',
      },
      interfaces: { label: 'Schnittstellen überwachen', hint: 'Status, Datenverkehr und Fehler jeder Schnittstelle, die das Gerät auflistet, höchstens 128.' },
    },
  },
  paramErrors: {
    wholeRange: (min: number, max: number) => `muss eine ganze Zahl zwischen ${min} und ${max} sein`,
    tooMany: (max: number) => `darf höchstens ${max} Einträge haben`,
    tooLong: (max: number) => `jeder Eintrag darf höchstens ${max} Byte lang sein`,
    formats: {
      notBlank: 'darf nicht leer sein',
      noControl: 'Einträge dürfen keine Steuerzeichen enthalten',
      devicePath: 'jeder Eintrag muss mit /dev/ beginnen, darf nur Buchstaben, Ziffern und . _ / , : + @ - enthalten und kein ..-Segment haben',
      secretName: 'muss 1 bis 64 Zeichen lang sein: Buchstaben, Ziffern und . _ -',
      commandName: 'jeder Eintrag muss ein Befehlsname aus 1 bis 64 Zeichen sein: Buchstaben, Ziffern und . _ -',
      snmpOID: 'jeder Eintrag muss Name=OID sein: ein Name aus 1 bis 64 Zeichen (Buchstaben, Ziffern und . _ -) und eine numerische OID wie 1.3.6.1.2.1.1.3.0',
      url: 'muss eine http://- oder https://-URL ohne Benutzername und Passwort sein',
      statusURL: 'muss eine http://- oder https://-URL ohne Benutzername, Passwort und #Fragment und ohne {address} sein',
      dnsName: 'muss ein DNS-Name sein: Buchstaben, Ziffern, Bindestriche und Unterstriche zwischen Punkten',
      server: 'muss eine IP-Adresse sein, optional mit Port',
      dockerLabel: 'muss ein Label-Schlüssel sein (Buchstaben, Ziffern, „.“, „_“, „/“ und „-“, höchstens 255 Zeichen), optional gefolgt von =Wert mit höchstens 255 Byte',
      dockerSocket: 'muss ein absoluter Pfad oder npipe:////./pipe/ gefolgt von einem Pipe-Namen sein, höchstens 255 Zeichen',
    },
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
    itemCheck: 'Check',
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
    checkPlaceholder: 'disk, disk-data, …',
    checkInvalid: 'Ein Check-Schlüssel besteht nur aus Kleinbuchstaben, Ziffern und Bindestrichen (bis zu 63 Zeichen), z. B. disk-data.',
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
