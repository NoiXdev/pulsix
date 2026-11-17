import { useAlert } from '../../api/hooks/useAlerts.ts';
import { useT } from '../../i18n/useT.ts';
import { AlertTimeline } from './AlertTimeline.tsx';

/** AlertTimelinePanel is one alert's timeline: GET /alerts/{id}, the only
 * route that carries a timeline at all. Mounted — and therefore requested —
 * only once somebody asks for it, the "pay for it when it is asked for"
 * rule HostAlertsCard's own panel of the same name holds too. */
export function AlertTimelinePanel({ org, alertId }: { org: string; alertId: string }) {
  const t = useT();
  const detail = useAlert(org, alertId);

  return (
    <div>
      {detail.isError && (
        <p role="alert" className="text-xs text-destructive">
          {detail.error instanceof Error ? detail.error.message : t.alerts.timelineReadFailed}
        </p>
      )}
      {!detail.data && !detail.isError && <p className="text-xs text-muted-foreground">{t.common.loading}</p>}
      {detail.data && (
        <AlertTimeline
          entries={detail.data.alert.timeline ?? []}
          closedReason={detail.data.alert.closed_reason ?? ''}
          notificationHold={detail.data.alert.notification_hold ?? ''}
        />
      )}
    </div>
  );
}
