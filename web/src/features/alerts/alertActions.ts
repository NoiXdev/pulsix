import { useUnacknowledgeAlert, useUnsnoozeAlert } from '../../api/hooks/useAlerts.ts';
import type { Messages } from '../../i18n/types.ts';
import { useT } from '../../i18n/useT.ts';
import { may } from '../../lib/roles.ts';
import type { RowAction } from '../../ui/data-list/index.ts';
import { useToast } from '../../ui/toastContext.ts';
import { isAckCovering, type AlertRowAlert } from './alertRow.ts';

/** What the caller's role may do with an alert; an action it may not use is
 * not offered. Acknowledging and snoozing need `alerts.handle` and an open
 * alert. `role` is undefined while it loads, which allows nothing. */
export interface AlertPermissions {
  canRead: boolean;
  canHandle: boolean;
}

export function alertPermissions(role: string | undefined): AlertPermissions {
  return { canRead: may(role, 'checks.read'), canHandle: may(role, 'alerts.handle') };
}

/** What each entry of an alert's row menu does, for the alert it was picked on. */
export interface AlertRowHandlers {
  acknowledge: (alert: AlertRowAlert) => void;
  unacknowledge: (alert: AlertRowAlert) => void;
  snooze: (alert: AlertRowAlert) => void;
  unsnooze: (alert: AlertRowAlert) => void;
  showTimeline: (alert: AlertRowAlert) => void;
}

/**
 * alertRowActions is an alert's row menu (the "…" button and the
 * right-click menu): Acknowledge or Unacknowledge (once the ack covers the
 * current severity), Snooze or Unsnooze (while a snooze mutes it) — both
 * only for an open alert and a caller with `alerts.handle` — then the
 * timeline, which anyone who can read alerts may open.
 */
export function alertRowActions(alert: AlertRowAlert, can: AlertPermissions, on: AlertRowHandlers, t: Messages): RowAction[] {
  const r = t.alerts.row;
  const items: RowAction[] = [];
  if (can.canHandle && alert.closedAt === null) {
    items.push(
      isAckCovering(alert)
        ? { id: 'unacknowledge', label: r.unacknowledge, onSelect: () => on.unacknowledge(alert) }
        : { id: 'acknowledge', label: r.acknowledge, onSelect: () => on.acknowledge(alert) },
    );
    items.push(
      alert.muted && alert.muteReason === 'snooze'
        ? { id: 'unsnooze', label: r.unsnooze, onSelect: () => on.unsnooze(alert) }
        : { id: 'snooze', label: r.snooze, onSelect: () => on.snooze(alert) },
    );
  }
  items.push({ id: 'timeline', label: t.alerts.showTimeline, onSelect: () => on.showTimeline(alert) });
  return items;
}

/** useAlertCommands is what the alert menu does without a dialog: take an
 * acknowledgement or a snooze back (a failure says why in a toast). */
export function useAlertCommands(org: string) {
  const t = useT();
  const toast = useToast();
  const unacknowledge = useUnacknowledgeAlert(org);
  const unsnooze = useUnsnoozeAlert(org);

  async function onUnacknowledge(alert: Pick<AlertRowAlert, 'id'>) {
    try {
      await unacknowledge.mutateAsync(alert.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.alerts.unacknowledgeFailed, 'destructive');
    }
  }

  async function onUnsnooze(alert: Pick<AlertRowAlert, 'id'>) {
    try {
      await unsnooze.mutateAsync(alert.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.alerts.unsnoozeFailed, 'destructive');
    }
  }

  return { unacknowledge: onUnacknowledge, unsnooze: onUnsnooze };
}
