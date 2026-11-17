import { useT } from '../../i18n/useT.ts';
import { Button } from '../../ui/button.tsx';
import { AlertAckCell, AlertHostCell, AlertMuteCell, AlertOpenCell, AlertPeakCell, AlertTrackedCell } from './AlertCells.tsx';
import { isAckCovering, type AlertRowAlert } from './alertRow.ts';

/** The grid this row renders into. */
export const ALERTS_COLUMNS = '70px minmax(0, 1.2fr) 70px 90px minmax(0, 1.3fr) minmax(0, 1.1fr) 160px';

export interface AlertRowProps {
  alert: AlertRowAlert;
  /** `alerts.handle` — without it this row offers no action, only the read. */
  canWrite?: boolean;
  onAcknowledge?: (alert: AlertRowAlert) => void;
  onUnacknowledge?: (alert: AlertRowAlert) => void;
  onSnooze?: (alert: AlertRowAlert) => void;
  onUnsnooze?: (alert: AlertRowAlert) => void;
}

/**
 * AlertRow is the list's own unit, and the component the whole page exists
 * to get right (v0.4a design doc, "GUI"): it shows three facts about one
 * episode SEPARATELY, because three different design decisions each need
 * one of them and collapsing them into a single "severity" column would
 * silently merge all three:
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
 *    exactly why "Current" would mislabel it. Review finding (Important):
 *    that gap used to be silent — Open / peak crit / tracked warn, with no
 *    explanation — because `wasFlapping` was mapped into the row model and
 *    never rendered. The `alert-flapping` marker beside this cell is that
 *    explanation: it is the one wire field that actually says "flapped",
 *    so it renders here, next to the value it qualifies, whenever
 *    `wasFlapping` is true — not gated on `open`, since the flag only ever
 *    latches and never clears (store_sql.go's own SetAlertFlapping), so a
 *    resolved episode that flapped keeps the marker as part of its own
 *    history. A SECOND review finding (also Important, caught only once a
 *    closed-and-flapped fixture existed to test it) is why the marker's
 *    own text depends on `open` too: a closed row showing the open-tuple
 *    present tense ("is still flapping", "keeps the alert open") asserts
 *    something false right beside a `Closed` pill. Open reads "flapping"
 *    with the decision-6 explanation; closed reads "flapped" — a past-tense
 *    historical note with no claim about the alert's current state.
 *
 * Acknowledgement renders as a fourth, related fact: decision 2 voids an
 * ack once the current severity outruns `ackSeverity`, and isAckCovering
 * is the one place that comparison is made, so a worsened ack renders as
 * unacknowledged with a note that it was overtaken rather than silently
 * dropping back to "no one has looked at this".
 *
 * The cells are AlertCells.tsx's, shared with the Alerts page's list
 * columns (alertColumns.tsx), so the host card and the list read an alert
 * the same way.
 *
 * Presentational only — no data fetching of its own (CLAUDE.md's own
 * layering), which is also why every action is a callback rather than a
 * mutation hook: the caller (AlertsPage) owns the dialogs and the API
 * calls, so this component renders the same way in a unit test as it does
 * wired to the real page.
 */
export function AlertRow({ alert, canWrite = false, onAcknowledge, onUnacknowledge, onSnooze, onUnsnooze }: AlertRowProps) {
  const t = useT();
  const open = alert.closedAt === null;
  const covering = isAckCovering(alert);

  return (
    <div className="grid items-center gap-3 border-b border-border px-4 py-2.5 text-[13px] last:border-b-0" style={{ gridTemplateColumns: ALERTS_COLUMNS }}>
      <AlertOpenCell alert={alert} />
      <AlertHostCell alert={alert} />
      <AlertPeakCell alert={alert} />
      <AlertTrackedCell alert={alert} />
      <AlertAckCell alert={alert} />
      <AlertMuteCell alert={alert} />
      <span className="flex justify-end gap-1">
        {canWrite && open && (
          <>
            {covering ? (
              <Button type="button" variant="ghost" aria-label={t.alerts.row.unacknowledge} onClick={() => onUnacknowledge?.(alert)}>
                {t.alerts.row.unacknowledge}
              </Button>
            ) : (
              <Button type="button" variant="ghost" aria-label={t.alerts.row.acknowledge} onClick={() => onAcknowledge?.(alert)}>
                {t.alerts.row.acknowledge}
              </Button>
            )}
            {alert.muted && alert.muteReason === 'snooze' ? (
              <Button type="button" variant="ghost" aria-label={t.alerts.row.unsnooze} onClick={() => onUnsnooze?.(alert)}>
                {t.alerts.row.unsnooze}
              </Button>
            ) : (
              <Button type="button" variant="ghost" aria-label={t.alerts.row.snooze} onClick={() => onSnooze?.(alert)}>
                {t.alerts.row.snooze}
              </Button>
            )}
          </>
        )}
      </span>
    </div>
  );
}
