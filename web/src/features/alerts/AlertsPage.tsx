import { useState } from 'react';
import { useNavigate, useParams, useSearch } from '@tanstack/react-router';
import { useT } from '../../i18n/useT.ts';
import { useFormat } from '../../i18n/format.ts';
import { DataList } from '../../ui/data-list/index.ts';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useAlertsPage, type AlertFilter } from '../../api/hooks/useAlerts.ts';
import { useListView } from '../lists/useListView.ts';
import { AlertFilters } from './AlertFilters.tsx';
import { alertPermissions } from './alertActions.ts';
import { alertColumns } from './alertColumns.tsx';
import { alertToRowAlert, subjectLabel } from './alertRow.ts';
import { useAlertListActions } from './useAlertListActions.tsx';

/**
 * AlertsPage is the Monitoring → Alerts list (v0.4a design doc, "GUI"):
 * every alert in the organization matching the filter row, newest opened
 * first — exactly the server's own order, the same discipline FleetPage
 * already holds for its own worst-first contract. Same permission bar as
 * the rest of the monitoring surface (alert_handlers.go's own doc
 * comment): checks.read to see it, alerts.handle to acknowledge or snooze.
 *
 * Bulk acknowledge is deliberately not offered (design doc, "Deliberately
 * not built"): decision 4 already took the peak off an alert storm by
 * routing `unknown` to one host alert instead of one per tuple.
 *
 * `?host=` seeds the filter from the URL (alertsRoute's own
 * validateSearch) rather than starting at a bare `{}` — HostAlertsCard
 * links here with it set, and that link is this host's only remaining
 * path to its own closed-alert history now that the host page itself
 * shows open alerts only (v0.4a Task 10 fix round 2). The sync below
 * follows HostsListPage's own `?q=` pattern exactly: adjusting state
 * during render, not in an effect, because navigating here again with a
 * different `?host=` does not remount this component.
 *
 * Each row's menu carries "Show timeline" (a dialog), for the reason the
 * design doc opens with: "nobody can yet say
 * host X was critical from 14:03 to 14:51 on Tuesday, Anna acknowledged it
 * at 14:12". That card shows OPEN alerts only, so until this page had a
 * disclosure of its own the API could answer that question and the GUI
 * could not — for any alert that had ended (review finding, Important).
 * The dialog mounts useAlert only once it is opened, the same "pay for it
 * when it is asked for" discipline the card holds.
 *
 * The list (id `alerts`) is sorted and paged by the server; every filter,
 * the host scope included, starts again at page 1. Acknowledge, Snooze and
 * their undoing are row-menu entries (alertActions.ts).
 *
 * "Show every alert" (clearHostFilter) clears BOTH the local filter and
 * the URL's own `?host=` (fix round 3's own review finding): clearing
 * only `filter` left the address bar at `.../alerts?host=h1`, so a
 * reload, a bookmarked link, or any remount re-ran the render-time sync
 * above and silently re-imposed the very filter the operator had just
 * cleared. The URL is this page's own source of truth for `host` —
 * `filter` is seeded FROM it — so clearing one without the other left
 * the two disagreeing the moment either one changed independently.
 */
export function AlertsPage() {
  const t = useT();
  const fmt = useFormat();
  const { org } = useParams({ strict: false }) as { org: string };
  const navigate = useNavigate();
  const routeSearch = useSearch({ strict: false }) as { host?: string };
  const orgInfo = useOrg(org);
  const can = alertPermissions(orgInfo.data?.org.role);
  const [filter, setFilter] = useState<AlertFilter>({ host: routeSearch.host });
  const [syncedHost, setSyncedHost] = useState(routeSearch.host);
  if (routeSearch.host !== syncedHost) {
    setSyncedHost(routeSearch.host);
    setFilter((f) => ({ ...f, host: routeSearch.host }));
  }

  const columns = alertColumns(t);
  const view = useListView('alerts', columns, { mode: 'server', resetKey: JSON.stringify(filter) });
  const alerts = useAlertsPage(org, filter, view.serverQuery, { enabled: view.ready && !!orgInfo.data && can.canRead });
  const actions = useAlertListActions(org, can);

  const header = <h1 className="text-xl font-semibold">{t.alerts.title}</h1>;

  const orgGuard = useQueryGuard(orgInfo);
  const alertsGuard = useQueryGuard(alerts);
  const guard = orgGuard ?? alertsGuard;
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
  if (!can.canRead) {
    return (
      <>
        {header}
        <p className="text-sm text-muted-foreground">{t.alerts.noAccess}</p>
      </>
    );
  }

  // Visible, not silent (v0.4a Task 10 fix round 2's own review finding):
  // an operator who arrived here from HostAlertsCard's link must be able
  // to see that the list in front of them is narrowed to one host, and
  // get back to the unscoped view — a list that was merely shorter than
  // expected, with no on-screen reason, would read as a bug. The sort
  // stays; the page starts again at 1.
  function clearHostFilter() {
    setFilter((f) => ({ ...f, host: undefined }));
    void navigate({ to: '/o/$org/alerts', params: { org }, search: (prev) => ({ ...prev, host: undefined, page: undefined }) });
  }

  const hostNotice = filter.host && (
    <p className="flex items-center gap-2 text-xs text-muted-foreground">
      {t.alerts.hostOnly}
      <button type="button" className="underline hover:text-primary" onClick={clearHostFilter}>
        {t.alerts.showEvery}
      </button>
    </p>
  );

  const rows = (alerts.data?.alerts ?? []).map(alertToRowAlert);

  return (
    <>
      {header}
      {hostNotice}
      <DataList
        label={t.alerts.list.label}
        columns={columns}
        rows={rows}
        rowKey={(a) => a.id}
        rowLabel={(a) => t.alerts.list.rowLabel(a.hostName, subjectLabel(a, t, fmt))}
        rowActions={actions.rowActions}
        loading={alerts.isPending || alerts.isPlaceholderData}
        empty={t.alerts.empty}
        toolbar={<AlertFilters value={filter} onChange={setFilter} />}
        {...view.listProps(alerts.isPlaceholderData ? undefined : alerts.data?.total, t.alerts.list.count)}
      />
      {actions.dialogs}
    </>
  );
}
