import { useParams } from '@tanstack/react-router';
import { Route as RouteIcon } from 'lucide-react';
import { DataList } from '../../ui/data-list/index.ts';
import { Button } from '../../ui/button.tsx';
import { ListToolbar, ResetFilters, ResultCount } from '../../ui/list-toolbar.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useHosts } from '../../api/hooks/useHosts.ts';
import { useMonitoringGroups } from '../../api/hooks/useChecks.ts';
import { useChannels } from '../../api/hooks/useNotifications.ts';
import { useOncallRotations } from '../../api/hooks/useOncall.ts';
import { useAlertRoutes, useCreateAlertRoute, useUpdateAlertRoute } from '../../api/hooks/useAlertRoutes.ts';
import { may } from '../../lib/roles.ts';
import type { AlertRoute, AlertRouteInput } from '../../api/types.ts';
import { RouteDialog } from './RouteDialog.tsx';
import { alertRouteColumns } from './alertRouteColumns.tsx';
import { useAlertRouteListActions } from './useAlertRouteListActions.tsx';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import { useListFilters } from '../lists/filterSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { ALERT_ROUTES_FILTERS, ENABLED_STATES, filterAlertRoutes } from './notificationsSearch.ts';
import { useT } from '../../i18n/useT.ts';
import { PageHeader } from '../../ui/settings.tsx';

/** AlertRoutesPage is Settings → Alert routes (v0.4b design, "GUI"): which
 * of the organization's notification channels hear about which alerts —
 * every severity and lifecycle event is explicit per route (decision 1),
 * so "why did no mail arrive?" has an answer on this page rather than in
 * the ticker's own logs. Same permissions as notification channels
 * themselves (alert_route_handlers.go's own comment: a route decides
 * where alert data LEAVES the system, which weighs as much as a channel
 * does) — operators read, admins manage. */
export function AlertRoutesPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const m = t.notifications;
  const a = m.alertRoutes;
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'channels.read');
  const canWrite = may(role, 'channels.write');
  const routes = useAlertRoutes(org, !!orgInfo.data && canRead);
  const channels = useChannels(org, !!orgInfo.data && canRead);
  const hosts = useHosts(org, {});
  const groups = useMonitoringGroups(org, !!orgInfo.data && canRead);
  // Feeds RouteDialog's rotation picker (the "On-call rotation" channel
  // source) — without this, that radio option renders with nothing to
  // choose, the gap task-10-brief.md's own context calls out: the dialog
  // existed before anything fetched rotations for it.
  const rotations = useOncallRotations(org, !!orgInfo.data && canRead);
  const create = useCreateAlertRoute(org);
  const update = useUpdateAlertRoute(org);
  const channelList = channels.data?.channels ?? [];
  const hostList = hosts.data?.pages.flatMap((p) => p.hosts ?? []) ?? [];
  const groupList = groups.data?.groups ?? [];
  const rotationList = rotations.data?.rotations ?? [];
  const channelOptions = channelList.map((c) => ({ id: c.id, name: c.name }));
  const hostOptions = hostList.map((h) => ({ id: h.id, name: h.name }));
  const groupOptions = groupList.map((g) => ({ id: g.id, name: g.name }));
  const rotationOptions = rotationList.map((r) => ({ id: r.id, name: r.name }));

  function scopeLabel(r: AlertRoute): string {
    if (r.scope === 'host') {
      const host = hostList.find((h) => h.id === r.scope_id);
      return host ? host.name : a.scopeHost((r.scope_id ?? '').slice(0, 8));
    }
    if (r.scope === 'monitoring_group') {
      const group = groupList.find((g) => g.id === r.scope_id);
      return group ? group.name : a.scopeGroup((r.scope_id ?? '').slice(0, 8));
    }
    return a.scopeOrg;
  }

  function channelNames(r: AlertRoute): string {
    return (r.channels ?? []).map((id) => channelList.find((c) => c.id === id)?.name ?? id.slice(0, 8)).join(', ');
  }

  // A rotation-sourced route carries no fixed channels at all
  // (validateChannelSource: exactly one channel source), so the Delivers-to
  // cell has to name the rotation or it says nothing about where this
  // route delivers — which is what left a healthy on-call route looking
  // broken (whole-branch review, Important #2). This page fetches the
  // rotations anyway, for the dialog's picker, so it is also the one view
  // that can tell an emptied cycle — every channel deleted out of it — from
  // a working one, and hand routeReachesNobody what it needs to say so.
  function rotationOf(r: AlertRoute) {
    return rotationList.find((rot) => rot.id === r.rotation_id);
  }

  function rotationName(r: AlertRoute): string {
    return rotationOf(r)?.name ?? a.rotationUnknown((r.rotation_id ?? '').slice(0, 8));
  }

  async function onSave(route: AlertRoute | undefined, body: AlertRouteInput) {
    if (route) {
      await update.mutateAsync({ ...body, id: route.id, revision: route.revision });
    } else {
      await create.mutateAsync(body);
    }
  }

  const actions = useAlertRouteListActions(org, canWrite, (route, onClose) => (
    <RouteDialog
      open
      route={route}
      channels={channelOptions}
      hosts={hostOptions}
      groups={groupOptions}
      rotations={rotationOptions}
      onClose={onClose}
      onSave={(body) => onSave(route, body)}
    />
  ));

  const columns = alertRouteColumns({ t, scopeLabel, channelNames, rotationName, rotationOf });
  const { filters, active, setFilter, reset } = useListFilters(ALERT_ROUTES_FILTERS);
  const view = useListView('alert-routes', columns, { mode: 'client', rowKey: (r) => r.id, resetKey: JSON.stringify(filters) });

  const guard = useQueryGuard(orgInfo);
  const routesGuard = useQueryGuard(routes);
  if (guard) return guard;
  if (routesGuard) return routesGuard;
  if (!orgInfo.data) return <QueryLoading />;
  if (!canRead) {
    return (
      <>
        <PageHeader title={m.title} />
        <p className="text-sm text-muted-foreground">{a.noAccess}</p>
      </>
    );
  }
  if (!routes.data) return <QueryLoading />;
  const list = routes.data.routes ?? [];
  const shown = filterAlertRoutes(list, filters);
  const { rows, total } = view.view(view.ready ? shown : undefined);

  return (
    <>
      <PageHeader
        title={m.title}
        description={a.subtitle}
        actions={
          canWrite ? (
            <Button variant="primary" onClick={actions.openNew}>
              <RouteIcon size={15} /> {a.newRoute}
            </Button>
          ) : undefined
        }
      />
      <DataList
        label={a.label}
        columns={columns}
        rows={rows}
        rowKey={(r) => r.id}
        rowLabel={(r) => r.name}
        rowActions={actions.rowActions}
        onRowActivate={actions.edit}
        loading={!view.ready}
        empty={active ? a.noMatch : a.empty}
        toolbar={
          <ListToolbar count={<ResultCount active={active} shown={shown.length} total={list.length} noun={a.count} />}>
            <FilterSelect
              label={a.filters.state}
              allLabel={a.filters.anyState}
              options={ENABLED_STATES.map((v) => ({ value: v, label: v === 'enabled' ? m.enabled : m.disabled }))}
              value={filters.state}
              onChange={(v) => setFilter('state', v as typeof filters.state)}
            />
            {active && <ResetFilters onReset={reset} />}
          </ListToolbar>
        }
        {...view.listProps(total, a.count)}
      />
      {actions.dialogs}
    </>
  );
}
