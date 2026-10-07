import { useState } from 'react';
import { useParams, useSearch } from '@tanstack/react-router';
import { Card, GridHeader } from '../../ui/table.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useAlerts, useUnacknowledgeAlert, useUnsnoozeAlert, type AlertFilter } from '../../api/hooks/useAlerts.ts';
import { may } from '../../lib/roles.ts';
import { AlertFilters } from './AlertFilters.tsx';
import { AlertRow, ALERTS_COLUMNS } from './AlertRow.tsx';
import { AcknowledgeDialog } from './AcknowledgeDialog.tsx';
import { SnoozeDialog } from './SnoozeDialog.tsx';
import { alertToRowAlert, type AlertRowAlert } from './alertRow.ts';

/**
 * AlertsPage is the Monitoring → Alerts list (v0.4a design doc, "GUI"):
 * every alert in the organization matching the filter row, newest opened
 * first — exactly the server's own order, the same discipline FleetPage
 * already holds for its own worst-first contract. Same permission bar as
 * the rest of the monitoring surface (alert_handlers.go's own doc
 * comment): checks.read to see it, checks.write to acknowledge or snooze.
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
 */
export function AlertsPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const routeSearch = useSearch({ strict: false }) as { host?: string };
  const toast = useToast();
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'checks.read');
  const canWrite = may(role, 'checks.write');
  const [filter, setFilter] = useState<AlertFilter>({ host: routeSearch.host });
  const [syncedHost, setSyncedHost] = useState(routeSearch.host);
  if (routeSearch.host !== syncedHost) {
    setSyncedHost(routeSearch.host);
    setFilter((f) => ({ ...f, host: routeSearch.host }));
  }
  const alerts = useAlerts(org, filter, !!orgInfo.data && canRead);
  const unacknowledge = useUnacknowledgeAlert(org);
  const unsnooze = useUnsnoozeAlert(org);
  const [acking, setAcking] = useState<string | null>(null);
  const [snoozing, setSnoozing] = useState<string | null>(null);

  const header = <h1 className="text-xl font-semibold">Alerts</h1>;

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
  if (!canRead) {
    return (
      <>
        {header}
        <p className="text-sm text-muted-foreground">Alerts are visible to members of this organization.</p>
      </>
    );
  }

  const filters = <AlertFilters value={filter} onChange={setFilter} />;
  // Visible, not silent (v0.4a Task 10 fix round 2's own review finding):
  // an operator who arrived here from HostAlertsCard's link must be able
  // to see that the list in front of them is narrowed to one host, and
  // get back to the unscoped view — a list that was merely shorter than
  // expected, with no on-screen reason, would read as a bug.
  const hostNotice = filter.host && (
    <p className="flex items-center gap-2 text-xs text-muted-foreground">
      Showing one host&apos;s alerts only.
      <button type="button" className="underline hover:text-primary" onClick={() => setFilter((f) => ({ ...f, host: undefined }))}>
        Show every alert
      </button>
    </p>
  );

  if (!alerts.data && !alerts.isError) {
    return (
      <>
        {header}
        {hostNotice}
        {filters}
        <QueryLoading />
      </>
    );
  }

  const rows = (alerts.data?.alerts ?? []).map(alertToRowAlert);

  async function onUnacknowledge(a: AlertRowAlert) {
    try {
      await unacknowledge.mutateAsync(a.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'Unacknowledging this alert failed', 'destructive');
    }
  }

  async function onUnsnooze(a: AlertRowAlert) {
    try {
      await unsnooze.mutateAsync(a.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'Unsnoozing this alert failed', 'destructive');
    }
  }

  return (
    <>
      {header}
      {hostNotice}
      {filters}
      <Card>
        <GridHeader columns={ALERTS_COLUMNS}>
          <span>Open</span>
          <span>Host</span>
          <span>Peak</span>
          <span>Tracked</span>
          <span>Ack</span>
          <span>Mute</span>
          <span />
        </GridHeader>
        {rows.map((a) => (
          <AlertRow
            key={a.id}
            alert={a}
            canWrite={canWrite}
            onAcknowledge={(alert) => setAcking(alert.id)}
            onUnacknowledge={(alert) => void onUnacknowledge(alert)}
            onSnooze={(alert) => setSnoozing(alert.id)}
            onUnsnooze={(alert) => void onUnsnooze(alert)}
          />
        ))}
        {rows.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">No alerts match this view.</p>}
      </Card>
      {acking && <AcknowledgeDialog org={org} alertId={acking} onClose={() => setAcking(null)} />}
      {snoozing && <SnoozeDialog org={org} alertId={snoozing} onClose={() => setSnoozing(null)} />}
    </>
  );
}
