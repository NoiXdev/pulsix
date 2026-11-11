import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { CalendarClock, Route as RouteIcon } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Switch } from '../../ui/switch.tsx';
import { Pill } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useHosts } from '../../api/hooks/useHosts.ts';
import { useMonitoringGroups } from '../../api/hooks/useChecks.ts';
import { useChannels } from '../../api/hooks/useNotifications.ts';
import { useOncallRotations } from '../../api/hooks/useOncall.ts';
import { useAlertRoutes, useCreateAlertRoute, useDeleteAlertRoute, useUpdateAlertRoute } from '../../api/hooks/useAlertRoutes.ts';
import { may } from '../../lib/roles.ts';
import type { AlertRoute, AlertRouteInput } from '../../api/types.ts';
import { RouteDialog } from './RouteDialog.tsx';
import { ReachesNobody, RouteFiresOn } from './AlertRouteCells.tsx';
import { routeReachesNobody, routeToInput } from './alertRoute.ts';
import { useT } from '../../i18n/useT.ts';
import { useConfirm } from '../../ui/confirmContext.ts';

const COLUMNS = 'minmax(0, 0.9fr) minmax(0, 0.9fr) minmax(0, 1.2fr) minmax(0, 1fr) 90px 220px';

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
  const toast = useToast();
  const ask = useConfirm();
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
  const remove = useDeleteAlertRoute(org);
  const [editing, setEditing] = useState<AlertRoute | 'new' | null>(null);

  const guard = useQueryGuard(orgInfo);
  const routesGuard = useQueryGuard(routes);
  if (guard) return guard;
  if (routesGuard) return routesGuard;
  if (!orgInfo.data) return <QueryLoading />;
  const header = (
    <h1 className="text-xl font-semibold">{m.title}</h1>
  );
  if (!canRead) {
    return (
      <>
        {header}
        <p className="text-sm text-muted-foreground">{a.noAccess}</p>
      </>
    );
  }
  if (!routes.data) return <QueryLoading />;
  const list = routes.data.routes ?? [];
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

  async function onSave(body: AlertRouteInput) {
    if (editing && editing !== 'new') {
      await update.mutateAsync({ ...body, id: editing.id, revision: editing.revision });
    } else {
      await create.mutateAsync(body);
    }
  }

  // PATCH is a full replace, so the toggle has to send the whole route
  // back, not the handful of fields it happens to remember: rebuilt by
  // hand here, it silently deleted the delivery window, the breakthrough
  // severities and the rotation, and 400'd outright on a rotation route
  // (whole-branch review, Important #3). routeToInput is the one place
  // that knows what a stored route is on the wire, so a field added to
  // the route in future cannot be forgotten at this call site.
  async function onToggle(r: AlertRoute) {
    try {
      await update.mutateAsync({ ...routeToInput(r), id: r.id, revision: r.revision, enabled: !r.enabled });
    } catch (err) {
      toast.show(err instanceof Error ? err.message : a.updateFailed, 'destructive');
    }
  }

  async function onDelete(r: AlertRoute) {
    if (!(await ask({ message: a.deleteConfirm(r.name), confirmLabel: t.common.delete, destructive: true }))) return;
    try {
      await remove.mutateAsync(r.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : m.deleteFailed, 'destructive');
    }
  }

  return (
    <>
      {header}
      <div className="flex items-center justify-between">
        <span className="text-[13px] text-muted-foreground">{a.subtitle}</span>
        {canWrite && (
          <Button variant="primary" onClick={() => setEditing('new')}>
            <RouteIcon size={15} /> {a.newRoute}
          </Button>
        )}
      </div>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>{a.columns.route}</span>
          <span>{a.columns.appliesTo}</span>
          <span>{a.columns.firesOn}</span>
          {/* Not "Channels": a rotation-sourced route has none of its own, and this cell names the rotation instead. */}
          <span>{a.columns.deliversTo}</span>
          <span>{a.columns.status}</span>
          <span />
        </GridHeader>
        {list.map((r) => (
          <GridRow key={r.id} columns={COLUMNS}>
            <span className="truncate text-[13px] font-medium" data-row>
              {r.name}
            </span>
            <span className="truncate text-[13px] text-muted-foreground">{scopeLabel(r)}</span>
            <RouteFiresOn route={r} />
            {routeReachesNobody(r, rotationOf(r)) ? (
              <ReachesNobody />
            ) : r.rotation_id ? (
              <span className="flex items-center gap-1.5 truncate text-xs text-muted-foreground">
                <CalendarClock size={12} aria-hidden="true" />
                <span className="truncate">{a.onCall(rotationName(r))}</span>
              </span>
            ) : (
              <span className="truncate font-mono text-xs text-muted-foreground">{channelNames(r)}</span>
            )}
            <span>
              {canWrite ? (
                <Switch
                  checked={r.enabled}
                  aria-label={r.enabled ? a.disableAria(r.name) : a.enableAria(r.name)}
                  onCheckedChange={() => void onToggle(r)}
                />
              ) : r.enabled ? (
                <Pill tone="ok">{m.enabled}</Pill>
              ) : (
                <Pill tone="unknown">{m.disabled}</Pill>
              )}
            </span>
            <span className="flex justify-end gap-1">
              {canWrite && (
                <>
                  <Button type="button" variant="ghost" aria-label={m.editAria(r.name)} onClick={() => setEditing(r)}>
                    {m.edit}
                  </Button>
                  <Button type="button" variant="destructive" aria-label={m.deleteAria(r.name)} onClick={() => void onDelete(r)}>
                    {t.common.delete}
                  </Button>
                </>
              )}
            </span>
          </GridRow>
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{a.empty}</p>}
      </Card>
      {editing && (
        <RouteDialog
          open
          route={editing === 'new' ? undefined : editing}
          channels={channelOptions}
          hosts={hostOptions}
          groups={groupOptions}
          rotations={rotationOptions}
          onClose={() => setEditing(null)}
          onSave={onSave}
        />
      )}
    </>
  );
}
