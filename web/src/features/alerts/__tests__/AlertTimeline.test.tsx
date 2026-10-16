import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { AlertTimeline } from '../AlertTimeline.tsx';
import { timelineEntryLabel } from '../alertTimeline.ts';
import type { AlertTimelineEntry } from '../../../api/types.ts';

const opened = (at: string): AlertTimelineEntry => ({
  at,
  kind: 'event',
  event: { id: 'e-opened', at, kind: 'opened' },
});

const closed = (at: string): AlertTimelineEntry => ({
  at,
  kind: 'event',
  event: { id: 'e-closed', at, kind: 'closed' },
});

const change = (at: string, from: 'ok' | 'warn' | 'crit' | 'unknown', to: 'ok' | 'warn' | 'crit' | 'unknown'): AlertTimelineEntry => ({
  at,
  kind: 'state_change',
  change: { at, from_state: from, to_state: to },
});

describe('AlertTimeline', () => {
  it('renders entries in exactly the order the server already merged them, never re-sorting by time', () => {
    // The server's own tie-break (mergeTimeline, internal/alerts/timeline.go)
    // sorts a state change before the event it caused at the very same
    // instant — these three entries share one `at`, in an order that is
    // deliberately NOT what a naive re-sort would produce if it broke ties
    // by, say, array position reversed or by kind alphabetically. A
    // component that re-sorted client-side by `at` alone would keep this
    // exact order too (same timestamp), so the real risk this test is
    // written against is a re-sort that uses a secondary key the server
    // does NOT use (e.g. alphabetical on kind, which would put "event"
    // before "state_change") — asserting the DOM order matches the input
    // order verbatim catches that, and also catches any re-sort at all
    // once a second, later-reordering fixture is added.
    const SAME_INSTANT = '2026-10-05T10:00:00Z';
    const entries: AlertTimelineEntry[] = [
      change(SAME_INSTANT, 'warn', 'crit'),
      { at: SAME_INSTANT, kind: 'event', event: { id: 'e1', at: SAME_INSTANT, kind: 'severity_raised' } },
      opened('2026-10-05T09:00:00Z'),
    ];

    render(<AlertTimeline entries={entries} closedReason="" />);

    const rows = screen.getAllByTestId('timeline-entry');
    expect(rows).toHaveLength(3);
    // Row 0 is the state change (warn -> crit), row 1 is the event that
    // shares its instant, row 2 is the chronologically earlier "opened"
    // entry the fixture deliberately placed last in the input array — if
    // this component sorted by `at`, "opened" (09:00) would render first,
    // not last.
    expect(rows[0]).toHaveTextContent(/warn/i);
    expect(rows[0]).toHaveTextContent(/crit/i);
    expect(rows[1]).toHaveTextContent(/severity raised/i);
    expect(rows[2]).toHaveTextContent(/alert opened/i);
  });

  it('labels a stale closing differently from a resolved one', () => {
    const { unmount } = render(<AlertTimeline entries={[closed('2026-10-05T12:00:00Z')]} closedReason="resolved" />);
    const resolvedRow = screen.getByTestId('timeline-entry');
    expect(resolvedRow).toHaveTextContent(/resolved/i);
    expect(resolvedRow).not.toHaveTextContent(/stale/i);
    unmount();

    render(<AlertTimeline entries={[closed('2026-10-05T12:00:00Z')]} closedReason="stale" />);
    const staleRow = screen.getByTestId('timeline-entry');
    expect(staleRow).toHaveTextContent(/stale/i);
    expect(staleRow).not.toHaveTextContent(/resolved/i);
  });

  it('renders a host-scope alert\'s events-only timeline cleanly: no error, no empty-looking gap', () => {
    // check_host_states has no change log, so a host-scope alert's
    // timeline never carries a state_change entry — only events. The bug
    // this guards against is a component built around two separate
    // regions ("state changes" / "events"), where the state-change region
    // renders as a visibly empty block an operator would read as missing
    // data rather than as "there is nothing more to show".
    render(<AlertTimeline entries={[opened('2026-10-05T09:00:00Z'), closed('2026-10-05T12:00:00Z')]} closedReason="resolved" />);

    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    const rows = screen.getAllByTestId('timeline-entry');
    expect(rows).toHaveLength(2);
    expect(screen.queryByText(/no state change/i)).not.toBeInTheDocument();
  });

  it('renders an explicit "nothing yet" message for a genuinely empty timeline, not a blank region', () => {
    render(<AlertTimeline entries={[]} closedReason="" />);
    expect(screen.queryByTestId('timeline-entry')).not.toBeInTheDocument();
    expect(screen.getByText(/no activity/i)).toBeInTheDocument();
  });
});

