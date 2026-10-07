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
 *  - alert-current: the alert's own current severity. Note this is NOT a
 *    live read of the tuple (nothing on the wire DTO carries that) — it is
 *    the alert's own tracked `severity`, which a flap holds at its last
 *    non-ok value rather than following the tuple down to ok.
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
  const open = alert.closedAt === null;
  const covering = isAckCovering(alert);
  const overtaken = alert.ackAt !== null && !covering;
  const mute = muteLabel(alert);

  return (
    <div className="grid items-center gap-3 border-b border-border px-4 py-2.5 last:border-b-0" style={{ gridTemplateColumns: ALERTS_COLUMNS }}>
      <span data-testid="alert-open">
        <Pill tone={open ? 'accent' : 'unknown'}>{open ? 'Open' : `Closed${alert.closedReason ? ` · ${alert.closedReason}` : ''}`}</Pill>
      </span>
      <span className="flex min-w-0 flex-col" title={alert.hostId} data-testid="alert-host">
        <span className="truncate text-[13px] font-medium">{alert.hostName}</span>
        <span className="truncate text-[11px] text-muted-foreground">{subjectLabel(alert)}</span>
      </span>
      <span data-testid="alert-peak">
        <Pill tone={SEVERITY_TONE[alert.peakSeverity] ?? 'unknown'}>{alert.peakSeverity}</Pill>
      </span>
      <span data-testid="alert-current">
        <Pill tone={SEVERITY_TONE[alert.severity] ?? 'unknown'}>{alert.severity}</Pill>
      </span>
      <span data-testid="alert-ack" className="flex min-w-0 flex-col">
        <span className="truncate text-[13px]">{covering ? 'Acknowledged' : 'Unacknowledged'}</span>
        {overtaken && (
          <span className="truncate text-[11px] text-muted-foreground">acknowledgement was overtaken by a worse severity</span>
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
              <Button type="button" variant="ghost" aria-label="Unacknowledge" onClick={() => onUnacknowledge?.(alert)}>
                Unacknowledge
              </Button>
            ) : (
              <Button type="button" variant="ghost" aria-label="Acknowledge" onClick={() => onAcknowledge?.(alert)}>
                Acknowledge
              </Button>
            )}
            {alert.muted && alert.muteReason === 'snooze' ? (
              <Button type="button" variant="ghost" aria-label="Unsnooze" onClick={() => onUnsnooze?.(alert)}>
                Unsnooze
              </Button>
            ) : (
              <Button type="button" variant="ghost" aria-label="Snooze" onClick={() => onSnooze?.(alert)}>
                Snooze
              </Button>
            )}
          </>
        )}
      </span>
    </div>
  );
}
