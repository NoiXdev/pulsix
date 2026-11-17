import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { DataList } from '../../ui/data-list/index.ts';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useZones } from '../../api/hooks/useHosts.ts';
import { useMonitoringGroups } from '../../api/hooks/useChecks.ts';
import { useFleet, type FleetFilter } from '../../api/hooks/useFleet.ts';
import { useAgents } from '../../api/hooks/useAgents.ts';
import { errorStatus } from '../../lib/apiError.ts';
import { may } from '../../lib/roles.ts';
import type { FleetHost } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { FleetFilters } from './FleetFilters.tsx';
import { fleetColumns } from './fleetColumns.tsx';
import { fleetLinkLabel } from './fleetHost.ts';
import { hostPresence } from '../hosts/presence.ts';
import { useListView } from '../lists/useListView.ts';

/**
 * FleetPage is the fleet overview (v0.3b-2 spec, "Fleet page"): every host
 * in the organization, worst state first exactly as GET /orgs/{org}/fleet
 * already orders them (fleet_handlers.go's own fleetStateRank), with each
 * host's per-severity tuple counts and the filtered set's own totals at
 * the top. Same permission as the rest of the rule surface (checks.read):
 * this is a read of what monitoring already concluded, not a host
 * management action.
 *
 * The list (list id `fleet`) is shown in exactly the order the API returns
 * until a column is sorted: this page does not re-rank. Recomputing "worst
 * first" here would be a second implementation of fleetStateRank that could
 * silently disagree with the server's own, the same reasoning
 * HostChecksCard already applies to the server's `stale` flag. The filters
 * go to the server; sorting and paging happen in the browser.
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

  const zoneNameById = new Map((zones.data?.zones ?? []).map((z) => [z.id, z.name]));
  const agentByHost = new Map((agents.data?.agents ?? []).map((a) => [a.host_id, a]));
  const presenceOf = (h: FleetHost) => hostPresence(h.kind, agentByHost.get(h.host_id));
  const columns = fleetColumns({ t, zoneName: (id) => zoneNameById.get(id) ?? id.slice(0, 8), presenceOf });
  const view = useListView('fleet', columns, { mode: 'client', rowKey: (h) => h.host_id, resetKey: JSON.stringify(filter) });

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

  const { rows, total } = view.view(view.ready ? fleet.data?.hosts ?? [] : undefined);
  // Still loading, as opposed to having failed: a 422 from an unsupported
  // tag expression leaves `fleet.data` unset too, but that is the field's
  // own error above, not an ongoing fetch.
  const loading = (!fleet.data && !fleet.isError) || !view.ready;

  return (
    <>
      {header}
      {fleet.data && (
        <p className="text-[13px] text-muted-foreground">
          {t.fleet.totals(fleet.data.totals.crit, fleet.data.totals.warn, fleet.data.totals.unknown, fleet.data.totals.ok, fleet.data.totals.no_data, fleet.data.totals.all_ignored)}
        </p>
      )}
      <DataList
        label={t.fleet.label}
        columns={columns}
        rows={rows}
        rowKey={(h) => h.host_id}
        rowLink={(h) => ({ to: '/o/$org/hosts/$id', params: { org, id: h.host_id }, label: fleetLinkLabel(h, presenceOf(h), t) })}
        rowLabel={(h) => h.name}
        loading={loading}
        empty={t.fleet.empty}
        toolbar={filters}
        {...view.listProps(total, t.fleet.count)}
      />
    </>
  );
}
