import { useParams } from '@tanstack/react-router';
import { useT } from '../../i18n/useT.ts';
import { useFormat } from '../../i18n/format.ts';
import { DataList } from '../../ui/data-list/index.ts';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useAlertsPage, type AlertFilter } from '../../api/hooks/useAlerts.ts';
import { useListFilters } from '../lists/filterSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { AlertFilters } from './AlertFilters.tsx';
import { ALERTS_FILTERS } from './alertsSearch.ts';
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
 * Every filter, `?host=` included, lives in the URL (alertsSearch.ts),
 * so Back, a reload and a bookmark bring the same view back. `?host=` is
 * HostAlertsCard's link: that card shows open alerts only, so this link is
 * the host's only path to its closed-alert history. It shows as a
 * removable host pill: a list narrowed to one host must say so, and
 * removing the pill clears the URL itself, so nothing re-imposes it.
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
 */
export function AlertsPage() {
  const t = useT();
  const fmt = useFormat();
  const { org } = useParams({ strict: false }) as { org: string };
  const orgInfo = useOrg(org);
  const can = alertPermissions(orgInfo.data?.org.role);
  const list = useListFilters(ALERTS_FILTERS);
  const filter: AlertFilter = list.filters;

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

  const rows = (alerts.data?.alerts ?? []).map(alertToRowAlert);

  return (
    <>
      {header}
      <DataList
        label={t.alerts.list.label}
        columns={columns}
        rows={rows}
        rowKey={(a) => a.id}
        rowLabel={(a) => t.alerts.list.rowLabel(a.hostName, subjectLabel(a, t, fmt))}
        rowActions={actions.rowActions}
        onRowActivate={actions.showTimeline}
        loading={alerts.isPending || alerts.isPlaceholderData}
        empty={t.alerts.empty}
        toolbar={<AlertFilters org={org} list={list} />}
        {...view.listProps(alerts.isPlaceholderData ? undefined : alerts.data?.total, t.alerts.list.count)}
      />
      {actions.dialogs}
    </>
  );
}
