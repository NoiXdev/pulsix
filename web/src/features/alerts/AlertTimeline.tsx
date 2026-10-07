import { Pill } from '../../ui/badge.tsx';
import { timelineEntryAt, timelineEntryLabel } from './alertTimeline.ts';
import type { AlertCloseReason, AlertTimelineEntry } from '../../api/types.ts';

export interface AlertTimelineProps {
  entries: AlertTimelineEntry[];
  /** The alert's own `closed_reason` — `''` while the alert is still open.
   * The only place a `closed` entry's wording can tell `resolved` and
   * `stale` apart (alertTimeline.ts's own closedEventLabel): the event
   * row itself carries no payload to read that off. */
  closedReason: AlertCloseReason | '';
}

/**
 * AlertTimeline is "the full story of one alert" (v0.4a design doc,
 * "GUI"): the alert's own alert_events interleaved with the
 * check_state_changes rows inside its lifetime, in the order the server
 * already merged them (internal/alerts/timeline.go's mergeTimeline,
 * surfaced through GET /alerts/{id}'s own `timeline` field). This
 * component renders that order verbatim and never re-sorts: the server's
 * own tie-break — a state change sorts before the event it caused, at the
 * very same instant (severity_raised/ack_voided sharing their state
 * change's own `at`) — is exactly the ordering a client-side re-sort keyed
 * on `at` alone cannot reproduce, because both entries already carry the
 * identical timestamp.
 *
 * A host-scope alert's timeline never carries a `state_change` entry at
 * all — check_host_states is a roll-up with no change log
 * (service.go's own doc comment on Get) — so this renders exactly the
 * same way either way: one unified list, one line per entry, labelled by
 * its own kind. There is deliberately no second "state history" region
 * that would sit empty for a host-scope alert and read as missing data;
 * there is nothing missing, there is simply nothing more to show.
 *
 * `closed` is the one event kind whose label depends on more than the
 * event itself: `closed_reason` ('resolved' vs 'stale') is read off the
 * alert, not the event row (see closedReason's own doc comment above) —
 * design decision 5's whole point is that those two must never collapse
 * into one undifferentiated "closed".
 */
export function AlertTimeline({ entries, closedReason }: AlertTimelineProps) {
  if (entries.length === 0) {
    return <p className="text-xs text-muted-foreground">No activity recorded for this alert yet.</p>;
  }

  return (
    <ol className="flex flex-col gap-1.5">
      {entries.map((entry, i) => {
        const { label, tone } = timelineEntryLabel(entry, closedReason);
        return (
          <li key={`${entry.kind}-${entry.at}-${i}`} className="flex items-center gap-2.5 text-xs" data-testid="timeline-entry">
            <span className="w-[150px] shrink-0 text-muted-foreground">{timelineEntryAt(entry.at)}</span>
            <Pill tone={tone}>{label}</Pill>
          </li>
        );
      })}
    </ol>
  );
}
