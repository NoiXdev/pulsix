import { Section } from '../../ui/detail-layout.tsx';
import type { Host } from '../../api/types.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { HostAlertsCard } from '../alerts/HostAlertsCard.tsx';
import { MonitoringOffHint } from './hostHints.tsx';
import { ChecksSummary, ConnectionSummary, SectionAction } from './HostOverviewCards.tsx';
import { HostRunsSection } from './HostRunsSection.tsx';
import { HostStateSection } from './HostStateSection.tsx';
import type { HostTab } from './hostTabs.ts';
import type { HostActions } from './useHostActions.tsx';

/** How many runs the Overview shows; the Runs tab has the rest. */
const OVERVIEW_RUNS = 3;

const COLUMNS = 'grid grid-cols-1 items-start gap-8 xl:grid-cols-2';

interface HostOverviewTabProps {
  org: string;
  host: Host;
  role: string;
  actions: HostActions;
  openAlerts: number;
  frozen: boolean;
  onTab: (tab: HostTab) => void;
  onSshAccess: () => void;
}

/**
 * HostOverviewTab is what the host page opens on: the key metrics, the
 * open alerts, the last runs, and summaries of the connection and the
 * checks — each with the way to its own tab. An unmonitored host shows the
 * monitoring-off hint in place of what monitoring would show, and keeps
 * its runs and its connection.
 */
export function HostOverviewTab({ org, host, role, actions, openAlerts, frozen, onTab, onSshAccess }: HostOverviewTabProps) {
  const t = useT();
  const o = t.hosts.detail.overview;
  const canRead = may(role, 'checks.read');
  const runs = (
    <HostRunsSection org={org} hostId={host.id} limit={OVERVIEW_RUNS} actions={<SectionAction onClick={() => onTab('runs')}>{o.allRuns}</SectionAction>} />
  );
  const connection = <ConnectionSummary org={org} host={host} canChange={may(role, 'inventory.write')} onChange={onSshAccess} />;

  if (!host.monitored) {
    return (
      <div className="flex flex-col gap-8">
        <MonitoringOffHint org={org} hostId={host.id} openAlerts={openAlerts} actions={actions} />
        <div className={COLUMNS}>
          {runs}
          {connection}
        </div>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-8">
      <HostStateSection org={org} hostId={host.id} canRead={canRead} frozen={frozen} />
      <div className={COLUMNS}>
        <div className="flex min-w-0 flex-col gap-8">
          <Section id="alerts" title={o.openAlerts}>
            <HostAlertsCard org={org} hostId={host.id} canRead={canRead} canWrite={may(role, 'alerts.handle')} />
          </Section>
          {runs}
        </div>
        <div className="flex min-w-0 flex-col gap-8">
          {connection}
          <ChecksSummary org={org} hostId={host.id} canRead={canRead} onAll={() => onTab('checks')} />
        </div>
      </div>
    </div>
  );
}
