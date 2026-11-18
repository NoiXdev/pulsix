import { Link } from '@tanstack/react-router';
import { Button } from '../../ui/button.tsx';
import { DisabledHint } from '../../ui/detail-layout.tsx';
import { useT } from '../../i18n/useT.ts';
import type { HostActions } from './useHostActions.tsx';

/** Stands in for what needs monitoring (the key metrics, alerts, checks)
 * while the host is not monitored, inside each tab that would show it.
 * Alerts opened before monitoring was switched off may still be open; they
 * are not hidden silently but counted, with the way to them. */
export function MonitoringOffHint({ org, hostId, openAlerts, actions }: { org: string; hostId: string; openAlerts: number; actions: HostActions }) {
  const t = useT();
  const m = t.hosts.detail.monitoringOff;
  return (
    <DisabledHint
      title={m.title}
      body={
        <>
          {m.body}
          {openAlerts > 0 && (
            <>
              {' '}
              <Link to="/o/$org/alerts" params={{ org }} search={{ host: hostId }} className="text-[var(--px-warn)] hover:underline">
                {m.stillOpen(openAlerts)}
              </Link>
            </>
          )}
        </>
      }
      action={
        actions.canSwitch ? (
          <Button variant="primary" onClick={actions.toggleMonitoring}>
            {t.hosts.detail.enable}
          </Button>
        ) : undefined
      }
    />
  );
}

/** Stands in for what needs SSH while the host is not managed. Only the
 * Connection tab's carries the Enable button: one way to switch it on is
 * enough, and that tab is where SSH lives. */
export function ManagementOffHint({ actions, withAction = false }: { actions: HostActions; withAction?: boolean }) {
  const t = useT();
  const m = t.hosts.detail.managementOff;
  return (
    <DisabledHint
      title={m.title}
      body={m.body}
      action={
        withAction && actions.canSwitch ? (
          <Button variant="primary" onClick={actions.toggleManagement}>
            {t.hosts.detail.enable}
          </Button>
        ) : undefined
      }
    />
  );
}
