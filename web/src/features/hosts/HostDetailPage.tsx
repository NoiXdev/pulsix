import { Link, useNavigate, useParams } from '@tanstack/react-router';
import { Button } from '../../ui/button.tsx';
import { Card } from '../../ui/table.tsx';
import { DetailLayout, DisabledHint, Section, type SectionNavItem } from '../../ui/detail-layout.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useHost } from '../../api/hooks/useHosts.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useHostChecks } from '../../api/hooks/useChecks.ts';
import type { Host } from '../../api/types.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { HostAlertsCard, HostAlertsHistoryLink } from '../alerts/HostAlertsCard.tsx';
import { HostAgentCard } from './HostAgentCard.tsx';
import { HostAlertRoutesCard } from './HostAlertRoutesCard.tsx';
import { HostChecksCard, ManageChecksLink } from './HostChecksCard.tsx';
import { EffectiveRulesPanel } from './EffectiveRulesPanel.tsx';
import { HostInventoryCard } from './HostInventoryCard.tsx';
import { HostSshCard } from './HostSshCard.tsx';
import { ConnectionTestCard } from './ConnectionTestCard.tsx';
import { HostSidebar } from './HostSidebar.tsx';
import { HostStateSection } from './HostStateSection.tsx';
import { HostRunsSection } from './HostRunsSection.tsx';
import { useHostActions, type HostActions } from './useHostActions.tsx';
import { useHostAlertSummary } from './useHostAlertSummary.ts';

/**
 * HostDetailPage is one host on one page (layout B of the host-view
 * design): a sticky sidebar with who the host is, its capabilities, tags
 * and actions, and a column of sections — State, Alerts, Checks, Runs,
 * Connection & SSH, Inventory, Facts — reachable from the sidebar's nav.
 *
 * A capability that is off removes what depends on it rather than showing
 * it greyed out: an unmonitored host has no State, Alerts or Checks (one
 * "Monitoring is off" card stands in for all three), an unmanaged one no
 * SSH, connection test or inventory.
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
  const actions = useHostActions(org, host, {
    onDeleted: () => void navigate({ to: '/o/$org/hosts', params: { org }, search: { q: undefined } }),
  });
  const canReadChecks = may(role, 'checks.read');
  const canWrite = may(role, 'inventory.write');
  const alerts = useHostAlertSummary(org, host.id, canReadChecks);
  const checks = useHostChecks(org, host.id, canReadChecks && host.monitored);
  const s = t.hosts.detail.section;

  // Counts only while the host is monitored: with nothing measured, a
  // number beside a section that is not there would have nothing to mean.
  const nav: SectionNavItem[] = [
    ...(host.monitored
      ? [
          { id: 'state', label: s.state },
          { id: 'alerts', label: s.alerts, count: alerts?.open, tone: alerts && alerts.tone !== 'ok' ? alerts.tone : undefined },
          { id: 'checks', label: s.checks, count: checks.data ? (checks.data.checks ?? []).length : undefined },
        ]
      : []),
    { id: 'runs', label: s.runs },
    { id: 'connection', label: s.connection },
    { id: 'inventory', label: s.inventory },
    { id: 'facts', label: s.facts },
  ];

  return (
    <>
      <DetailLayout sidebar={<HostSidebar org={org} host={host} actions={actions} />} nav={nav}>
        {host.monitored ? (
          <>
            <HostStateSection org={org} hostId={host.id} canRead={canReadChecks} />
            <Section id="alerts" title={s.alerts} actions={<HostAlertsHistoryLink org={org} hostId={host.id} canRead={canReadChecks} />}>
              <HostAlertsCard org={org} hostId={host.id} canRead={canReadChecks} canWrite={may(role, 'checks.write')} />
              <HostAlertRoutesCard org={org} hostId={host.id} canRead={may(role, 'channels.read')} />
            </Section>
            <Section id="checks" title={s.checks} actions={<ManageChecksLink org={org} canWrite={may(role, 'checks.write')} />}>
              <HostChecksCard org={org} hostId={host.id} canRead={canReadChecks} canWrite={may(role, 'checks.write')} />
              <EffectiveRulesPanel org={org} hostId={host.id} canRead={canReadChecks} />
            </Section>
          </>
        ) : (
          <MonitoringOffHint org={org} hostId={host.id} openAlerts={alerts?.open ?? 0} actions={actions} />
        )}
        <HostRunsSection org={org} hostId={host.id} />
        <Section id="connection" title={s.connection}>
          <HostAgentCard org={org} hostId={host.id} canWrite={may(role, 'agents.write')} />
          {host.managed ? (
            <>
              <HostSshCard org={org} hostId={host.id} managed canWrite={canWrite} canCreateCredential={may(role, 'credentials.write')} />
              <ConnectionTestCard org={org} hostId={host.id} canWrite={canWrite} />
            </>
          ) : (
            <ManagementOffHint actions={actions} withAction />
          )}
        </Section>
        <Section id="inventory" title={s.inventory}>
          {host.managed ? <HostInventoryCard org={org} hostId={host.id} managed /> : <ManagementOffHint actions={actions} />}
        </Section>
        <Section id="facts" title={s.facts}>
          <FactsCard facts={host.facts} />
        </Section>
      </DetailLayout>
      {actions.dialogs}
    </>
  );
}

/** Stands in for State, Alerts and Checks while the host is not monitored.
 * Alerts opened before monitoring was switched off may still be open; they
 * are not hidden silently but counted, with the way to them. */
function MonitoringOffHint({ org, hostId, openAlerts, actions }: { org: string; hostId: string; openAlerts: number; actions: HostActions }) {
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

/** Stands in for what needs SSH while the host is not managed. Only one
 * of the two places it appears carries the Enable button. */
function ManagementOffHint({ actions, withAction = false }: { actions: HostActions; withAction?: boolean }) {
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

/** Every fact the host reported, as reported. */
function FactsCard({ facts }: { facts: Host['facts'] }) {
  const t = useT();
  const entries = Object.entries(facts ?? {});
  return (
    <Card className="px-4 py-2">
      {entries.length === 0 ? (
        <span className="py-1.5 text-xs text-muted-foreground">{t.hosts.detail.noFacts}</span>
      ) : (
        <dl className="flex flex-col">
          {entries.map(([k, v]) => (
            <div key={k} className="flex justify-between gap-4 border-b border-border py-1.5 text-xs last:border-b-0">
              <dt className="text-muted-foreground">{k}</dt>
              <dd className="min-w-0 font-mono break-all text-foreground">{String(v)}</dd>
            </div>
          ))}
        </dl>
      )}
    </Card>
  );
}
