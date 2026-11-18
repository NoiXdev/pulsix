import { Section } from '../../ui/detail-layout.tsx';
import type { Host } from '../../api/types.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { EffectiveRulesPanel } from './EffectiveRulesPanel.tsx';
import { HostChecksCard, ManageChecksLink } from './HostChecksCard.tsx';
import { MonitoringOffHint } from './hostHints.tsx';
import type { HostActions } from './useHostActions.tsx';

interface HostChecksTabProps {
  org: string;
  host: Host;
  role: string;
  actions: HostActions;
  /** Open alerts, for the monitoring-off hint's "still open" line. */
  openAlerts: number;
  /** A client whose agent is offline (backlog 3.57). */
  frozen: boolean;
}

/**
 * HostChecksTab is the host page's Checks tab: every check that applies to
 * the host with its readings and verdicts, and the effective rules below.
 * An unmonitored host has none of it; the monitoring-off hint stands in.
 */
export function HostChecksTab({ org, host, role, actions, openAlerts, frozen }: HostChecksTabProps) {
  const t = useT();
  if (!host.monitored) return <MonitoringOffHint org={org} hostId={host.id} openAlerts={openAlerts} actions={actions} />;
  const canRead = may(role, 'checks.read');
  const canWrite = may(role, 'checks.write');
  return (
    <Section id="checks" title={t.hosts.detail.section.checks} actions={<ManageChecksLink org={org} canWrite={canWrite} />}>
      <HostChecksCard org={org} hostId={host.id} canRead={canRead} canWrite={canWrite} frozen={frozen} />
      <EffectiveRulesPanel org={org} hostId={host.id} canRead={canRead} />
    </Section>
  );
}
