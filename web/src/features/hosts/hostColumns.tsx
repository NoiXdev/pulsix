import type { Agent, HostListItem, Zone } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import { CapabilityCell } from './capabilities.tsx';
import { ActiveRuns, HostName, HostTags, LastSeen } from './HostCells.tsx';
import { systemLabel } from './hostOverview.ts';

/** What the hosts list's cells read besides the host itself, each resolved
 * once for the whole page. */
export interface HostColumnsContext {
  org: string;
  t: Messages;
  /** The org's zones by id: a host shows its zone's slug and sorts by its
   * name (as the server does). */
  zones: ReadonlyMap<string, Pick<Zone, 'slug' | 'name'>>;
  /** Each host's agent by host id, from the one org-wide agents read: its
   * status dot and its version. No entry means the host runs no agent. */
  agents: ReadonlyMap<string, Agent>;
}

/**
 * hostColumns is every column the hosts list offers (list id `hosts`). The
 * sort keys are the server's (GET /hosts `sort`); `sortValue` mirrors them
 * for the tag-selector mode, which sorts its one answer in the browser.
 * Agent version and operating system are there to be chosen; the rest is
 * shown by default.
 */
export function hostColumns({ org, t, zones, agents }: HostColumnsContext): DataListColumn<HostListItem>[] {
  const c = t.hosts.list.columns;
  return [
    {
      id: 'name',
      label: c.host,
      width: 'minmax(170px,1.2fr)',
      sortKey: 'name',
      sortValue: (h) => h.name,
      required: true,
      defaultVisible: true,
      render: (h, { density }) => <HostName host={h} agent={agents.get(h.id)} density={density} />,
    },
    {
      id: 'zone',
      label: c.zone,
      width: '110px',
      sortKey: 'zone',
      sortValue: (h) => zones.get(h.zone_id)?.name,
      defaultVisible: true,
      render: (h) => <span className={`block truncate font-mono text-muted-foreground ${CELL_MINOR}`}>{zones.get(h.zone_id)?.slug ?? h.zone_id.slice(0, 8)}</span>,
    },
    {
      id: 'tags',
      label: c.tags,
      width: 'minmax(0,1fr)',
      defaultVisible: true,
      render: (h) => <HostTags org={org} hostId={h.id} />,
    },
    {
      id: 'managed',
      label: c.managed,
      width: '120px',
      sortKey: 'managed',
      sortValue: (h) => h.managed,
      defaultVisible: true,
      render: (h) => <CapabilityCell kind="managed" on={h.managed} />,
    },
    {
      id: 'monitored',
      label: c.monitored,
      width: '120px',
      sortKey: 'monitored',
      sortValue: (h) => h.monitored,
      defaultVisible: true,
      render: (h) => <CapabilityCell kind="monitored" on={h.monitored} />,
    },
    {
      id: 'active_runs',
      label: c.activeRuns,
      width: '100px',
      sortKey: 'active_runs',
      sortValue: (h) => h.active_runs,
      defaultVisible: true,
      render: (h) => <ActiveRuns org={org} host={h} />,
    },
    {
      id: 'last_seen',
      label: c.seen,
      width: '120px',
      sortKey: 'last_seen',
      sortValue: (h) => h.last_seen_at,
      defaultVisible: true,
      render: (h) => <LastSeen host={h} agent={agents.get(h.id)} />,
    },
    {
      id: 'agent_version',
      label: c.agentVersion,
      width: '120px',
      defaultVisible: false,
      render: (h) => <span className={`block truncate font-mono text-muted-foreground ${CELL_MINOR}`}>{agents.get(h.id)?.version || '—'}</span>,
    },
    {
      id: 'os',
      label: c.os,
      width: '130px',
      defaultVisible: false,
      render: (h) => <span className={`block truncate text-muted-foreground ${CELL_MINOR}`}>{systemLabel(h.facts) ?? '—'}</span>,
    },
  ];
}
