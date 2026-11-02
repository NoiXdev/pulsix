import type { Format } from '../../i18n/format.ts';
import type { Messages } from '../../i18n/types.ts';
import type { AlertCloseReason, AlertEvent, AlertEventKind, AlertNotificationHold, AlertTimelineEntry, AlertTupleState } from '../../api/types.ts';
import type { Tone } from '../../ui/badge.tsx';

/**
 * The pure reading rules behind AlertTimeline, kept beside it rather than
 * inside it — the same split alertRow.ts already draws for AlertRow (its
 * own doc comment explains why: a file exporting a component may not also
 * export plain functions without breaking fast refresh). The one rule
 * Task 10's brief calls out by name — label `stale` differently from
 * `resolved` — is worth a table-driven test of its own rather than only
 * ever exercised through a render.
 */

export interface TimelineEntryLabel {
  label: string;
  tone: Tone;
}

const STATE_TONE: Record<AlertTupleState, Tone> = {
  ok: 'ok',
  warn: 'warn',
  crit: 'crit',
  unknown: 'unknown',
};

/** The catalogue key (t.alerts.timeline) of each plain event kind's label. */
const EVENT_LABEL_KEY = {
  opened: 'opened',
  severity_raised: 'severityRaised',
  acknowledged: 'acknowledged',
  ack_voided: 'ackVoided',
  snoozed: 'snoozed',
  unsnoozed: 'unsnoozed',
} as const satisfies Record<Exclude<AlertEventKind, 'closed' | 'escalated'>, string>;

const EVENT_TONE: Record<Exclude<AlertEventKind, 'closed' | 'escalated'>, Tone> = {
  opened: 'accent',
  severity_raised: 'warn',
  acknowledged: 'ok',
  ack_voided: 'warn',
  snoozed: 'unknown',
  unsnoozed: 'unknown',
};

/**
 * closedEventLabel is the one case design decision 5 exists for. A
 * `closed` alert_events row carries no payload of its own
 * (alert_handlers.go's own alertEventDTO doc comment: "nothing downstream
 * reads one yet"), so the two very different reasons an episode stops
 * being open — the tuple went healthy, or the thing being watched stopped
 * existing (its rule was deleted, its check was taken off the host, its
 * item was dropped, or the host was decommissioned) — can only be told
 * apart from the alert's OWN `closed_reason`, never from the event row
 * itself. An operator who cannot tell them apart reads a vanished subject
 * as a fixed problem (Task 10 brief, verbatim).
 */
function closedEventLabel(closedReason: AlertCloseReason | '', t: Messages): TimelineEntryLabel {
  if (closedReason === 'stale') {
    return { label: t.alerts.timeline.closedStale, tone: 'unknown' };
  }
  // 'resolved', or — defensively, should the store ever write a `closed`
  // event without closed_reason set in the same statement, which
  // migration 00049's own "set together or not at all" rules out — reads
  // as resolved rather than inventing a third, unlabelled state.
  return { label: t.alerts.timeline.closedResolved, tone: 'ok' };
}

/**
 * escalatedEventLabel is `escalated`'s own wording (v0.4d escalation
 * chains, Task 8's own carried-over gap): a ladder rung firing without
 * acknowledgement. Like `closedEventLabel`, this depends on the event's
 * own data rather than a static string — here `escalation_position`, the
 * step that fired (alerts.EscalatedPayload, decoded server-side into
 * `alertEventDTO`) — because "escalated" alone tells an operator a rung
 * fired but not which one, and a ladder several rungs deep needs that
 * number to read as a sequence rather than a repeated, undifferentiated
 * event. `escalation_position` is only absent for an event the server
 * could not decode a payload for (defensive: alert_handlers.go's own
 * doc comment) — reads as plain "Escalated" rather than a crash.
 */
function escalatedEventLabel(event: AlertEvent, t: Messages): TimelineEntryLabel {
  const position = event.escalation_position;
  return { label: position ? t.alerts.timeline.escalatedTo(position) : t.alerts.timeline.escalated, tone: 'warn' };
}

/**
 * timelineEntryLabel names one merged timeline row the way AlertTimeline
 * renders it: a state change reads as its own transition ("ok → warn"),
 * an event reads by its kind — except `closed`, whose wording depends on
 * the alert's own `closed_reason` (closedEventLabel's own doc comment),
 * and `escalated`, whose wording depends on the event's own payload
 * (escalatedEventLabel's own doc comment).
 */
export function timelineEntryLabel(entry: AlertTimelineEntry, closedReason: AlertCloseReason | '', t: Messages): TimelineEntryLabel {
  if (entry.kind === 'state_change' && entry.change) {
    const { from_state: from, to_state: to } = entry.change;
    return { label: `${t.alerts.state[from]} → ${t.alerts.state[to]}`, tone: STATE_TONE[to] };
  }
  if (entry.kind === 'event' && entry.event) {
    if (entry.event.kind === 'closed') return closedEventLabel(closedReason, t);
    if (entry.event.kind === 'escalated') return escalatedEventLabel(entry.event, t);
    return { label: t.alerts.timeline[EVENT_LABEL_KEY[entry.event.kind]], tone: EVENT_TONE[entry.event.kind] };
  }
  // Defensive: TimelineEntry's own contract (mergeTimeline, timeline.go)
  // is exactly one of event/change set, named by kind — unreachable
  // against a server that honours it, not a case worth a silent crash.
  return { label: t.alerts.timeline.unknownEvent, tone: 'unknown' };
}

/**
 * notificationHoldLabel answers "why did nothing arrive" in the one
 * sentence AlertTimeline shows above the timeline itself (v0.4c Task 11):
 * `null` renders nothing at all, for the common case nothing is held.
 * The two wordings deliberately name two different things an operator
 * would otherwise have to guess at — a shut delivery window ('deferred')
 * versus an open maintenance window ('suppressed') — never a shared
 * "notification delayed" that would leave which one to a follow-up
 * question. When an alert's events carry both, the server has already
 * resolved which one this prop is (internal/alerts.Detail.NotificationHold's
 * own doc comment: suppressed outranks deferred), so this function never
 * has to choose between them itself.
 */
export function notificationHoldLabel(hold: AlertNotificationHold | '', t: Messages): string | null {
  if (hold === 'deferred') return t.alerts.timeline.holdDeferred;
  if (hold === 'suppressed') return t.alerts.timeline.holdSuppressed;
  return null;
}

/** timelineEntryAt formats one entry's own timestamp for the timeline —
 * absolute, not relative (seenLabel's "5 min ago" shape): an episode's
 * history is read well after the fact, when a relative age has stopped
 * being useful and a wall-clock time has not. */
export function timelineEntryAt(at: string, fmt: Format): string {
  return fmt.dateTime(at, { dateStyle: 'medium', timeStyle: 'short' });
}
