import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
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
 */
export function AlertsPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const toast = useToast();
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'checks.read');
  const canWrite = may(role, 'checks.write');
  const [filter, setFilter] = useState<AlertFilter>({});
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

  if (!alerts.data && !alerts.isError) {
    return (
      <>
        {header}
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
      {filters}
      <Card>
        <GridHeader columns={ALERTS_COLUMNS}>
          <span>Open</span>
          <span>Subject</span>
          <span>Peak</span>
          <span>Current</span>
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
