import type { ReactNode } from 'react';
import { useNavigate, useParams, useSearch } from '@tanstack/react-router';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useHost } from '../../api/hooks/useHosts.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useHostChecks } from '../../api/hooks/useChecks.ts';
import { useHostAgent } from '../../api/hooks/useAgents.ts';
import type { Host } from '../../api/types.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { HostAlertsTab } from './HostAlertsTab.tsx';
import { HostChecksTab } from './HostChecksTab.tsx';
import { HostConnectionTab } from './HostConnectionTab.tsx';
import { HostHeader } from './HostHeader.tsx';
import { HostInventoryTab } from './HostInventoryTab.tsx';
import { HostOverviewTab } from './HostOverviewTab.tsx';
import { HostRunsSection } from './HostRunsSection.tsx';
import { HostTabs, type HostTabItem } from './HostTabs.tsx';
import { HOST_PANEL_ID, hostTabId, parseHostTab, type HostTab } from './hostTabs.ts';
import { useHostActions } from './useHostActions.tsx';
import { useHostAlertSummary } from './useHostAlertSummary.ts';
import { hostPresence } from './presence.ts';

/**
 * HostDetailPage is one host on one page (the host-view tabs design,
 * docs/superpowers/specs/2026-11-18-host-view-tabs-design.md): a full-width
 * header with who the host is, its tags and actions, and below it tabs —
 * Overview, Checks, Alerts, Runs, Inventory & facts, Connection — the open
 * one in the URL (`?tab=`, Overview when absent).
 *
 * A capability that is off keeps its tabs and says so inside them: an
 * unmonitored host's Overview, Checks and Alerts show the "Monitoring is
 * off" hint, an unmanaged host's Connection and Inventory the "Management
 * is off" one.
 */
export function HostDetailPage() {
  const { org, id } = useParams({ strict: false }) as { org: string; id: string };
  const orgInfo = useOrg(org);
  const host = useHost(org, id);

  // The org query carries the role, and the role decides what several
  // cards below may ask for at all. `may(undefined, …)` is false by
  // design, so a page rendered before that response lands cannot tell
  // "not loaded yet" from "not permitted" — HostChecksCard would inform
  // an owner that monitoring is visible to members of this organization.
  // Ordered the way ChecksPage and MonitoringGroupsPage order it: the org
  // query's own failure and loading states first, the permission question
  // only once there is a role to ask about.
  const orgGuard = useQueryGuard(orgInfo);
  const hostGuard = useQueryGuard(host);
  if (orgGuard) return orgGuard;
  if (hostGuard) return hostGuard;
  if (!orgInfo.data || !host.data) return <QueryLoading />;
  return <HostDetail org={org} host={host.data.host} role={orgInfo.data.org.role} />;
}

function HostDetail({ org, host, role }: { org: string; host: Host; role: string }) {
  const t = useT();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { tab?: unknown };
  const tab = parseHostTab(search.tab);
  const actions = useHostActions(org, host, role, {
    onDeleted: () => void navigate({ to: '/o/$org/hosts', params: { org }, search: { q: undefined } }),
  });
  const canReadChecks = may(role, 'checks.read');
  const alerts = useHostAlertSummary(org, host.id, canReadChecks);
  const checks = useHostChecks(org, host.id, canReadChecks && host.monitored);
  // Backlog 3.57: a client whose agent is offline is frozen — the manager
  // keeps its last verdicts without judging them — so the tabs show them
  // muted and say why. The same read the header and HostAgentCard make.
  const agent = useHostAgent(org, host.id);
  const frozen = hostPresence(host.kind, agent.data?.agent).kind === 'off';
  const openAlerts = alerts?.open ?? 0;
  const tabs = t.hosts.detail.tabs;

  const select = (next: HostTab) =>
    void navigate({ to: '/o/$org/hosts/$id', params: { org, id: host.id }, search: { tab: next === 'overview' ? undefined : next } });
  // SSH access opens the Connection tab, where the SSH settings are.
  const openSsh = () => select('connection');

  // Counts only while the host is monitored: with nothing measured, a
  // number beside a tab that only holds the hint would have nothing to mean.
  const items: HostTabItem[] = [
    { id: 'overview', label: tabs.overview },
    {
      id: 'checks',
      label: tabs.checks,
      count: host.monitored && checks.data ? (checks.data.checks ?? []).length : undefined,
    },
    {
      id: 'alerts',
      label: tabs.alerts,
      count: host.monitored ? alerts?.open : undefined,
      tone: host.monitored && alerts && alerts.tone !== 'ok' ? alerts.tone : undefined,
    },
    { id: 'runs', label: tabs.runs },
    { id: 'inventory', label: tabs.inventory },
    { id: 'connection', label: tabs.connection },
  ];

  const panels: Record<HostTab, () => ReactNode> = {
    overview: () => (
      <HostOverviewTab org={org} host={host} role={role} actions={actions} openAlerts={openAlerts} frozen={frozen} onTab={select} onSshAccess={openSsh} />
    ),
    checks: () => <HostChecksTab org={org} host={host} role={role} actions={actions} openAlerts={openAlerts} frozen={frozen} />,
    alerts: () => <HostAlertsTab org={org} host={host} role={role} actions={actions} openAlerts={openAlerts} />,
    runs: () => <HostRunsSection org={org} hostId={host.id} />,
    inventory: () => <HostInventoryTab org={org} host={host} actions={actions} />,
    connection: () => <HostConnectionTab org={org} host={host} role={role} actions={actions} />,
  };

  return (
    <div className="flex flex-col gap-5">
      <HostHeader org={org} host={host} role={role} actions={actions} onSshAccess={openSsh} />
      <HostTabs label={tabs.label} tabs={items} active={tab} onSelect={select} />
      <div role="tabpanel" id={HOST_PANEL_ID} aria-labelledby={hostTabId(tab)} className="flex min-w-0 flex-col gap-8">
        {panels[tab]()}
      </div>
      {actions.dialogs}
    </div>
  );
}
