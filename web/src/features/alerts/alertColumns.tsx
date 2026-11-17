import type { Messages } from '../../i18n/types.ts';
import type { DataListColumn } from '../../ui/data-list/index.ts';
import { AlertAckCell, AlertHostCell, AlertMuteCell, AlertOpenCell, AlertPeakCell, AlertTrackedCell } from './AlertCells.tsx';
import type { AlertRowAlert } from './alertRow.ts';

/**
 * alertColumns is every column the Alerts list offers (list id `alerts`),
 * and the component the whole page exists to get right (v0.4a design doc,
 * "GUI"): it shows three facts about one episode SEPARATELY, because three
 * different design decisions each need one of them and collapsing them into
 * a single "severity" column would silently merge all three:
 *
 *  - alert-open: whether the episode is still open. `closedAt` is the only
 *    thing this reads — never `wasFlapping` or `severity` — because
 *    decision 6 keeps an alert open on a tuple that currently reads ok
 *    while it is still flapping, and openness must stay true in that case
 *    regardless of what either of the other two facts say.
 *  - alert-peak: the episode's peak severity (decision 1 — one episode is
 *    one alert, and the peak is what an operator triages on, not whatever
 *    the tuple happens to read at the moment they looked).
 *  - alert-current ("Tracked" in the header, not "Current"): the alert's
 *    own tracked severity. This is NOT a live read of the tuple (nothing
 *    on the wire DTO carries that) — it is the last non-ok value a flap
 *    holds it at rather than following the tuple down to ok, which is
 *    exactly why "Current" would mislabel it. The `alert-flapping` marker
 *    beside this cell is the explanation of that gap: it renders whenever
 *    `wasFlapping` is true, not gated on `open`, since the flag only ever
 *    latches and never clears (store_sql.go's own SetAlertFlapping), so a
 *    resolved episode that flapped keeps the marker as part of its own
 *    history. Its text depends on `open` too: a closed row showing the
 *    open-tuple present tense ("is still flapping", "keeps the alert
 *    open") asserts something false right beside a `Closed` pill. Open
 *    reads "flapping" with the decision-6 explanation; closed reads
 *    "flapped" — a past-tense historical note with no claim about the
 *    alert's current state.
 *
 * Acknowledgement renders as a fourth, related fact: decision 2 voids an
 * ack once the current severity outruns `ackSeverity`, and isAckCovering
 * is the one place that comparison is made, so a worsened ack renders as
 * unacknowledged with a note that it was overtaken rather than silently
 * dropping back to "no one has looked at this".
 *
 * The cells are AlertCells.tsx's (the host page's AlertRow shares them).
 * The server sorts by opened (the default, newest first), host, peak,
 * tracked and acknowledged; Mute is not sortable (a mute is decided in Go).
 * The acknowledge, snooze and timeline actions are the row menu's
 * (alertActions.ts), not buttons in a cell. Presentational only.
 */
export function alertColumns(t: Messages): DataListColumn<AlertRowAlert>[] {
  const c = t.alerts.columns;
  return [
    { id: 'alert', label: c.alert, width: 'minmax(0, 1.4fr)', sortKey: 'host', required: true, defaultVisible: true, render: (alert) => <AlertHostCell alert={alert} /> },
    { id: 'open', label: c.open, width: '150px', sortKey: 'opened', defaultVisible: true, render: (alert) => <AlertOpenCell alert={alert} /> },
    { id: 'peak', label: c.peak, width: '90px', sortKey: 'peak', defaultVisible: true, render: (alert) => <AlertPeakCell alert={alert} /> },
    { id: 'tracked', label: c.tracked, width: '130px', sortKey: 'tracked', defaultVisible: true, render: (alert) => <AlertTrackedCell alert={alert} /> },
    { id: 'acknowledged', label: c.acknowledged, width: 'minmax(0, 1.1fr)', sortKey: 'acknowledged', defaultVisible: true, render: (alert) => <AlertAckCell alert={alert} /> },
    { id: 'mute', label: c.mute, width: 'minmax(0, 1.1fr)', defaultVisible: true, render: (alert) => <AlertMuteCell alert={alert} /> },
  ];
}
