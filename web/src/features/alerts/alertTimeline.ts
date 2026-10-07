import type { AlertCloseReason, AlertEventKind, AlertTimelineEntry, AlertTupleState } from '../../api/types.ts';
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

const EVENT_LABEL: Record<Exclude<AlertEventKind, 'closed'>, string> = {
  opened: 'Alert opened',
  severity_raised: 'Severity raised',
  acknowledged: 'Acknowledged',
  ack_voided: 'Acknowledgement voided by a worse severity',
  snoozed: 'Snoozed',
  unsnoozed: 'Snooze cancelled',
};

const EVENT_TONE: Record<Exclude<AlertEventKind, 'closed'>, Tone> = {
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
function closedEventLabel(closedReason: AlertCloseReason | ''): TimelineEntryLabel {
  if (closedReason === 'stale') {
    return { label: 'Closed — stale (its subject no longer exists)', tone: 'unknown' };
  }
  // 'resolved', or — defensively, should the store ever write a `closed`
  // event without closed_reason set in the same statement, which
  // migration 00049's own "set together or not at all" rules out — reads
  // as resolved rather than inventing a third, unlabelled state.
  return { label: 'Closed — resolved (the tuple is healthy again)', tone: 'ok' };
}

/**
 * timelineEntryLabel names one merged timeline row the way AlertTimeline
 * renders it: a state change reads as its own transition ("ok → warn"),
 * an event reads by its kind — except `closed`, whose wording depends on
 * the alert's own `closed_reason` (closedEventLabel's own doc comment).
 */
export function timelineEntryLabel(entry: AlertTimelineEntry, closedReason: AlertCloseReason | ''): TimelineEntryLabel {
  if (entry.kind === 'state_change' && entry.change) {
    return { label: `${entry.change.from_state} → ${entry.change.to_state}`, tone: STATE_TONE[entry.change.to_state] };
  }
  if (entry.kind === 'event' && entry.event) {
    if (entry.event.kind === 'closed') return closedEventLabel(closedReason);
    return { label: EVENT_LABEL[entry.event.kind], tone: EVENT_TONE[entry.event.kind] };
  }
  // Defensive: TimelineEntry's own contract (mergeTimeline, timeline.go)
  // is exactly one of event/change set, named by kind — unreachable
  // against a server that honours it, not a case worth a silent crash.
  return { label: 'Unknown event', tone: 'unknown' };
}

/** timelineEntryAt formats one entry's own timestamp for the timeline —
 * absolute, not relative (seenLabel's "5 min ago" shape): an episode's
 * history is read well after the fact, when a relative age has stopped
 * being useful and a wall-clock time has not. */
export function timelineEntryAt(at: string): string {
  return new Date(at).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
}
