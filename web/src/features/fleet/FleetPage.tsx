import { useState } from 'react';
import { Link, useParams } from '@tanstack/react-router';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Pill } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useZones } from '../../api/hooks/useHosts.ts';
import { useMonitoringGroups } from '../../api/hooks/useChecks.ts';
import { useFleet, type FleetFilter } from '../../api/hooks/useFleet.ts';
import { errorStatus } from '../../lib/apiError.ts';
import { may } from '../../lib/roles.ts';
import { seenLabel } from '../workers/expiry.ts';
import type { FleetHost } from '../../api/types.ts';
import { fleetStateLabel } from './fleetHost.ts';
import { FleetFilters } from './FleetFilters.tsx';

const COLUMNS = '90px minmax(0, 1fr) 120px 50px 50px 70px 90px';

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
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'checks.read');
  const [filter, setFilter] = useState<FleetFilter>({});

  const zones = useZones(org);
  const groups = useMonitoringGroups(org, !!orgInfo.data && canRead);
  const fleet = useFleet(org, filter, !!orgInfo.data && canRead);

  const header = <h1 className="text-xl font-semibold">Fleet</h1>;

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
        <p className="text-sm text-muted-foreground">The fleet overview is visible to members of this organization.</p>
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
  const t = fleet.data?.totals ?? { crit: 0, warn: 0, unknown: 0, ok: 0, no_data: 0 };
  const zoneNameById = new Map((zones.data?.zones ?? []).map((z) => [z.id, z.name]));

  return (
    <>
      {header}
      {filters}
      <p className="text-[13px] text-muted-foreground">
        Totals for this filtered view, not the whole organization: {t.crit} crit · {t.warn} warn · {t.unknown} unknown · {t.ok} ok ·{' '}
        {t.no_data} no data
      </p>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>State</span>
          <span>Host</span>
          <span>Zone</span>
          <span>Crit</span>
          <span>Warn</span>
          <span>Unknown</span>
          <span>Since</span>
        </GridHeader>
        {hosts.map((h) => (
          <FleetRow key={h.host_id} org={org} host={h} zoneName={zoneNameById.get(h.zone_id)} />
        ))}
        {hosts.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">No hosts match this view.</p>}
      </Card>
    </>
  );
}

function FleetRow({ org, host, zoneName }: { org: string; host: FleetHost; zoneName: string | undefined }) {
  const st = fleetStateLabel(host.state);
  return (
    <GridRow columns={COLUMNS}>
      <span title={host.since ? `in this state since ${seenLabel(host.since, new Date())}` : undefined}>
        <Pill tone={st.tone}>{st.label}</Pill>
      </span>
      <Link
        to="/o/$org/hosts/$id"
        params={{ org, id: host.host_id }}
        className="truncate font-mono text-[13px] text-foreground hover:text-primary"
      >
        {host.name}
      </Link>
      <span className="truncate text-[13px] text-muted-foreground">{zoneName ?? host.zone_id.slice(0, 8)}</span>
      <span className="text-[13px]">{host.crit}</span>
      <span className="text-[13px]">{host.warn}</span>
      <span className="text-[13px]">{host.unknown}</span>
      <span className="text-[13px] text-muted-foreground">{host.since ? seenLabel(host.since, new Date()) : '—'}</span>
    </GridRow>
  );
}