describe('AlertTimeline notification hold', () => {
  // v0.4c Task 11 ("Why nothing arrived"): the ledger holds a fact no GUI
  // surfaced before — notification_hold on the alert detail. These three
  // cases are the whole contract: deferred reads as the delivery window,
  // suppressed reads as the maintenance window (the follower's own
  // precedence when both are true at once — StateDeferred's own doc
  // comment, internal/alertroutes/ledger_sql.go), and nothing is shown at
  // all once the hold clears.
  it('says a notification is held for a shut delivery window', () => {
    render(<AlertTimeline entries={[]} closedReason="" notificationHold="deferred" />);
    expect(screen.getByText(/held until the delivery window/i)).toBeInTheDocument();
  });

  it('says a notification is held for an open maintenance window', () => {
    render(<AlertTimeline entries={[]} closedReason="" notificationHold="suppressed" />);
    expect(screen.getByText(/held until the maintenance window/i)).toBeInTheDocument();
  });

  it('says nothing when no notification is held', () => {
    render(<AlertTimeline entries={[]} closedReason="" notificationHold="" />);
    expect(screen.queryByText(/held until/i)).not.toBeInTheDocument();
  });

  it('shows the hold line above a non-empty timeline too', () => {
    render(<AlertTimeline entries={[opened('2026-10-05T09:00:00Z')]} closedReason="" notificationHold="deferred" />);
    expect(screen.getByText(/held until the delivery window/i)).toBeInTheDocument();
    expect(screen.getByTestId('timeline-entry')).toBeInTheDocument();
  });
});

describe('timelineEntryLabel', () => {
  it('names a state change by its own transition', () => {
    const entry = change('t', 'ok', 'warn');
    expect(timelineEntryLabel(entry, '').label).toMatch(/ok/i);
    expect(timelineEntryLabel(entry, '').label).toMatch(/warn/i);
  });

  it('names "opened" plainly', () => {
    expect(timelineEntryLabel(opened('t'), '').label).toMatch(/opened/i);
  });

  it('names ack_voided as a voided acknowledgement', () => {
    const entry: AlertTimelineEntry = { at: 't', kind: 'event', event: { id: 'e', at: 't', kind: 'ack_voided' } };
    expect(timelineEntryLabel(entry, '').label).toMatch(/voided/i);
  });

  it('a closed entry reads "stale" when the alert closed stale, never mentioning "resolved"', () => {
    const label = timelineEntryLabel(closed('t'), 'stale').label;
    expect(label).toMatch(/stale/i);
    expect(label).not.toMatch(/resolved/i);
  });

  it('a closed entry reads "resolved" when the alert closed resolved, never mentioning "stale"', () => {
    const label = timelineEntryLabel(closed('t'), 'resolved').label;
    expect(label).toMatch(/resolved/i);
    expect(label).not.toMatch(/stale/i);
  });

  // v0.4d escalation chains, Task 8's own carried-over gap: an 'escalated'
  // event used to read as "undefined" because EVENT_LABEL had no entry for
  // it. It needs the step number, not just the word "escalated" — a ladder
  // several rungs deep is unreadable if every rung says the same thing.
  it('names an escalated event by the ladder step that fired', () => {
    const entry: AlertTimelineEntry = { at: 't', kind: 'event', event: { id: 'e', at: 't', kind: 'escalated', escalation_position: 2 } };
    expect(timelineEntryLabel(entry, '').label).toMatch(/step 2/i);
  });

  it('still names an escalated event plainly if its step number could not be decoded', () => {
    const entry: AlertTimelineEntry = { at: 't', kind: 'event', event: { id: 'e', at: 't', kind: 'escalated' } };
    expect(timelineEntryLabel(entry, '').label).toMatch(/escalated/i);
  });
});
