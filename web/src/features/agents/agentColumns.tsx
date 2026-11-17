import { Pill } from '../../ui/badge.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { Agent } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { VersionSkewBadge } from '../workers/VersionSkewBadge.tsx';
import { EXPIRY_WARN_DAYS, daysLeft, expiryLabel, seenLabel } from '../workers/expiry.ts';
import { ConfigVersionBadge } from './ConfigVersionBadge.tsx';

export interface AgentColumnsContext {
  t: Messages;
  /** The moment ages and expiries count from. */
  now: Date;
}

/**
 * agentColumns is every column the agents list offers (list id `agents`),
 * sorted in the browser. The host's page is the agent's detail page, so the
 * host column's row link opens it at its Connection section; Open host and
 * Revoke are the row menu's (useAgentListActions).
 */
export function agentColumns({ t, now }: AgentColumnsContext): DataListColumn<Agent>[] {
  const c = t.agents.columns;
  const statusLabel = (a: Agent) => (a.status in t.agents.status ? t.agents.status[a.status as keyof typeof t.agents.status] : a.status);
  return [
    {
      id: 'host',
      label: c.host,
      width: 'minmax(0, 0.9fr)',
      sortKey: 'host',
      sortValue: (a) => a.host_name,
      required: true,
      defaultVisible: true,
      render: (a) => <span className="block truncate">{a.host_name}</span>,
    },
    {
      id: 'agent',
      label: c.agent,
      width: 'minmax(0, 0.8fr)',
      sortKey: 'agent',
      sortValue: (a) => a.agent_name,
      defaultVisible: true,
      render: (a) => <span className="block truncate font-mono">{a.agent_name}</span>,
    },
    {
      id: 'status',
      label: c.status,
      width: '90px',
      sortKey: 'status',
      sortValue: (a) => a.status,
      defaultVisible: true,
      render: (a) => <Pill tone={a.status === 'online' ? 'ok' : 'warn'}>{statusLabel(a)}</Pill>,
    },
    {
      id: 'last_seen',
      label: c.lastSeen,
      width: '110px',
      sortKey: 'last_seen',
      sortValue: (a) => a.last_seen ?? null,
      defaultVisible: true,
      render: (a) => (
        <span className={`${CELL_MINOR} ${a.status === 'online' ? 'text-muted-foreground' : 'text-[var(--px-warn)]'}`}>{seenLabel(a.last_seen, now, t)}</span>
      ),
    },
    {
      id: 'version',
      label: c.version,
      width: 'minmax(0, 0.9fr)',
      sortKey: 'version',
      sortValue: (a) => a.version || null,
      defaultVisible: true,
      render: (a) => (
        <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
          <span className="truncate font-mono">{a.version || '—'}</span>
          <VersionSkewBadge status={a.version_status} />
          <ConfigVersionBadge reported={a.config_version} expected={a.config_expected} checkedAt={a.config_checked_at} />
        </span>
      ),
    },
    {
      id: 'certificate',
      label: c.certificate,
      width: '110px',
      sortKey: 'certificate',
      sortValue: (a) => a.cert_expires_at ?? null,
      defaultVisible: true,
      render: (a) => {
        const expiring = !!a.cert_expires_at && daysLeft(a.cert_expires_at, now) < EXPIRY_WARN_DAYS;
        return (
          <span className={`${CELL_MINOR} ${expiring ? 'font-semibold text-[var(--px-warn)]' : 'text-muted-foreground'}`} data-expiring={expiring || undefined}>
            {expiryLabel(a.cert_expires_at, now, t)}
          </span>
        );
      },
    },
  ];
}
