import { useState } from 'react';
import { useNavigate, useParams, useSearch } from '@tanstack/react-router';
import { useT } from '../../i18n/useT.ts';
import { Card, GridHeader } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { LoadMoreButton } from '../../ui/LoadMoreButton.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useAlert, useAlerts, useUnacknowledgeAlert, useUnsnoozeAlert, type AlertFilter } from '../../api/hooks/useAlerts.ts';
import { may } from '../../lib/roles.ts';
import { AlertFilters } from './AlertFilters.tsx';
import { AlertRow, ALERTS_COLUMNS } from './AlertRow.tsx';
import { AlertTimeline } from './AlertTimeline.tsx';
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
 *
 * Each row carries the same timeline disclosure HostAlertsCard already
 * has, and for the reason the design doc opens with: "nobody can yet say
 * host X was critical from 14:03 to 14:51 on Tuesday, Anna acknowledged it
 * at 14:12". That card shows OPEN alerts only, so until this page had a
 * disclosure of its own the API could answer that question and the GUI
 * could not — for any alert that had ended (review finding, Important).
 * The panel mounts useAlert only once a row is actually expanded, the
 * same "pay for it when it is asked for" discipline the card holds.
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
  const { org } = useParams({ strict: false }) as { org: string };
  const navigate = useNavigate();
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
  const [expanded, setExpanded] = useState<string | null>(null);

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
  if (!canRead) {
    return (
      <>
        {header}
        <p className="text-sm text-muted-foreground">{t.alerts.noAccess}</p>
      </>
    );
  }

  const filters = <AlertFilters value={filter} onChange={setFilter} />;
  // Visible, not silent (v0.4a Task 10 fix round 2's own review finding):
  // an operator who arrived here from HostAlertsCard's link must be able
  // to see that the list in front of them is narrowed to one host, and
  // get back to the unscoped view — a list that was merely shorter than
  // expected, with no on-screen reason, would read as a bug.
  function clearHostFilter() {
    setFilter((f) => ({ ...f, host: undefined }));
    void navigate({ to: '/o/$org/alerts', params: { org }, search: { host: undefined } });
  }

  const hostNotice = filter.host && (
    <p className="flex items-center gap-2 text-xs text-muted-foreground">
      {t.alerts.hostOnly}
      <button type="button" className="underline hover:text-primary" onClick={clearHostFilter}>
        {t.alerts.showEvery}
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

  const rows = (alerts.data?.pages.flatMap((p) => p.alerts ?? []) ?? []).map(alertToRowAlert);

  async function onUnacknowledge(a: AlertRowAlert) {
    try {
      await unacknowledge.mutateAsync(a.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.alerts.unacknowledgeFailed, 'destructive');
    }
  }

  async function onUnsnooze(a: AlertRowAlert) {
    try {
      await unsnooze.mutateAsync(a.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.alerts.unsnoozeFailed, 'destructive');
    }
  }

  return (
    <>
      {header}
      {hostNotice}
      {filters}
      <Card>
        <GridHeader columns={ALERTS_COLUMNS}>
          <span>{t.alerts.columns.open}</span>
          <span>{t.alerts.columns.host}</span>
          <span>{t.alerts.columns.peak}</span>
          <span>{t.alerts.columns.tracked}</span>
          <span>{t.alerts.columns.ack}</span>
          <span>{t.alerts.columns.mute}</span>
          <span />
        </GridHeader>
        {rows.map((a) => {
          const open = expanded === a.id;
          return (
            <div key={a.id}>
              <AlertRow
                alert={a}
                canWrite={canWrite}
                onAcknowledge={(alert) => setAcking(alert.id)}
                onUnacknowledge={(alert) => void onUnacknowledge(alert)}
                onSnooze={(alert) => setSnoozing(alert.id)}
                onUnsnooze={(alert) => void onUnsnooze(alert)}
              />
              <div className="flex justify-end border-b border-border px-4 py-1.5">
                <Button
                  type="button"
                  variant="ghost"
                  aria-expanded={open}
                  aria-label={t.alerts.timelineFor(a.hostName)}
                  onClick={() => setExpanded(open ? null : a.id)}
                >
                  {open ? t.alerts.hideTimeline : t.alerts.showTimeline}
                </Button>
              </div>
              {open && <AlertTimelinePanel org={org} alertId={a.id} />}
            </div>
          );
        })}
        {/* Only once the cursor is exhausted (backlog 4.37). The `muted`
            filter is applied in Go AFTER the page is cut
            (alerts.Service.List), so a page can legitimately come back
            empty with next_cursor still set — correct, since following
            the cursor yields the full set, but "nothing matched" and
            "Load more" side by side on screen contradict each other.
            This says nothing matched only when that is actually known. */}
        {rows.length === 0 && !alerts.hasNextPage && (
          <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.alerts.empty}</p>
        )}
      </Card>
      {alerts.hasNextPage && (
        <LoadMoreButton onClick={() => void alerts.fetchNextPage()} loading={alerts.isFetchingNextPage} />
      )}
      {acking && <AcknowledgeDialog org={org} alertId={acking} onClose={() => setAcking(null)} />}
      {snoozing && <SnoozeDialog org={org} alertId={snoozing} onClose={() => setSnoozing(null)} />}
    </>
  );
}

/** AlertTimelinePanel is one row's disclosure body: GET /alerts/{id}, the
 * only route that carries a timeline at all. Split out so useAlert is
 * mounted — and therefore requested — only for a row somebody actually
 * expanded, exactly as HostAlertsCard's own panel of the same name is. */
function AlertTimelinePanel({ org, alertId }: { org: string; alertId: string }) {
  const t = useT();
  const detail = useAlert(org, alertId);

  return (
    <div className="border-b border-border p-3">
      {detail.isError && (
        <p role="alert" className="text-xs text-destructive">
          {detail.error instanceof Error ? detail.error.message : t.alerts.timelineReadFailed}
        </p>
      )}
      {!detail.data && !detail.isError && <p className="text-xs text-muted-foreground">{t.common.loading}</p>}
      {detail.data && (
        <AlertTimeline
          entries={detail.data.alert.timeline ?? []}
          closedReason={detail.data.alert.closed_reason ?? ''}
          notificationHold={detail.data.alert.notification_hold ?? ''}
        />
      )}
    </div>
  );
}
