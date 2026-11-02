import { useT } from '../../i18n/useT.ts';
import { useFormat } from '../../i18n/format.ts';
import { Button } from '../../ui/button.tsx';
import { Pill, type Tone } from '../../ui/badge.tsx';
import { isAckCovering, muteLabel, subjectLabel, type AlertRowAlert } from './alertRow.ts';

/** The grid this row renders into — exported so AlertsPage's own
 * GridHeader lines up with it exactly, the same pairing FleetPage's own
 * COLUMNS constant gives FleetRow. */
export const ALERTS_COLUMNS = '70px minmax(0, 1.2fr) 70px 90px minmax(0, 1.3fr) minmax(0, 1.1fr) 160px';

const SEVERITY_TONE: Record<string, Tone> = { warn: 'warn', crit: 'crit', unknown: 'unknown' };

export interface AlertRowProps {
  alert: AlertRowAlert;
  /** `checks.write` — without it this row offers no action, only the read. */
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
 * Presentational only — no data fetching of its own (CLAUDE.md's own
 * layering), which is also why every action is a callback rather than a
 * mutation hook: the caller (AlertsPage) owns the dialogs and the API
 * calls, so this component renders the same way in a unit test as it does
 * wired to the real page.
 */
export function AlertRow({ alert, canWrite = false, onAcknowledge, onUnacknowledge, onSnooze, onUnsnooze }: AlertRowProps) {
  const t = useT();
  const fmt = useFormat();
  const open = alert.closedAt === null;
  const covering = isAckCovering(alert);
  const overtaken = alert.ackAt !== null && !covering;
  const mute = muteLabel(alert, t, fmt);

  return (
    <div className="grid items-center gap-3 border-b border-border px-4 py-2.5 last:border-b-0" style={{ gridTemplateColumns: ALERTS_COLUMNS }}>
      <span data-testid="alert-open">
        <Pill tone={open ? 'accent' : 'unknown'}>{open ? t.alerts.row.open : t.alerts.row.closed(alert.closedReason ? t.alerts.closeReason[alert.closedReason] : '')}</Pill>
      </span>
      <span className="flex min-w-0 flex-col" title={alert.hostId} data-testid="alert-host">
        <span className="truncate text-[13px] font-medium">{alert.hostName}</span>
        <span className="truncate text-[11px] text-muted-foreground">{subjectLabel(alert, t)}</span>
      </span>
      <span data-testid="alert-peak">
        <Pill tone={SEVERITY_TONE[alert.peakSeverity] ?? 'unknown'}>{t.alerts.severity[alert.peakSeverity]}</Pill>
      </span>
      <span data-testid="alert-current" className="flex items-center gap-1.5">
        <Pill tone={SEVERITY_TONE[alert.severity] ?? 'unknown'}>{t.alerts.severity[alert.severity]}</Pill>
        {alert.wasFlapping && (
          <span
            data-testid="alert-flapping"
            title={open ? t.alerts.row.flappingTitle : t.alerts.row.flappedTitle}
            className="text-[10px] font-semibold text-[var(--px-warn)]"
          >
            {open ? t.alerts.row.flapping : t.alerts.row.flapped}
          </span>
        )}
      </span>
      <span data-testid="alert-ack" className="flex min-w-0 flex-col">
        <span className="truncate text-[13px]">{covering ? t.alerts.row.acknowledged : t.alerts.row.unacknowledged}</span>
        {overtaken && (
          <span className="truncate text-[11px] text-muted-foreground">{t.alerts.row.overtaken}</span>
        )}
        {covering && alert.ackNote && <span className="truncate text-[11px] text-muted-foreground">{alert.ackNote}</span>}
      </span>
      {mute ? (
        <span data-testid="alert-mute" className="truncate text-[13px] text-muted-foreground">
          {mute}
        </span>
      ) : (
        <span />
      )}
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
