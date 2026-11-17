import { Link } from '@tanstack/react-router';
import { useHostTags } from '../../api/hooks/useTags.ts';
import type { Agent, HostListItem } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { useT } from '../../i18n/useT.ts';
import { Tag } from '../../ui/badge.tsx';
import { CELL_MINOR, type Density } from '../../ui/data-list/index.ts';
import { liveRunsOf } from '../runs/runsSearch.ts';
import { seenLabel } from '../workers/expiry.ts';
import { HostPresenceBadge } from './HostPresenceBadge.tsx';
import { hostPresence } from './presence.ts';

// The cells of the hosts list (hostColumns.tsx) that need more than one
// line: each is a component, as some read hooks (the language, a host's tags).

/**
 * HostName is the name cell: the agent's state dot, the name and, for a
 * client, its presence badge; the comfortable density adds the address
 * underneath. The host's agent is what can honestly claim a state (v0.3a):
 * it reports on a heartbeat, and the server decides online from its age. A
 * host without an agent stays unknown. An offline client is off, not a
 * host whose agent went quiet (backlog 3.57): its dot is the neutral colour.
 */
export function HostName({ host, agent, density }: { host: HostListItem; agent: Agent | undefined; density: Density }) {
  const t = useT();
  const status = agent?.status;
  const statusLabel = status && status in t.agents.status ? t.agents.status[status as keyof typeof t.agents.status] : (status ?? '');
  const presence = hostPresence(host.kind, agent);
  const off = presence.kind === 'off';
  return (
    <span className="flex min-w-0 items-center gap-2.5">
      <span
        className="h-2 w-2 shrink-0 rounded-full"
        style={{ background: status === 'online' ? 'var(--px-ok)' : status && !off ? 'var(--px-warn)' : 'var(--px-unknown)' }}
        title={status ? t.hosts.row.agentTitle(statusLabel) : t.hosts.row.unknown}
      />
      <span className="flex min-w-0 flex-col">
        {/* Wraps rather than squeezing the name: the badge moves below a
            long name instead of truncating it earlier. */}
        <span className="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5">
          <span className="max-w-full truncate font-mono font-semibold text-foreground">{host.name}</span>
          <HostPresenceBadge presence={presence} />
        </span>
        {density === 'comfortable' && host.address && <span className={`truncate font-mono text-muted-foreground ${CELL_MINOR}`}>{host.address}</span>}
      </span>
    </span>
  );
}

/** HostTags is the tags cell: the host's tags as key=value chips. */
export function HostTags({ org, hostId }: { org: string; hostId: string }) {
  const tags = useHostTags(org, hostId);
  return (
    <div className="flex flex-wrap gap-1">
      {(tags.data?.tags ?? []).map((tag) => (
        <Tag key={tag.key}>
          {tag.key}={tag.value}
        </Tag>
      ))}
    </div>
  );
}

/** ActiveRuns is the host's live runs ("2 running"), linking to the runs
 * list filtered to this host and to live runs; a dash for none. The link
 * sits above the row's own link (`relative z-10`). */
export function ActiveRuns({ org, host }: { org: string; host: HostListItem }) {
  const t = useT();
  if (!(host.active_runs > 0)) return <span className={`text-muted-foreground ${CELL_MINOR}`}>—</span>;
  return (
    <Link
      to="/o/$org/runs"
      params={{ org }}
      search={liveRunsOf(host.id)}
      className="relative z-10 inline-flex items-center gap-1.5 font-medium text-brand hover:underline"
    >
      <span className="h-1.5 w-1.5 shrink-0 animate-pulse rounded-full bg-current" aria-hidden="true" />
      {t.hosts.list.activeRuns(host.active_runs)}
    </Link>
  );
}

/** LastSeen is when the host was last heard from; an offline client says
 * when its agent was last seen instead (backlog 3.57). */
export function LastSeen({ host, agent }: { host: HostListItem; agent: Agent | undefined }) {
  const t = useT();
  const presence = hostPresence(host.kind, agent);
  if (presence.kind === 'off') {
    return <span className={`text-muted-foreground ${CELL_MINOR}`}>{t.hosts.presence.lastSeen(seenLabel(presence.lastSeen, new Date(), t))}</span>;
  }
  return <span className={`font-mono text-muted-foreground ${CELL_MINOR}`}>{timeAgo(host.last_seen_at, t)}</span>;
}

function timeAgo(iso: string | null | undefined, t: Messages): string {
  if (!iso) return '—';
  const seconds = Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 1000));
  if (seconds < 60) return t.hosts.row.seconds(seconds);
  if (seconds < 3600) return t.hosts.row.minutes(Math.floor(seconds / 60));
  if (seconds < 86400) return t.hosts.row.hours(Math.floor(seconds / 3600));
  return t.hosts.row.days(Math.floor(seconds / 86400));
}
