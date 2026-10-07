import type { Alert, AlertCloseReason, AlertMuteReason, AlertScope, AlertSeverity } from '../../api/types.ts';

/**
 * The pure reading rules behind AlertRow, kept beside it rather than inside
 * it — the same split fleetHost.ts and hostChecks.ts already draw: a file
 * exporting a component may not also export plain functions without
 * breaking fast refresh, and the three-fact rule this milestone's whole GUI
 * section exists to enforce (whether an alert is open, its peak severity,
 * and how its tuple reads right now) is worth a table-driven test of its
 * own rather than only ever exercised through a render.
 */

/** AlertRowAlert is AlertRow's own prop shape: Alert (the wire DTO),
 * flattened to camelCase and with `closedAt`/`ackAt`/`snoozedUntil`/
 * `mutedUntil` normalised to `null` (never `undefined`) so a row built by
 * hand in a test does not have to spell out every optional field the API
 * happens to omit. `severity` is the alert's own current severity — never
 * "ok" (the alerts table's own CHECK) — not a live read of the tuple: a
 * flapping alert can be open at `severity: "warn"` while the tuple itself
 * currently reads ok (design decision 6); nothing on the wire DTO carries
 * that live tuple reading at all; v0.4a's row does not either. */
export interface AlertRowAlert {
  id: string;
  hostId: string;
  scope: AlertScope;
  checkKey: string;
  item: string;
  metric: string;

  openedAt: string;
  severity: AlertSeverity;
  peakSeverity: AlertSeverity;

  closedAt: string | null;
  closedReason: AlertCloseReason | '';

  ackAt: string | null;
  ackBy: string;
  ackSeverity: AlertSeverity | '';
  ackNote: string;

  snoozedUntil: string | null;
  snoozeUntilChange: boolean;

  wasFlapping: boolean;

  muted: boolean;
  muteReason: AlertMuteReason | '';
  mutedUntil: string | null;
}

/** alertToRowAlert flattens one Alert (the wire DTO) into the camelCase
 * shape AlertRow reads, normalising every optional field the server omits
 * with `omitempty` to an explicit empty string / null rather than
 * `undefined` — one shape for "a decision has no value here" instead of two. */
export function alertToRowAlert(a: Alert): AlertRowAlert {
  return {
    id: a.id,
    hostId: a.host_id,
    scope: a.scope,
    checkKey: a.check_key ?? '',
    item: a.item ?? '',
    metric: a.metric ?? '',
    openedAt: a.opened_at,
    severity: a.severity,
    peakSeverity: a.peak_severity,
    closedAt: a.closed_at ?? null,
    closedReason: a.closed_reason ?? '',
    ackAt: a.ack_at ?? null,
    ackBy: a.ack_by ?? '',
    ackSeverity: a.ack_severity ?? '',
    ackNote: a.ack_note ?? '',
    snoozedUntil: a.snoozed_until ?? null,
    snoozeUntilChange: a.snooze_until_change,
    wasFlapping: a.was_flapping,
    muted: a.muted,
    muteReason: a.mute_reason ?? '',
    mutedUntil: a.muted_until ?? null,
  };
}

/** severityRank orders a severity the same way the server's own
 * rules.StateRank does (internal/rules/states.go): unknown ranks below
 * warn deliberately, so one dying agent cannot read as "better" than a
 * known warn and cannot read as worse than a known crit either. Restated
 * here rather than imported because there is nothing to import from — the
 * SPA has no generated client for internal/rules — but the three literal
 * ranks below must never drift from that switch, the same risk
 * store_sql.go's own raiseAlertSeverityRank comment names for the Go side. */
export function severityRank(s: AlertSeverity | 'ok' | ''): number {
  switch (s) {
    case 'crit':
      return 3;
    case 'warn':
      return 2;
    case 'unknown':
      return 1;
    default:
      return 0;
  }
}

/** isAckCovering reports whether alert's acknowledgement still covers its
 * current severity (design decision 2): a worsening severity voids an
 * acknowledgement, an improvement does not. The comparison is against
 * `severity` (the alert's own current tracked severity), never
 * `peakSeverity` — the peak only ever rises, so comparing against it would
 * make every ack of a still-climbing alert read as voided the instant it
 * was given, which is backwards: Reconcile's own advance() voids an ack by
 * comparing the alert's *new* severity to ackSeverity, not the peak. */
export function isAckCovering(a: AlertRowAlert): boolean {
  if (!a.ackAt || a.ackSeverity === '') return false;
  return severityRank(a.severity) <= severityRank(a.ackSeverity);
}

/** subjectLabel names what one alert is about: a host-scope alert (decision
 * 4, the roll-up) carries no check/item/metric at all — the alerts table's
 * own CHECK forbids it — so it reads as "Host" rather than an empty
 * string; a tuple-scope alert names its check, then its item and metric
 * when they are not empty (a whole-host metric has no item name, v0.3b-3's
 * own "items with an empty name are not rows at all"). */
export function subjectLabel(a: AlertRowAlert): string {
  if (a.scope === 'host') return 'Host';
  return [a.checkKey, a.item, a.metric].filter((p) => p !== '').join(' · ');
}

/** muteLabel explains why an alert is currently muted, and until when — or
 * null when it is not muted at all. A snooze with no `mutedUntil` is the
 * unbounded "until the alert's severity next changes" shape (Snooze's own
 * "never both, never neither"); maintenance and silence windows always
 * carry an end, by construction of the window they come from. */
export function muteLabel(a: AlertRowAlert): string | null {
  if (!a.muted) return null;
  const reason = a.muteReason === 'silence' ? 'silenced' : a.muteReason === 'maintenance' ? 'in maintenance' : 'snoozed';
  if (!a.mutedUntil) return `Muted — ${reason} until the state changes`;
  return `Muted — ${reason} until ${new Date(a.mutedUntil).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })}`;
}
