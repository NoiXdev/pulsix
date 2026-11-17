import { CalendarClock } from 'lucide-react';
import { Pill } from '../../ui/badge.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { AlertRoute } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { ReachesNobody, RouteFiresOn } from './AlertRouteCells.tsx';
import { routeReachesNobody } from './alertRoute.ts';

export interface AlertRouteColumnsContext {
  t: Messages;
  scopeLabel: (r: AlertRoute) => string;
  channelNames: (r: AlertRoute) => string;
  rotationName: (r: AlertRoute) => string;
  rotationOf: (r: AlertRoute) => Parameters<typeof routeReachesNobody>[1];
}

/**
 * alertRouteColumns is every column the alert routes list offers (list id
 * `alert-routes`), sorted in the browser. A route opens nothing (its dialog
 * edits it), so the name is plain text. Whether it is enabled is a column
 * that only shows the state; Enable and Disable are in the row menu.
 */
export function alertRouteColumns({ t, scopeLabel, channelNames, rotationName, rotationOf }: AlertRouteColumnsContext): DataListColumn<AlertRoute>[] {
  const m = t.notifications;
  const a = m.alertRoutes;
  return [
    {
      id: 'route',
      label: a.columns.route,
      width: 'minmax(0, 0.9fr)',
      sortKey: 'name',
      sortValue: (r) => r.name,
      required: true,
      defaultVisible: true,
      render: (r) => <span className="block truncate font-medium">{r.name}</span>,
    },
    {
      id: 'applies_to',
      label: a.columns.appliesTo,
      width: 'minmax(0, 0.9fr)',
      sortKey: 'applies_to',
      sortValue: (r) => scopeLabel(r),
      defaultVisible: true,
      render: (r) => <span className="block truncate text-muted-foreground">{scopeLabel(r)}</span>,
    },
    {
      id: 'fires_on',
      label: a.columns.firesOn,
      width: 'minmax(0, 1.2fr)',
      defaultVisible: true,
      render: (r) => <RouteFiresOn route={r} />,
    },
    {
      // Not "Channels": a rotation-sourced route has none of its own, and this cell names the rotation instead.
      id: 'delivers_to',
      label: a.columns.deliversTo,
      width: 'minmax(0, 1fr)',
      defaultVisible: true,
      render: (r) =>
        routeReachesNobody(r, rotationOf(r)) ? (
          <ReachesNobody />
        ) : r.rotation_id ? (
          <span className={`flex items-center gap-1.5 truncate text-muted-foreground ${CELL_MINOR}`}>
            <CalendarClock size={12} aria-hidden="true" />
            <span className="truncate">{a.onCall(rotationName(r))}</span>
          </span>
        ) : (
          <span className={`block truncate font-mono text-muted-foreground ${CELL_MINOR}`}>{channelNames(r)}</span>
        ),
    },
    {
      id: 'enabled',
      label: a.columns.enabled,
      width: '100px',
      sortKey: 'enabled',
      sortValue: (r) => (r.enabled ? 0 : 1),
      defaultVisible: true,
      render: (r) => <span>{r.enabled ? <Pill tone="ok">{m.enabled}</Pill> : <Pill tone="unknown">{m.disabled}</Pill>}</span>,
    },
  ];
}
