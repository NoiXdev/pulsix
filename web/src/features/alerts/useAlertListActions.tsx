import { useState, type ReactNode } from 'react';
import { useT } from '../../i18n/useT.ts';
import { Button } from '../../ui/button.tsx';
import { Dialog } from '../../ui/dialog.tsx';
import type { RowAction } from '../../ui/data-list/index.ts';
import { AcknowledgeDialog } from './AcknowledgeDialog.tsx';
import { AlertTimelinePanel } from './AlertTimelinePanel.tsx';
import { SnoozeDialog } from './SnoozeDialog.tsx';
import { alertRowActions, useAlertCommands, type AlertPermissions, type AlertRowHandlers } from './alertActions.ts';
import type { AlertRowAlert } from './alertRow.ts';

type OpenDialog = { kind: 'acknowledge' | 'snooze' | 'timeline'; alert: AlertRowAlert };

/**
 * useAlertListActions is the alerts list's row menu for every row at once:
 * `rowActions(alert)` builds an alert's entries (DataList shows them in the
 * "…" menu and on a right-click), and `dialogs` holds the one dialog an
 * entry opened, for the alert it was picked on. Render `dialogs` next to the
 * list.
 */
export function useAlertListActions(
  org: string,
  can: AlertPermissions,
): { rowActions: (alert: AlertRowAlert) => RowAction[]; dialogs: ReactNode } {
  const t = useT();
  const commands = useAlertCommands(org);
  const [dialog, setDialog] = useState<OpenDialog | null>(null);
  const close = () => setDialog(null);

  const on: AlertRowHandlers = {
    acknowledge: (alert) => setDialog({ kind: 'acknowledge', alert }),
    unacknowledge: (alert) => void commands.unacknowledge(alert),
    snooze: (alert) => setDialog({ kind: 'snooze', alert }),
    unsnooze: (alert) => void commands.unsnooze(alert),
    showTimeline: (alert) => setDialog({ kind: 'timeline', alert }),
  };

  return {
    rowActions: (alert) => alertRowActions(alert, can, on, t),
    dialogs: dialog && (
      <>
        {dialog.kind === 'acknowledge' && <AcknowledgeDialog org={org} alertId={dialog.alert.id} onClose={close} />}
        {dialog.kind === 'snooze' && <SnoozeDialog org={org} alertId={dialog.alert.id} onClose={close} />}
        {dialog.kind === 'timeline' && (
          <Dialog open onClose={close} title={t.alerts.timelineFor(dialog.alert.hostName)}>
            <div className="flex flex-col gap-3">
              <AlertTimelinePanel org={org} alertId={dialog.alert.id} />
              <div className="flex justify-end">
                <Button type="button" variant="ghost" onClick={close}>
                  {t.common.close}
                </Button>
              </div>
            </div>
          </Dialog>
        )}
      </>
    ),
  };
}
