import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { Pill, type Tone } from '../../ui/badge.tsx';
import { CELL_MINOR } from '../../ui/data-list/index.ts';
import { closeReasonLabel, detailText, isAckCovering, muteLabel, subjectLabel, type AlertRowAlert } from './alertRow.ts';

/**
 * The cells of an alert, one component per fact, shared by the Alerts
 * list's columns (alertColumns.tsx) and the host page's AlertRow. They set
 * no text size of their own: the row they sit in does (secondary lines use
 * CELL_MINOR). See alertColumns.tsx for why open, peak and tracked are
 * three separate facts.
 */

const SEVERITY_TONE: Record<string, Tone> = { warn: 'warn', crit: 'crit', unknown: 'unknown' };

interface CellProps {
  alert: AlertRowAlert;
}

export function AlertHostCell({ alert }: CellProps) {
  const t = useT();
  const fmt = useFormat();
  const detail = detailText(alert.detail, t);
  return (
    <span className="flex min-w-0 flex-col" title={alert.hostId} data-testid="alert-host">
      <span className="truncate font-medium">{alert.hostName}</span>
      <span className={`truncate text-muted-foreground ${CELL_MINOR}`}>{subjectLabel(alert, t, fmt)}</span>
      {/* Backlog 3.13b: why it fired, under what it is about. */}
      {detail && (
        <span data-testid="alert-detail" className={`truncate text-foreground ${CELL_MINOR}`} title={detail}>
          {detail}
        </span>
      )}
    </span>
  );
}

export function AlertOpenCell({ alert }: CellProps) {
  const t = useT();
  const open = alert.closedAt === null;
  return (
    <span data-testid="alert-open">
      <Pill tone={open ? 'accent' : 'unknown'}>{open ? t.alerts.row.open : t.alerts.row.closed(closeReasonLabel(alert.closedReason, t))}</Pill>
    </span>
  );
}

export function AlertPeakCell({ alert }: CellProps) {
  const t = useT();
  return (
    <span data-testid="alert-peak">
      <Pill tone={SEVERITY_TONE[alert.peakSeverity] ?? 'unknown'}>{t.alerts.severity[alert.peakSeverity]}</Pill>
    </span>
  );
}

export function AlertTrackedCell({ alert }: CellProps) {
  const t = useT();
  const open = alert.closedAt === null;
  return (
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
  );
}

export function AlertAckCell({ alert }: CellProps) {
  const t = useT();
  const covering = isAckCovering(alert);
  const overtaken = alert.ackAt !== null && !covering;
  return (
    <span data-testid="alert-ack" className="flex min-w-0 flex-col">
      <span className="truncate">{covering ? t.alerts.row.acknowledged : t.alerts.row.unacknowledged}</span>
      {overtaken && <span className={`truncate text-muted-foreground ${CELL_MINOR}`}>{t.alerts.row.overtaken}</span>}
      {covering && alert.ackNote && <span className={`truncate text-muted-foreground ${CELL_MINOR}`}>{alert.ackNote}</span>}
    </span>
  );
}

export function AlertMuteCell({ alert }: CellProps) {
  const t = useT();
  const fmt = useFormat();
  const mute = muteLabel(alert, t, fmt);
  return mute ? (
    <span data-testid="alert-mute" className="block truncate text-muted-foreground">
      {mute}
    </span>
  ) : (
    <span />
  );
}
