import { useState } from 'react';
import { ChevronRight } from 'lucide-react';
import { Section } from '../../ui/detail-layout.tsx';
import type { Host } from '../../api/types.ts';
import { cn } from '../../lib/utils.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { HostAlertsCard, HostAlertsHistoryLink } from '../alerts/HostAlertsCard.tsx';
import { HostAlertRoutesCard } from './HostAlertRoutesCard.tsx';
import { MonitoringOffHint } from './hostHints.tsx';
import type { HostActions } from './useHostActions.tsx';

/**
 * HostAlertsTab is the host page's Alerts tab: the open alerts (with
 * Acknowledge and Snooze for a role that may handle them), the way to the
 * full history, and the alert routes folded away under "Who is notified?",
 * read only once someone opens it. An unmonitored host raises no alerts;
 * the monitoring-off hint stands in, counting any still open from before.
 */
export function HostAlertsTab({ org, host, role, actions, openAlerts }: { org: string; host: Host; role: string; actions: HostActions; openAlerts: number }) {
  const t = useT();
  const [routesOpen, setRoutesOpen] = useState(false);
  if (!host.monitored) return <MonitoringOffHint org={org} hostId={host.id} openAlerts={openAlerts} actions={actions} />;
  const canRead = may(role, 'checks.read');
  return (
    <Section id="alerts" title={t.hosts.detail.section.alerts} actions={<HostAlertsHistoryLink org={org} hostId={host.id} canRead={canRead} />}>
      <HostAlertsCard org={org} hostId={host.id} canRead={canRead} canWrite={may(role, 'alerts.handle')} />
      <div className="flex flex-col gap-2">
        <button
          type="button"
          aria-expanded={routesOpen}
          aria-controls={routesOpen ? 'host-alert-routes' : undefined}
          onClick={() => setRoutesOpen((open) => !open)}
          className="flex items-center gap-1.5 self-start rounded text-[13px] font-semibold text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
        >
          <ChevronRight size={14} className={cn('transition-transform', routesOpen && 'rotate-90')} />
          {t.hosts.detail.whoIsNotified}
        </button>
        {routesOpen && (
          <div id="host-alert-routes">
            <HostAlertRoutesCard org={org} hostId={host.id} canRead={may(role, 'channels.read')} />
          </div>
        )}
      </div>
    </Section>
  );
}
