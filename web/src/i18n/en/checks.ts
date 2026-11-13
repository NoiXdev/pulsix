/**
 * The Checks section: definitions, monitoring groups, rules and item
 * decisions, with their dialogs. Check keys, metric names, plugin names,
 * item names, host names, tags and rule expressions are data and never
 * keys. Enumerations the API sends (origin, severity, verdict, match kind)
 * are mapped to a label.
 */
/** The texts of one parameter field (enChecks.params). */
export interface ParamText {
  label: string;
  hint?: string;
  /** Shown while the field is empty, where the parameter has no default
   * to show instead (list fields). */
  placeholder?: string;
  /** Labels for an enum's options, by value; an option without one shows
   * its value. */
  options?: Readonly<Record<string, string>>;
}

export const enChecks = {
  title: 'Checks',
  tabs: {
    checks: 'Checks',
    groups: 'Monitoring groups',
    rules: 'Rules',
    items: 'Items',
  },
  interval: {
    seconds: (n: number) => `${n} s`,
    minutes: (n: number) => `${n} min`,
    hours: (n: number) => `${n} h`,
  },
  enabled: 'enabled',
  disabled: 'disabled',
  enabledField: 'Enabled',
  organization: 'Organization',
  edit: 'Edit',
  copy: 'Copy',
  deleteItem: 'Delete…',
  deleteFailed: 'Deleting failed',
  saveFailed: 'Saving failed',
  name: 'Name',
  note: 'Note',
  host: 'Host',
  matching: 'Matching…',
  origin: {
    seed: 'seed',
    custom: 'custom',
    shared: 'shared',
  },
  severity: {
    warn: 'warn',
    crit: 'crit',
  },
  scope: {
    org: 'The whole organization',
    group: 'A monitoring group',
    groupNone: 'A monitoring group (none yet)',
    host: 'One host',
    item: 'One item on a host',
  },
  appliesTo: 'Applies to',
  monitoringGroup: 'Monitoring group',
  chooseGroup: 'Choose a monitoring group…',
  groupFallback: (id: string) => `group ${id}`,
  rowActions: (key: string, binding: string) => `Actions for ${key} (${binding})`,
  groupCurrent: (id: string) => `group ${id} (current binding)`,
  currentBinding: (name: string) => `${name} (current binding)`,
  hostPicker: {
    searchLabel: 'Search monitored hosts',
    searchPlaceholder: 'Search monitored hosts…',
    choose: 'Choose a monitored host…',
    moreMatch: 'More hosts match than are listed — narrow the search.',
    loadFailed: 'Could not load monitored hosts.',
  },
  list: {
    noAccess: 'Checks are visible to members of this organization.',
    summary: (n: number) => `${n} definitions · a narrower scope overrides a wider one under the same key`,
    newCheck: 'New check',
    columns: {
      check: 'Check',
      plugin: 'Plugin',
      bindsTo: 'Binds to',
      interval: 'Interval',
      state: 'State',
    },
    empty: 'No check definitions yet.',
    deleteConfirm: (key: string) => `Delete check ${key}? The hosts it reaches stop measuring it.`,
    deleteSeeded: ' It ships with every organization and will not come back on its own.',
  },
  dialog: {
    editTitle: (key: string) => `Edit ${key}`,
    copyTitle: (key: string) => `Copy ${key}`,
    newTitle: 'New check',
    plugin: 'Plugin',
    plugins: {
      cpu: 'CPU',
      memory: 'Memory',
      disk: 'Disk',
      service: 'Service units',
      network: 'Network',
      uptime: 'Uptime',
      icmp: 'Ping (ICMP)',
      tcp: 'TCP port',
      http: 'HTTP(S)',
      tls: 'TLS certificate',
      dns: 'DNS',
      docker: 'Docker containers',
      temperature: 'Temperature',
      smart: 'SMART disk health',
      raid: 'RAID arrays',
      postgres: 'PostgreSQL',
      mysql: 'MySQL / MariaDB',
      redis: 'Redis / Valkey',
      nginx: 'nginx',
      apache: 'Apache httpd',
      caddy: 'Caddy',
      phpfpm: 'PHP-FPM',
    },
    key: 'Key',
    keyTitle: 'Lower-case letters, digits and hyphens; up to 63 characters, not starting with a hyphen',
    seededNote:
      'This definition ships with every organization; its key and plugin are fixed. Copy it to change either — its interval, parameters, binding and enabled state are editable here.',
    sharedNote:
      "This check is managed by the installation; its key, plugin, interval and parameters are fixed here. Copy it to change any of them — its binding and enabled state are this organization's own and stay editable here.",
    copyNote:
      'A copy needs either a key of its own or a narrower binding than the row it came from: a narrower scope with the same key is what overrides that check for those hosts.',
    bindsTo: 'Binds to',
    interval: 'Interval (seconds)',
  },
  pluginsLoading: 'Loading the check plugins…',
  pluginsLoadFailed: 'Could not load the check plugins.',
  /** The markers of a plugin that runs on the Pulsix server rather than on
   * the host (the synthetic probes, backlog 3.13; `runs: server` in GET
   * /check-plugins). */
  synthetic: {
    pluginHint: 'runs from the Pulsix server',
    runsFromServer: 'Runs from the Pulsix server, not on the host.',
    rulesKey: (plugin: string) => `Alert rules on ${plugin}.* metrics apply to every check of this type, whatever its key.`,
  },
  /** Every check plugin's parameter fields, keyed by plugin and parameter
   * as GET /check-plugins names them (backlog 3.54), shared by the check
   * dialog and the instance's shared check form. Every parameter a
   * descriptor lists needs a label here (paramTexts.test.ts); a hint, a
   * placeholder (list fields — the others show the default) and labels for
   * enum options that are no text to show are optional. */
  params: {
    disk: {
      mounts: { label: 'Mounts (one per line, empty means every mount)', placeholder: '/\n/var' },
    },
    service: {
      units: {
        label: 'Units (one per line)',
        hint: 'systemd units on Linux, service names on Windows, launchd labels on macOS — one per line',
        placeholder: 'sshd',
      },
    },
    network: {
      interfaces: {
        label: 'Interfaces (one per line, empty means every real interface)',
        hint: 'Loopback and container or virtual interfaces are skipped by default. An interface named here is always measured.',
        placeholder: 'eth0\nbond0',
      },
    },
    docker: {
      selection: { label: 'Containers', options: { all: 'All containers', labeled: 'Only labeled containers' } },
      label: {
        label: 'Label',
        hint: 'A label key, or key=value. Empty means pulsix.monitor=true. When all containers are checked, a container with this label’s key set to false is left out.',
      },
      socket: {
        label: 'Docker socket',
        hint: 'An absolute path or a Windows pipe. Empty means the platform default: /var/run/docker.sock, on Windows npipe:////./pipe/docker_engine.',
      },
    },
    temperature: {
      sensors: {
        label: 'Sensors (one per line, empty means every sensor)',
        hint: 'Sensor names as the host reports them; empty means every sensor.',
        placeholder: 'coretemp_core_0\nnvme_composite',
      },
    },
    smart: {
      devices: {
        label: 'Devices (one per line, empty means every disk)',
        hint: 'Device paths such as /dev/sda or /dev/nvme0; empty means every disk smartctl finds. Needs smartmontools on the host.',
        placeholder: '/dev/sda\n/dev/nvme0',
      },
    },
    raid: {
      arrays: {
        label: 'Arrays (one per line, empty means every array)',
        hint: 'Array names such as md0; empty means every array. Linux software RAID only.',
        placeholder: 'md0\nmd1',
      },
    },
    postgres: {
      secret: {
        label: 'Secret',
        hint: 'Name of a secret in agent.yaml on each host: it holds the server address and login. The value never leaves the host.',
        placeholder: 'db-main',
      },
      databases: {
        label: 'Databases (one per line, empty means every database)',
        hint: 'Databases whose size to report; empty means every database that accepts connections and is not a template.',
        placeholder: 'app\nanalytics',
      },
    },
    mysql: {
      secret: {
        label: 'Secret',
        hint: 'Name of a secret in agent.yaml on each host: it holds the server address and login. The value never leaves the host.',
        placeholder: 'db-main',
      },
    },
    redis: {
      secret: {
        label: 'Secret (optional)',
        hint: 'Name of a secret in agent.yaml on each host: it holds the server address and login. The value never leaves the host. Without one, the check reads 127.0.0.1:6379 without logging in.',
        placeholder: 'db-main',
      },
    },
    nginx: {
      url: { label: 'Status page URL', hint: 'The stub_status page of nginx, read by the agent on each host. Without a value the check reads http://127.0.0.1/nginx_status.' },
      secret: {
        label: 'Secret (optional)',
        hint: 'Name of a secret in agent.yaml on each host: it holds the HTTP basic-authentication login (basic_auth_user, basic_auth_password). The value never leaves the host. Without one, no login is sent.',
        placeholder: 'status-login',
      },
      verify_tls: { label: 'Verify the TLS certificate' },
    },
    apache: {
      url: { label: 'Status page URL', hint: 'The mod_status page of Apache httpd, read by the agent on each host; the agent adds ?auto when the URL has no such query key. Without a value the check reads http://127.0.0.1/server-status?auto.' },
      secret: {
        label: 'Secret (optional)',
        hint: 'Name of a secret in agent.yaml on each host: it holds the HTTP basic-authentication login (basic_auth_user, basic_auth_password). The value never leaves the host. Without one, no login is sent.',
        placeholder: 'status-login',
      },
      verify_tls: { label: 'Verify the TLS certificate' },
    },
    caddy: {
      url: { label: 'Metrics URL', hint: 'The Prometheus metrics page of Caddy, read by the agent on each host: the admin endpoint or a metrics handler on a site. The HTTP metrics must be switched on in Caddy. Without a value the check reads http://127.0.0.1:2019/metrics.' },
      secret: {
        label: 'Secret (optional)',
        hint: 'Name of a secret in agent.yaml on each host: it holds the HTTP basic-authentication login (basic_auth_user, basic_auth_password). The value never leaves the host. Without one, no login is sent.',
        placeholder: 'status-login',
      },
      verify_tls: { label: 'Verify the TLS certificate' },
    },
    phpfpm: {
      urls: {
        label: 'Status page URLs (one per line, one pool each)',
        hint: 'The status page (pm.status_path) of each PHP-FPM pool, served by the web server in front of it and read by the agent on each host; the agent adds ?json when the URL has no such query key. Each URL is one pool and names its readings. Empty means one pool at http://127.0.0.1/status.',
      },
      secret: {
        label: 'Secret (optional)',
        hint: 'Name of a secret in agent.yaml on each host: it holds the HTTP basic-authentication login (basic_auth_user, basic_auth_password), sent to every pool. The value never leaves the host. Without one, no login is sent.',
        placeholder: 'status-login',
      },
      verify_tls: { label: 'Verify the TLS certificate' },
    },
    icmp: {
      count: { label: 'Pings per run' },
      timeout_s: { label: 'Timeout (seconds)' },
    },
    tcp: {
      port: { label: 'Port' },
      send: { label: 'Send after connecting', hint: 'Use \\r\\n for a line break, \\t for a tab and \\\\ for a backslash.' },
      expect: { label: 'Expected in the response', hint: 'Looked for in the first 4 KiB the server sends.' },
      timeout_s: { label: 'Timeout (seconds)' },
    },
    http: {
      url: { label: 'URL', hint: '{address} is replaced with the host’s address.' },
      method: { label: 'Method' },
      body_contains: { label: 'Body contains', hint: 'Looked for in the first 1 MiB of the body.' },
      timeout_s: { label: 'Timeout (seconds)' },
      follow_redirects: { label: 'Follow redirects (up to 10)' },
      verify_tls: { label: 'Verify the TLS certificate' },
    },
    tls: {
      port: { label: 'Port' },
      server_name: { label: 'Server name (SNI)', hint: 'Empty means the host’s address.' },
      timeout_s: { label: 'Timeout (seconds)' },
    },
    dns: {
      name: { label: 'Name to resolve', hint: 'Empty means the host’s address; {address} is replaced with it.' },
      type: { label: 'Record type' },
      server: {
        label: 'DNS server',
        hint: 'An IP address with an optional port, an IPv6 address with a port in brackets. Empty means the Pulsix server’s own resolver.',
      },
      expect: { label: 'Expected answer', hint: 'One answer must equal this, ignoring case and a trailing dot.' },
      timeout_s: { label: 'Timeout (seconds)' },
    },
  } satisfies Readonly<Record<string, Readonly<Record<string, ParamText>>>>,
  /** Why a parameter field is refused before the round trip: its bounds,
   * and one message per format (internal/checkplugins formats.go). */
  paramErrors: {
    wholeRange: (min: number, max: number) => `must be a whole number between ${min} and ${max}`,
    tooMany: (max: number) => `must have at most ${max} entries`,
    tooLong: (max: number) => `every entry must be at most ${max} bytes`,
    formats: {
      notBlank: 'must not be blank',
      noControl: 'entries must not contain control characters',
      devicePath: 'every entry must start with /dev/, contain only letters, digits and . _ / , : + @ - and have no .. segment',
      secretName: 'must be 1 to 64 characters: letters, digits and . _ -',
      url: 'must be an http:// or https:// URL without a user name or password',
      statusURL: 'must be an http:// or https:// URL without a user name, password or #fragment, and without {address}',
      dnsName: 'must be a DNS name: letters, digits, hyphens and underscores between dots',
      server: 'must be an IP address, optionally with a port',
      dockerLabel: 'must be a label key (letters, digits, “.”, “_”, “/” and “-”, up to 255 characters), optionally followed by =value of up to 255 bytes',
      dockerSocket: 'must be an absolute path or npipe:////./pipe/ followed by a pipe name, up to 255 characters',
    },
  },
  groups: {
    noAccess: 'Monitoring groups are visible to members of this organization.',
    deleteConfirm: (name: string) => `Delete monitoring group ${name}? The check definitions bound to it are deleted with it.`,
    summary: (n: number) => `${n} groups · a higher priority wins when two groups bind the same check key to one host`,
    newGroup: 'New group',
    columns: {
      group: 'Group',
      selector: 'Selector',
      priority: 'Priority',
    },
    empty: 'No monitoring groups yet.',
    editTitle: (name: string) => `Edit ${name}`,
    newTitle: 'New monitoring group',
    selector: 'Selector',
    priority: 'Priority',
    typeSelector: 'Type a selector to see which hosts it matches.',
    resolveFailed: 'Could not resolve this selector',
    noMatch: 'No host matches this selector',
    matches: (n: number) => `Matches ${n} host${n === 1 ? '' : 's'}`,
    notMonitored: (name: string) => `${name} (not monitored)`,
  },
  rules: {
    noAccess: 'Rules are visible to members of this organization.',
    deleteConfirm: (name: string) => `Delete rule ${name}? Hosts stop being judged against it.`,
    deleteSeeded: ' It ships with every organization and will not come back on its own.',
    summary: (n: number) => `${n} rules · a narrower scope overrides a wider one on the same metric`,
    newRule: 'New rule',
    noMetricsTitle: 'No metrics to write a rule on yet',
    noMetricsBody: 'A rule judges a measured value, so New rule stays unavailable until an agent reports its first metric.',
    columns: {
      rule: 'Rule',
      severity: 'Severity',
      appliesTo: 'Applies to',
      for: 'For',
      state: 'State',
    },
    empty: 'No rules yet.',
    origin: {
      seed: 'seed',
      user: 'user',
    },
  },
  ruleDialog: {
    editTitle: (name: string) => `Edit ${name}`,
    newTitle: 'New rule',
    metric: 'Metric',
    severity: 'Severity',
    severityWarn: 'Warning',
    severityCrit: 'Critical',
    forSeconds: 'For (seconds)',
    seededNote:
      'This rule ships with every organization; its metric, scope and severity are fixed. Its threshold, clear condition, duration and enabled state are editable here.',
    toExpression: 'Switch to expression editor',
    toForm: 'Switch to form editor',
    confirmSwitch:
      'Switching to the expression editor is a one-way door from here. The way back to the form editor only stays open while the ' +
      'expression keeps exactly the shape the form produces — one comparison against this metric, no arithmetic, no time window, no "or". ' +
      'Once it leaves that shape, this rule can only be edited as an expression from now on. Continue?',
    confirmDropClear: (op: string, value: string) =>
      `This rule clears at "${op} ${value}". The expression editor has no syntax for a clear condition, so saving as an ` +
      'expression will remove it — the rule will then change state on every crossing of its threshold instead of holding ' +
      'until the clear condition is met. Remove the clear condition and continue?',
    expression: 'Expression',
    item: 'Item',
    itemCheck: 'Check',
    itemPlaceholder: '/var, sshd, …',
    form: {
      operator: 'Operator',
      value: 'Value',
      labels: 'Labels (one per line)',
      label: 'Label',
      clearCondition: 'Clear condition',
      noClearCondition: 'No clear condition',
      clearValue: 'Clear value',
    },
  },
  items: {
    noAccess: 'Item decisions are visible to members of this organization.',
    deleteConfirm: (decision: string, pattern: string) =>
      `Delete this ${decision} decision for pattern "${pattern}"? The item(s) it names go back to being judged normally.`,
    summary: (n: number) => `${n} decisions · a host decision overrules an organization one on the same check`,
    newDecision: 'New decision',
    noMetricsTitle: 'No checks to decide on yet',
    noMetricsBody: 'A decision names an item of a check, so New decision stays unavailable until an agent reports its first metric.',
    columns: {
      target: 'Target',
      check: 'Check',
      pattern: 'Pattern',
      verdict: 'Verdict',
      note: 'Note',
      created: 'Created',
    },
    verdict: {
      ignore: 'ignore',
      watch: 'watch',
    },
    matchKind: {
      exact: 'exact',
      glob: 'glob',
    },
    editDecision: (pattern: string) => `Edit decision for ${pattern}`,
    decisionActions: (pattern: string) => `Actions for decision ${pattern}`,
    empty: 'No item decisions yet.',
  },
  itemDialog: {
    newTitle: 'New item decision',
    appliesTo: 'Applies to',
    check: 'Check',
    checkPlaceholder: 'disk, disk-data, …',
    checkInvalid: 'A check key uses lowercase letters, digits and hyphens only (up to 63 characters), e.g. disk-data.',
    matchKind: 'Match kind',
    exact: 'Exact — one literal item name',
    glob: "Glob — a '*' pattern",
    pattern: 'Pattern',
    tooLong: (bytes: number, max: number) =>
      `This pattern is ${bytes} bytes long; the limit is ${max}. Item names outside the ASCII range cost more than one byte each, so a pattern can pass the field's own character count and still be too long.`,
    verdict: 'Verdict',
    verdictIgnore: 'Ignore — stop judging this item',
    verdictWatch: 'Watch — judge it even though a wider decision ignores it',
    verdictWatchStored: 'Watch (stored outside this editor — has no effect at organization scope)',
    watchNotOffered:
      'Watch is not offered at organization scope: watching is already what happens by default, so an organization-wide watch would change nothing. It is available once this decision is scoped to a single host, which is the overrule it exists for.',
    watchMismatch:
      'This decision is stored as an organization-wide watch, which has no effect and the server now refuses to save. Choose ignore, or scope it to a host to keep watch as the overrule it is meant to be.',
    notePlaceholder: 'Why does this item not need judging? The next operator reads this before switching it back on.',
    confirmBroad: (total: number) =>
      `This pattern currently matches ${total} items — more than the preview lists in full. Saving changes every one of them. Continue?`,
    typePattern: 'Type a pattern to see which items it would match.',
    resolveFailed: 'Could not resolve this pattern',
    noMatch: 'Matches no currently known item — it still applies the moment one appears',
    matches: (n: number) => `Matches ${n} item${n === 1 ? '' : 's'}`,
    showingFirst: (n: number) => `Showing the first ${n} below — narrow the pattern to see the rest.`,
  },
};
