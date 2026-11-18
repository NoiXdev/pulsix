/**
 * The Alerts page, the host page's alerts card and the dialogs to
 * acknowledge and snooze. Host names, check keys, items and metrics are
 * data and never keys. States, severities and close reasons the API sends
 * are mapped to a label.
 */
export const enAlerts = {
  title: 'Alerts',
  noAccess: 'Alerts are visible to members of this organization.',
  empty: 'No alerts match this view.',
  showTimeline: 'Show timeline',
  hideTimeline: 'Hide timeline',
  timelineFor: (host: string) => `Timeline for ${host}`,
  timelineReadFailed: 'Could not read this alert’s timeline.',
  unacknowledgeFailed: 'Unacknowledging this alert failed',
  unsnoozeFailed: 'Unsnoozing this alert failed',
  /** The table's accessible name, and the total in its footer. */
  list: {
    label: 'Alerts',
    count: (n: number) => (n === 1 ? '1 alert' : `${n} alerts`),
    /** A row's name for its "…" button: the host, then what it is about. */
    rowLabel: (host: string, subject: string) => (subject === '' ? host : `${host}, ${subject}`),
  },
  columns: {
    alert: 'Alert',
    open: 'Open',
    peak: 'Peak',
    tracked: 'Tracked',
    acknowledged: 'Acknowledged',
    mute: 'Mute',
  },
  severity: {
    warn: 'warn',
    crit: 'crit',
    unknown: 'unknown',
  },
  state: {
    ok: 'ok',
    warn: 'warn',
    crit: 'crit',
    unknown: 'unknown',
  },
  /** Why an alert fired (backlog 3.13b): a failed web assertion's
   * expression and the value it saw. */
  detail: {
    saw: (expression: string, actual: string) => `${expression} — saw ${actual}`,
    sawOnly: (actual: string) => `saw ${actual}`,
  },
  closeReason: {
    resolved: 'resolved',
    stale: 'stale',
    forgotten: 'forgotten',
  },
  row: {
    open: 'Open',
    closed: (reason: string) => (reason === '' ? 'Closed' : `Closed · ${reason}`),
    subjectHost: 'Host',
    // Backlog 3.57: the absent warning (a host-scope warn alert) on a
    // client not seen for days.
    subjectNotSeenSince: (when: string) => `Not seen since ${when}`,
    subjectNotSeen: 'Not seen',
    flapping: 'flapping',
    flapped: 'flapped',
    flappingTitle:
      'This tuple is still flapping between ok and a worse state — decision 6 keeps the alert open until it settles, even on a tick where it currently reads ok',
    flappedTitle:
      'This episode flapped between ok and a worse state while it was open — recorded here as part of its history, not a claim about its current state',
    acknowledged: 'Acknowledged',
    unacknowledged: 'Unacknowledged',
    overtaken: 'acknowledgement was overtaken by a worse severity',
    acknowledge: 'Acknowledge',
    unacknowledge: 'Unacknowledge',
    snooze: 'Snooze',
    unsnooze: 'Unsnooze',
  },
  mute: {
    silence: (until: string) => `Muted — silenced until ${until}`,
    maintenance: (until: string) => `Muted — in maintenance until ${until}`,
    snooze: (until: string) => `Muted — snoozed until ${until}`,
    silenceOpen: 'Muted — silenced until the state changes',
    maintenanceOpen: 'Muted — in maintenance until the state changes',
    snoozeOpen: 'Muted — snoozed until the state changes',
  },
  timeline: {
    empty: 'No activity recorded for this alert yet.',
    opened: 'Alert opened',
    severityRaised: 'Severity raised',
    acknowledged: 'Acknowledged',
    ackVoided: 'Acknowledgement voided by a worse severity',
    snoozed: 'Snoozed',
    unsnoozed: 'Snooze cancelled',
    closedStale: 'Closed — stale (its subject no longer exists)',
    closedResolved: 'Closed — resolved (the tuple is healthy again)',
    closedForgotten: 'Closed — forgotten (the item stopped reporting and was forgotten)',
    escalatedTo: (position: number) => `Escalated to step ${position}`,
    escalated: 'Escalated',
    unknownEvent: 'Unknown event',
    holdDeferred: 'Notification held until the delivery window opens.',
    holdSuppressed: 'Notification held until the maintenance window ends.',
  },
  filters: {
    all: 'All',
    any: 'Any',
    state: 'State',
    open: 'Open',
    closed: 'Closed',
    severity: 'Severity',
    warn: 'Warn',
    crit: 'Crit',
    unknown: 'Unknown',
    scope: 'Scope',
    tuple: 'Tuple',
    host: 'Host',
    acknowledged: 'Acknowledged',
    unacknowledged: 'Unacknowledged',
    muted: 'Muted',
    notMuted: 'Not muted',
  },
  acknowledgeDialog: {
    title: 'Acknowledge alert',
    note: 'Note',
    placeholder: 'Optional — what should the next operator know?',
    failed: 'Acknowledging this alert failed',
  },
  snoozeDialog: {
    title: 'Snooze alert',
    until: 'Until',
    untilChange: "Until the alert's severity next changes",
    failed: 'Snoozing this alert failed',
  },
  hostCard: {
    fullHistory: 'Full history, including closed alerts',
    readFailed: 'Could not read this host’s alerts.',
    none: 'No open alerts for this host right now — it is healthy.',
  },
};
