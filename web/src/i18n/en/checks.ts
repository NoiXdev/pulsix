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

/** The vantage-point parameters every synthetic plugin takes (backlog
 * 3.13a); spread into each synthetic plugin's texts below. */
const vantageParamTexts = {
  vantage_manager: {
    label: 'Probe from the Pulsix server',
    hint: 'The Pulsix server probes the target itself. Switch it off to probe only from the agents of the groups below.',
  },
  vantage_groups: {
    label: 'Probe from the agents of these groups',
    hint: 'Monitoring groups whose agents also probe the target, at most 8. Checks with a secret are probed only from the Pulsix server.',
  },
  agents_per_group: { label: 'Agents per group', hint: 'How many agents of each group probe the target, 1 to 10.' },
  fail_policy: {
    label: 'The check is down when',
    hint: 'How the results of the probing points combine into one result.',
    options: { any: 'any probing point sees it down', majority: 'most probing points see it down', all: 'every probing point sees it down' },
  },
} satisfies Readonly<Record<string, ParamText>>;

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
    label: 'Check definitions',
    count: (n: number) => (n === 1 ? '1 check' : `${n} checks`),
    search: 'Search checks',
    filterPlaceholder: 'Filter checks…',
    noMatch: 'No check definitions match the filters.',
    filters: { plugin: 'Filter by plugin', allPlugins: 'All plugins', state: 'Filter by state', anyState: 'Any state' },
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
      snmp: 'SNMP device',
      docker: 'Docker containers',
      temperature: 'Temperature',
      smart: 'SMART disk health',
      raid: 'RAID arrays',
      zfs: 'ZFS pools',
      postgres: 'PostgreSQL',
      mysql: 'MySQL / MariaDB',
      redis: 'Redis / Valkey',
      nginx: 'nginx',
      apache: 'Apache httpd',
      caddy: 'Caddy',
      phpfpm: 'PHP-FPM',
      script: 'Custom script',
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
    /** Why an attempt failed: the labels of the <plugin>.failure metric
     * (internal/probe failure.go, backlog 3.13b), in its order. */
    failures: {
      none: 'no failure',
      dns: 'name did not resolve',
      refused: 'connection refused',
      timeout: 'timed out',
      unreachable: 'network or host unreachable',
      reset: 'connection reset or closed early',
      tls: 'TLS handshake failed',
      certificate: 'certificate did not verify',
      protocol: 'response was not valid HTTP',
      redirects: 'too many redirects',
      nxdomain: 'name does not exist',
      servfail: 'DNS server answered with an error',
      auth: 'credentials refused',
      auth_or_lost: 'credentials refused, or the requests were lost',
      error: 'other failure',
    },
  },
  /** The http check's assertion editor (backlog 3.13b): one row per
   * entry of `assertions`, or the raw list behind the text toggle. The
   * subjects and operators are the grammar's own words. */
  assertionEditor: {
    columns: { name: 'Name', subject: 'Subject', op: 'Operator', value: 'Value', severity: 'Severity' },
    name: (n: number) => `Name of assertion ${n}`,
    subject: (n: number) => `Subject of assertion ${n}`,
    arg: (n: number) => `Argument of assertion ${n}`,
    op: (n: number) => `Operator of assertion ${n}`,
    operand: (n: number) => `Value of assertion ${n}`,
    from: (n: number) => `Lowest value of assertion ${n}`,
    to: (n: number) => `Highest value of assertion ${n}`,
    severity: (n: number) => `Severity of assertion ${n}`,
    remove: (n: number) => `Remove assertion ${n}`,
    add: 'Add assertion',
    editText: 'Edit as text',
    editRows: 'Edit as rows',
    empty: 'No assertions: the check reports up or down only.',
    subjects: {
      status: 'status (status code)',
      response_ms: 'response_ms (response time, ms)',
      ttfb_ms: 'ttfb_ms (time to first byte, ms)',
      size: 'size (body bytes)',
      redirects: 'redirects (redirects followed)',
      cert_days: 'cert_days (days until the certificate expires)',
      cert_issuer: 'cert_issuer (certificate issuer)',
      body: 'body (response text)',
      header: 'header (a response header)',
      json: 'json (a JSON value)',
      html: 'html (text of an HTML element)',
      'html count': 'html count (number of HTML elements)',
      connect_ms: 'connect_ms (connect time, ms)',
      response: 'response (first line received)',
      resolve_ms: 'resolve_ms (lookup time, ms)',
      answer_count: 'answer_count (number of answers)',
      answers: 'answers (all answers, sorted)',
      answer: 'answer (one answer is present)',
      handshake_ms: 'handshake_ms (handshake time, ms)',
      cert_valid: 'cert_valid (1 when the chain verifies)',
      cert_subject: 'cert_subject (certificate subject)',
      cert_sans: 'cert_sans (names in the certificate)',
      version: 'version (TLS version, such as 1.3)',
      cipher: 'cipher (cipher suite)',
    },
    argPlaceholders: { header: 'Content-Type', json: '$.status', html: 'h1.title', answer: '192.0.2.1' },
    numberPlaceholder: '200',
    textPlaceholder: 'text',
    rowInvalid: 'Not a complete assertion yet: it needs a name (letters, digits and . _ -), an argument where the subject takes one and a value.',
    cannotParse: (line: number) => `Line ${line} is not an assertion. Correct it here before switching to rows.`,
    selectorHint:
      'html(…) takes a CSS selector from this subset: element names and *, #id, .class, [attr], [attr="v"], [attr~="v"], [attr^="v"], [attr$="v"], [attr*="v"], combined with a space (descendant), > (child), + (next sibling) or ~ (later sibling), several selectors separated by commas, and :first-child, :last-child, :only-child, :nth-child(…), :nth-last-child(…), :first-of-type, :last-of-type, :only-of-type, :nth-of-type(…), :nth-last-of-type(…), :empty, :root and :not(…). The nth forms take a number, odd, even or An+B such as 2n+1; :not takes selectors without combinators, separated by commas, and cannot be nested.',
  },
  /** An assertion's result (http.assertion) and the two words its value
   * seen (http.assertion_actual) can be instead of a value. */
  assertionResults: { pass: 'pass', warn: 'warn', crit: 'crit' },
  assertionActual: { absent: 'absent', hidden: '(hidden)' },
  /** Where a synthetic check is probed from (backlog 3.13a); the four
   * parameters' own labels are vantageParamTexts above. */
  vantage: {
    title: 'Probing points',
    chosenGroups: 'Chosen groups',
    addGroup: 'Add a group',
    secretOnlyServer: 'Checks with a secret are probed only from the Pulsix server.',
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
      resources: {
        label: 'Measure resource usage',
        hint: 'CPU, memory, network and disk throughput of each container, for at most 100 containers (running ones first, each by name). A stopped container reads 0.',
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
    zfs: {
      pools: {
        label: 'Pools (one per line, empty means every pool)',
        hint: 'Pool names such as tank; empty means every pool. Linux with ZFS only.',
        placeholder: 'tank\nbackup',
      },
      optional_pools: {
        label: 'Optional pools (one per line)',
        hint: 'Pools that may be exported, such as a backup pool on removable disks: while one is not imported it reads EXPORTED and nothing else is measured. A pool under Pools that is not imported reads MISSING. This list does not narrow the check: with Pools empty, every imported pool is still monitored.',
        placeholder: 'backup',
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
      schema_sizes: {
        label: 'Report the size of each schema',
        hint: 'Data and indexes per schema, system schemas left out. The login needs a privilege on the tables, such as SHOW VIEW, which reads no data. Can be slow on a server with very many tables.',
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
    script: {
      commands: {
        label: 'Commands (one per line)',
        hint: 'Names of commands defined under commands: in agent.yaml on each host; only the name is sent, the host decides what runs. A host that does not define a name reports it as unknown.',
        placeholder: 'backup_age\ndisk_root',
      },
      format: {
        label: 'Output format',
        options: { nagios: 'Nagios plugin (exit code and perfdata)', json: 'JSON (status, message, metrics)' },
      },
      timeout_seconds: { label: 'Timeout (seconds)', hint: 'A command still running then is stopped, with every process it started, and reported as unknown.' },
    },
    icmp: {
      count: { label: 'Pings per run' },
      timeout_s: { label: 'Timeout (seconds)' },
      ...vantageParamTexts,
    },
    tcp: {
      port: { label: 'Port' },
      send: { label: 'Send after connecting', hint: 'Use \\r\\n for a line break, \\t for a tab and \\\\ for a backslash.' },
      expect: { label: 'Expected in the response', hint: 'Looked for in the first 4 KiB the server sends.' },
      timeout_s: { label: 'Timeout (seconds)' },
      assertions: {
        label: 'Assertions (one per line, name=assertion)',
        hint: 'Each line states what a good connection is and reports pass, or warn or crit when it fails, under its name, with the value seen. A subject (connect_ms, response_ms or response, the first line the server sends), an operator and a value: numbers with < <= > >= == != or in a..b; text in double quotes with == != contains !contains matches !matches. Add @warn to report warn instead of crit.',
        placeholder: 'fast=connect_ms < 500',
      },
      ...vantageParamTexts,
    },
    http: {
      url: { label: 'URL', hint: '{address} is replaced with the host’s address.' },
      method: { label: 'Method' },
      secret: {
        label: 'Secret',
        hint: 'Optional. Name of a secret in the organization’s secrets (Settings → Secrets) holding the login: username and password, a token (sent as a bearer token), or header and value (one header of your choice). It is never sent on to another host when a redirect leads there; its values are never shown again and never leave the Pulsix server. A definition several organizations use reads each one’s own secret of that name.',
        placeholder: 'api-token',
      },
      body_contains: { label: 'Body contains', hint: 'Looked for in the first 1 MiB of the body.' },
      json_values: {
        label: 'JSON values (one per line, name=JSONPath)',
        hint: 'Each line reads one value out of a JSON response (up to 1 MiB) and reports it under its name: a number as a value, text or true/false as a label. The path must select exactly one value. Supported: names, indexes, *, one .. and filters comparing @ paths with values. Needs method GET.',
        placeholder: 'status=$.status',
      },
      assertions: {
        label: 'Assertions (one per line, name=assertion)',
        hint: 'Each line states what a good response is and reports pass, or warn or crit when it fails, under its name, with the value seen. A subject (status, response_ms, ttfb_ms, size, redirects, cert_days, cert_issuer, body, header("Name"), json("$.path"), html("selector") or html("selector") count), an operator and a value: numbers with < <= > >= == != or in a..b; text in double quotes with == != contains !contains matches !matches; header, json and html also with exists and !exists. Add @warn to report warn instead of crit. Body, json, html and size need method GET.',
        placeholder: 'ok=status in 200..299',
      },
      timeout_s: { label: 'Timeout (seconds)' },
      follow_redirects: { label: 'Follow redirects (up to 10)' },
      verify_tls: { label: 'Verify the TLS certificate' },
      ...vantageParamTexts,
    },
    tls: {
      port: { label: 'Port' },
      server_name: { label: 'Server name (SNI)', hint: 'Empty means the host’s address.' },
      timeout_s: { label: 'Timeout (seconds)' },
      assertions: {
        label: 'Assertions (one per line, name=assertion)',
        hint: 'Each line states what a good handshake is and reports pass, or warn or crit when it fails, under its name, with the value seen. A subject (handshake_ms, cert_valid, cert_days, cert_issuer, cert_subject, cert_sans, version or cipher), an operator and a value: numbers with < <= > >= == != or in a..b; text in double quotes with == != contains !contains matches !matches. Add @warn to report warn instead of crit.',
        placeholder: 'modern=version >= 1.2',
      },
      ...vantageParamTexts,
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
      assertions: {
        label: 'Assertions (one per line, name=assertion)',
        hint: 'Each line states what a good answer is and reports pass, or warn or crit when it fails, under its name, with the value seen. A subject (resolve_ms, answer_count, answers or answer("value")), an operator and a value: numbers with < <= > >= == != or in a..b; text in double quotes with == != contains !contains matches !matches; answer("value") only with exists or !exists. Add @warn to report warn instead of crit.',
        placeholder: 'ip=answer("192.0.2.1") exists',
      },
      ...vantageParamTexts,
    },
    snmp: {
      secret: {
        label: 'Secret',
        hint: 'Name of a secret in the organization’s secrets (Settings → Secrets), holding the community (v2c) or the SNMPv3 login; its values are never shown again and never leave the Pulsix server. A definition several organizations use reads each one’s own secret of that name.',
        placeholder: 'switch-core',
      },
      version: { label: 'SNMP version', options: { v2c: 'v2c (community)', v3: 'v3 (user name and passwords)' } },
      port: { label: 'Port' },
      timeout_seconds: { label: 'Timeout per request (seconds)' },
      oids: {
        label: 'Values (one per line, name=OID)',
        hint: 'Each line reads one value from the device and reports it under its name. Numeric OIDs only, such as 1.3.6.1.2.1.1.3.0.',
        placeholder: 'ups_load=1.3.6.1.2.1.33.1.4.4.1.5.1',
      },
      interfaces: { label: 'Watch the interfaces', hint: 'Status, traffic and errors of each interface the device lists, at most 128.' },
      ...vantageParamTexts,
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
      commandName: 'every entry must be a command name of 1 to 64 characters: letters, digits and . _ -',
      assertion: 'every entry must be name=assertion such as ok=status == 200: a name of 1 to 64 characters (letters, digits and . _ -), a subject, an operator and a value (text in double quotes), optionally followed by @warn',
      tcpAssertion: 'every entry must be name=assertion such as fast=connect_ms < 500: a name, a subject (connect_ms, response_ms or response), an operator and a value (text in double quotes), optionally followed by @warn',
      dnsAssertion: 'every entry must be name=assertion such as ip=answer("192.0.2.1") exists: a name, a subject (resolve_ms, answer_count, answers or answer("…")), an operator and a value (text in double quotes), optionally followed by @warn',
      tlsAssertion: 'every entry must be name=assertion such as modern=version >= 1.2: a name, a subject (handshake_ms, cert_valid, cert_days, cert_issuer, cert_subject, cert_sans, version or cipher), an operator and a value (text in double quotes), optionally followed by @warn',
      jsonValue: 'every entry must be name=JSONPath: a name of 1 to 64 characters (letters, digits and . _ -) and a path of at most 512 bytes in the supported JSONPath subset (see the docs), such as $.status',
      snmpOID: 'every entry must be name=OID: a name of 1 to 64 characters (letters, digits and . _ -) and a numeric OID such as 1.3.6.1.2.1.1.3.0 that starts with 0, 1 or 2 (after 0 or 1, the second number is at most 39)',
      url: 'must be an http:// or https:// URL without a user name or password',
      statusURL: 'must be an http:// or https:// URL without a user name, password or #fragment, and without {address}',
      dnsName: 'must be a DNS name: letters, digits, hyphens and underscores between dots',
      server: 'must be an IP address, optionally with a port',
      uuid: 'every entry must be a monitoring group’s id (a lower-case UUID)',
      dockerLabel: 'must be a label key (letters, digits, “.”, “_”, “/” and “-”, up to 255 characters), optionally followed by =value of up to 255 bytes',
      dockerSocket: 'must be an absolute path or npipe:////./pipe/ followed by a pipe name, up to 255 characters',
    },
  },
  groups: {
    noAccess: 'Monitoring groups are visible to members of this organization.',
    deleteConfirm: (name: string) => `Delete monitoring group ${name}? The check definitions bound to it are deleted with it.`,
    summary: (n: number) => `${n} groups · a higher priority wins when two groups bind the same check key to one host`,
    newGroup: 'New group',
    label: 'Monitoring groups',
    count: (n: number) => (n === 1 ? '1 monitoring group' : `${n} monitoring groups`),
    search: 'Search monitoring groups',
    filterPlaceholder: 'Filter groups…',
    noFilterMatch: 'No monitoring groups match the filter.',
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
    label: 'Rules',
    count: (n: number) => (n === 1 ? '1 rule' : `${n} rules`),
    search: 'Search rules',
    filterPlaceholder: 'Filter rules…',
    noMatch: 'No rules match the filters.',
    filters: { severity: 'Filter by severity', anySeverity: 'Any severity', state: 'Filter by state', anyState: 'Any state' },
    columns: {
      rule: 'Rule',
      metric: 'Metric',
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
    label: 'Item decisions',
    count: (n: number) => (n === 1 ? '1 decision' : `${n} decisions`),
    noMatch: 'No item decisions match the filters.',
    filters: { verdict: 'Filter by verdict', anyVerdict: 'Any verdict', check: 'Filter by check', allChecks: 'All checks' },
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
