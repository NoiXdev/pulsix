import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { Card, GridHeader } from '../../ui/table.tsx';
import { ListRow, ListRowName } from '../../ui/list-row.tsx';
import { Pill } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useZones } from '../../api/hooks/useHosts.ts';
import { useMonitoringGroups } from '../../api/hooks/useChecks.ts';
import { useFleet, type FleetFilter } from '../../api/hooks/useFleet.ts';
import { useAgents } from '../../api/hooks/useAgents.ts';
import { errorStatus } from '../../lib/apiError.ts';
import { may } from '../../lib/roles.ts';
import { seenLabel } from '../workers/expiry.ts';
import type { FleetHost } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { fleetStateLabel } from './fleetHost.ts';
import { FleetFilters } from './FleetFilters.tsx';
import { HostPresenceBadge } from '../hosts/HostPresenceBadge.tsx';
import { hostPresence, type AgentPresence } from '../hosts/presence.ts';

const COLUMNS = '90px minmax(0, 1fr) 120px 50px 50px 70px 110px';

/**
 * FleetPage is the fleet overview (v0.3b-2 spec, "Fleet page"): every host
 * in the organization, worst state first exactly as GET /orgs/{org}/fleet
 * already orders them (fleet_handlers.go's own fleetStateRank), with each
 * host's per-severity tuple counts and the filtered set's own totals at
 * the top. Same permission as the rest of the rule surface (checks.read):
 * this is a read of what monitoring already concluded, not a host
 * management action.
 *
 * The list is rendered in exactly the order the API returns — this page
 * does not re-sort. Recomputing "worst first" here would be a second
 * implementation of fleetStateRank that could silently disagree with the
 * server's own, the same reasoning HostChecksCard already applies to the
 * server's `stale` flag.
 */
export function FleetPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'checks.read');
  const [filter, setFilter] = useState<FleetFilter>({});

  const zones = useZones(org);
  const groups = useMonitoringGroups(org, !!orgInfo.data && canRead);
  const fleet = useFleet(org, filter, !!orgInfo.data && canRead);
  // Backlog 3.57: which clients are off is the agents' status, the same
  // org-wide read the hosts list makes; without it no client reads as off.
  const agents = useAgents(org, !!orgInfo.data && canRead && may(role, 'agents.read'));

  const header = <h1 className="text-xl font-semibold">{t.fleet.title}</h1>;

  const orgGuard = useQueryGuard(orgInfo);
  // A 422 from an unsupported tag expression is not a page-level failure —
  // it is the filter's own field reporting the API's refusal (see
  // tagError below) — so the guard is disabled for that one status and the
  // page keeps rendering the filter row and whatever list it already had.
  const fleetGuard = useQueryGuard(fleet, { enabled: errorStatus(fleet.error) !== 422 });
  const guard = orgGuard ?? fleetGuard;
  if (guard) {
    return (
      <>
        {header}
        {guard}
      </>
    );
  }
  if (!orgInfo.data) {
    return (
      <>
        {header}
        <QueryLoading />
      </>
    );
  }
  if (!canRead) {
    return (
      <>
        {header}
        <p className="text-sm text-muted-foreground">{t.fleet.noAccess}</p>
      </>
    );
  }

  const tagError = errorStatus(fleet.error) === 422 ? (fleet.error instanceof Error ? fleet.error.message : null) : null;
  const filters = (
    <FleetFilters
      zones={zones.data?.zones ?? []}
      groups={groups.data?.groups ?? []}
      value={filter}
      onChange={setFilter}
      tagError={tagError}
    />
  );

  // Still loading, as opposed to having failed: a 422 from an unsupported
  // tag expression leaves `fleet.data` unset too, but that is the field's
  // own error above, not an ongoing fetch — showing "Loading…" underneath
  // it while nothing is in flight would be its own small lie.
  if (!fleet.data && !fleet.isError) {
    return (
      <>
        {header}
        {filters}
        <QueryLoading />
      </>
    );
  }

  const hosts = fleet.data?.hosts ?? [];
  const totals = fleet.data?.totals ?? { crit: 0, warn: 0, unknown: 0, ok: 0, no_data: 0, all_ignored: 0 };
  const zoneNameById = new Map((zones.data?.zones ?? []).map((z) => [z.id, z.name]));
  const agentByHost = new Map((agents.data?.agents ?? []).map((a) => [a.host_id, a]));

  return (
    <>
      {header}
      {filters}
      <p className="text-[13px] text-muted-foreground">
        {t.fleet.totals(totals.crit, totals.warn, totals.unknown, totals.ok, totals.no_data, totals.all_ignored)}
      </p>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>{t.fleet.columns.state}</span>
          <span>{t.fleet.columns.host}</span>
          <span>{t.fleet.columns.zone}</span>
          <span>{t.fleet.columns.crit}</span>
          <span>{t.fleet.columns.warn}</span>
          <span>{t.fleet.columns.unknown}</span>
          <span>{t.fleet.columns.since}</span>
        </GridHeader>
        {hosts.map((h) => (
          <FleetRow key={h.host_id} org={org} host={h} zoneName={zoneNameById.get(h.zone_id)} agent={agentByHost.get(h.host_id)} />
        ))}
        {hosts.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.fleet.empty}</p>}
      </Card>
    </>
  );
}

/** FleetRow is one host; the whole row opens the host page.
 *
 * A client whose agent is offline (backlog 3.57) is frozen: the manager
 * keeps its last roll-up but does not judge it, so the row says "Off" in
 * the neutral tone instead of that roll-up's colour, keeps the last state
 * as a small muted label below it and the counts muted, and shows when the agent was
 * last seen where the others show since when their state holds. */
function FleetRow({ org, host, zoneName, agent }: { org: string; host: FleetHost; zoneName: string | undefined; agent: AgentPresence | undefined }) {
  const t = useT();
  const st = fleetStateLabel(host.state, host.all_ignored, t);
  const presence = hostPresence(host.kind, agent);
  const off = presence.kind === 'off';
  const count = off ? 'text-[13px] text-muted-foreground' : 'text-[13px]';
  return (
    <ListRow to="/o/$org/hosts/$id" params={{ org, id: host.host_id }} columns={COLUMNS} label={host.name}>
      {off ? (
        <span className="flex flex-col items-start gap-0.5">
          <Pill tone="unknown">{t.hosts.presence.off}</Pill>
          <span className="text-[10px] leading-tight text-muted-foreground">{t.hosts.presence.lastState(st.label)}</span>
        </span>
      ) : (
        <span title={host.since ? t.fleet.sinceTitle(seenLabel(host.since, new Date(), t)) : undefined}>
          <Pill tone={st.tone}>{st.label}</Pill>
        </span>
      )}
      <span className="flex min-w-0 items-center gap-1.5">
        <ListRowName className="truncate font-mono text-[13px] text-foreground">{host.name}</ListRowName>
        {presence.kind === 'client' && <HostPresenceBadge presence={presence} />}
      </span>
      <span className="truncate text-[13px] text-muted-foreground">{zoneName ?? host.zone_id.slice(0, 8)}</span>
      <span className={count}>{host.crit}</span>
      <span className={count}>{host.warn}</span>
      <span className={count}>{host.unknown}</span>
      {presence.kind === 'off' ? (
        <span className="text-[11px] text-muted-foreground">{t.hosts.presence.lastSeen(seenLabel(presence.lastSeen, new Date(), t))}</span>
      ) : (
        <span className="text-[13px] text-muted-foreground">{host.since ? seenLabel(host.since, new Date(), t) : '—'}</span>
      )}
    </ListRow>
  );
}
