import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { Route as RouteIcon } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { SettingsTabs } from '../settings/SettingsTabs.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useHosts } from '../../api/hooks/useHosts.ts';
import { useMonitoringGroups } from '../../api/hooks/useChecks.ts';
import { useChannels } from '../../api/hooks/useNotifications.ts';
import { useAlertRoutes, useCreateAlertRoute, useDeleteAlertRoute, useUpdateAlertRoute } from '../../api/hooks/useAlertRoutes.ts';
import { may } from '../../lib/roles.ts';
import type { AlertRoute, AlertRouteInput } from '../../api/types.ts';
import { RouteDialog } from './RouteDialog.tsx';
import { ReachesNobody, RouteFiresOn } from './AlertRouteCells.tsx';
import { routeReachesNobody } from './alertRoute.ts';

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
  const toast = useToast();
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'channels.read');
  const canWrite = may(role, 'channels.write');
  const routes = useAlertRoutes(org, !!orgInfo.data && canRead);
  const channels = useChannels(org, !!orgInfo.data && canRead);
  const hosts = useHosts(org, {});
  const groups = useMonitoringGroups(org, !!orgInfo.data && canRead);
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
    <div className="flex flex-col gap-2">
      <h1 className="text-xl font-semibold">Notifications</h1>
      <SettingsTabs org={org} />
    </div>
  );
  if (!canRead) {
    return (
      <>
        {header}
        <p className="text-sm text-muted-foreground">Alert routes are visible to operators and above.</p>
      </>
    );
  }
  if (!routes.data) return <QueryLoading />;
  const list = routes.data.routes ?? [];
  const channelList = channels.data?.channels ?? [];
  const hostList = hosts.data?.pages.flatMap((p) => p.hosts ?? []) ?? [];
  const groupList = groups.data?.groups ?? [];
  const channelOptions = channelList.map((c) => ({ id: c.id, name: c.name }));
  const hostOptions = hostList.map((h) => ({ id: h.id, name: h.name }));
  const groupOptions = groupList.map((g) => ({ id: g.id, name: g.name }));

  function scopeLabel(r: AlertRoute): string {
    if (r.scope === 'host') {
      const host = hostList.find((h) => h.id === r.scope_id);
      return host ? host.name : `host ${(r.scope_id ?? '').slice(0, 8)}`;
    }
    if (r.scope === 'monitoring_group') {
      const group = groupList.find((g) => g.id === r.scope_id);
      return group ? group.name : `group ${(r.scope_id ?? '').slice(0, 8)}`;
    }
    return 'Organization';
  }

  function channelNames(r: AlertRoute): string {
    return (r.channels ?? []).map((id) => channelList.find((c) => c.id === id)?.name ?? id.slice(0, 8)).join(', ');
  }

  async function onSave(body: AlertRouteInput) {
    if (editing && editing !== 'new') {
      await update.mutateAsync({ ...body, id: editing.id, revision: editing.revision });
    } else {
      await create.mutateAsync(body);
    }
  }

  async function onToggle(r: AlertRoute) {
    try {
      await update.mutateAsync({
        id: r.id,
        revision: r.revision,
        name: r.name,
        enabled: !r.enabled,
        scope: r.scope,
        ...(r.scope !== 'org' && r.scope_id ? { scope_id: r.scope_id } : {}),
        severities: r.severities ?? [],
        events: r.events ?? [],
        channels: r.channels ?? [],
      });
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'Updating the route failed', 'destructive');
    }
  }

  async function onDelete(r: AlertRoute) {
    if (!confirm(`Delete route ${r.name}? Its channels stop hearing about the alerts it covered.`)) return;
    try {
      await remove.mutateAsync(r.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'Deleting failed', 'destructive');
    }
  }

  return (
    <>
      {header}
      <div className="flex items-center justify-between">
        <span className="text-[13px] text-muted-foreground">Which severities and alert events reach which notification channels</span>
        {canWrite && (
          <Button variant="primary" onClick={() => setEditing('new')}>
            <RouteIcon size={15} /> New route
          </Button>
        )}
      </div>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>Route</span>
          <span>Applies to</span>
          <span>Fires on</span>
          <span>Channels</span>
          <span>Status</span>
          <span />
        </GridHeader>
        {list.map((r) => (
          <GridRow key={r.id} columns={COLUMNS}>
            <span className="truncate text-[13px] font-medium" data-row>
              {r.name}
            </span>
            <span className="truncate text-[13px] text-muted-foreground">{scopeLabel(r)}</span>
            <RouteFiresOn route={r} />
            {routeReachesNobody(r) ? (
              <ReachesNobody />
            ) : (
              <span className="truncate font-mono text-xs text-muted-foreground">{channelNames(r)}</span>
            )}
            <span>
              {canWrite ? (
                <button
                  type="button"
                  role="switch"
                  aria-checked={r.enabled}
                  aria-label={`${r.enabled ? 'Disable' : 'Enable'} ${r.name}`}
                  onClick={() => void onToggle(r)}
                  className={r.enabled ? 'flex h-[18px] w-8 items-center rounded-full bg-primary px-0.5 justify-end' : 'flex h-[18px] w-8 items-center rounded-full bg-border px-0.5 justify-start'}
                >
                  <span className="h-3.5 w-3.5 rounded-full bg-background" />
                </button>
              ) : r.enabled ? (
                <Pill tone="ok">enabled</Pill>
              ) : (
                <Pill tone="unknown">disabled</Pill>
              )}
            </span>
            <span className="flex justify-end gap-1">
              {canWrite && (
                <>
                  <Button type="button" variant="ghost" aria-label={`Edit ${r.name}`} onClick={() => setEditing(r)}>
                    Edit
                  </Button>
                  <Button type="button" variant="destructive" aria-label={`Delete ${r.name}`} onClick={() => void onDelete(r)}>
                    Delete
                  </Button>
                </>
              )}
            </span>
          </GridRow>
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">No alert routes yet.</p>}
      </Card>
      {editing && (
        <RouteDialog
          open
          route={editing === 'new' ? undefined : editing}
          channels={channelOptions}
          hosts={hostOptions}
          groups={groupOptions}
          onClose={() => setEditing(null)}
          onSave={onSave}
        />
      )}
    </>
  );
}
