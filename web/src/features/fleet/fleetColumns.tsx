import { Pill } from '../../ui/badge.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { FleetHost } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { seenLabel } from '../workers/expiry.ts';
import { HostPresenceBadge } from '../hosts/HostPresenceBadge.tsx';
import type { HostPresence } from '../hosts/presence.ts';
import { fleetStateLabel } from './fleetHost.ts';

export interface FleetColumnsContext {
  t: Messages;
  /** A zone's name by id; the id's start for a zone this page does not know. */
  zoneName: (id: string) => string;
  /** A host's presence (server, client, off), from its kind and agent. */
  presenceOf: (host: FleetHost) => HostPresence;
}

/**
 * fleetColumns is every column the fleet overview offers (list id `fleet`),
 * sorted in the browser. There is no sort on the state: the unsorted order
 * IS the server's worst-first order (fleet_handlers.go's fleetStateRank),
 * and a second ranking here could silently disagree with it.
 *
 * A client whose agent is offline is frozen: the state column says "Off" in
 * the neutral tone with its last state beneath, the counts are muted, and
 * Since shows when its agent was last seen.
 */
export function fleetColumns({ t, zoneName, presenceOf }: FleetColumnsContext): DataListColumn<FleetHost>[] {
  const c = t.fleet.columns;
  const count = (r: FleetHost, n: number) => <span className={presenceOf(r).kind === 'off' ? 'text-muted-foreground' : undefined}>{n}</span>;
  return [
    {
      id: 'host',
      label: c.host,
      width: 'minmax(170px,1.2fr)',
      sortKey: 'name',
      sortValue: (r) => r.name,
      required: true,
      defaultVisible: true,
      render: (r) => {
        const presence = presenceOf(r);
        return (
          <span className="flex min-w-0 items-center gap-1.5">
            <span className="block truncate font-mono text-foreground">{r.name}</span>
            {presence.kind === 'client' && <HostPresenceBadge presence={presence} />}
          </span>
        );
      },
    },
    {
      id: 'state',
      label: c.state,
      width: '110px',
      defaultVisible: true,
      render: (r) => {
        const st = fleetStateLabel(r.state, r.all_ignored, t);
        const presence = presenceOf(r);
        if (presence.kind === 'off') {
          return (
            <span className="flex flex-col items-start gap-0.5">
              <Pill tone="unknown">{t.hosts.presence.off}</Pill>
              <span className="text-[10px] leading-tight text-muted-foreground">{t.hosts.presence.lastState(st.label)}</span>
            </span>
          );
        }
        return (
          <span title={r.since ? t.fleet.sinceTitle(seenLabel(r.since, new Date(), t)) : undefined}>
            <Pill tone={st.tone}>{st.label}</Pill>
          </span>
        );
      },
    },
    {
      id: 'zone',
      label: c.zone,
      width: '120px',
      sortKey: 'zone',
      sortValue: (r) => zoneName(r.zone_id),
      defaultVisible: true,
      render: (r) => <span className={`block truncate text-muted-foreground ${CELL_MINOR}`}>{zoneName(r.zone_id)}</span>,
    },
    { id: 'crit', label: c.crit, width: '64px', sortKey: 'crit', sortValue: (r) => r.crit, defaultVisible: true, render: (r) => count(r, r.crit) },
    { id: 'warn', label: c.warn, width: '64px', sortKey: 'warn', sortValue: (r) => r.warn, defaultVisible: true, render: (r) => count(r, r.warn) },
    { id: 'unknown', label: c.unknown, width: '84px', sortKey: 'unknown', sortValue: (r) => r.unknown, defaultVisible: true, render: (r) => count(r, r.unknown) },
    {
      id: 'since',
      label: c.since,
      width: '130px',
      sortKey: 'since',
      sortValue: (r) => r.since,
      defaultVisible: true,
      render: (r) => {
        const presence = presenceOf(r);
        if (presence.kind === 'off') {
          return <span className={`text-muted-foreground ${CELL_MINOR}`}>{t.hosts.presence.lastSeen(seenLabel(presence.lastSeen, new Date(), t))}</span>;
        }
        return <span className={`text-muted-foreground ${CELL_MINOR}`}>{r.since ? seenLabel(r.since, new Date(), t) : '—'}</span>;
      },
    },
  ];
}
