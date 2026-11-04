import { useState } from 'react';
import { Link } from '@tanstack/react-router';
import { useT } from '../../i18n/useT.ts';
import { Button } from '../../ui/button.tsx';
import { Card } from '../../ui/table.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useAlert, useAlerts, useUnacknowledgeAlert, useUnsnoozeAlert } from '../../api/hooks/useAlerts.ts';
import { AlertRow } from './AlertRow.tsx';
import { alertToRowAlert, type AlertRowAlert } from './alertRow.ts';
import { AlertTimeline } from './AlertTimeline.tsx';
import { AcknowledgeDialog } from './AcknowledgeDialog.tsx';
import { SnoozeDialog } from './SnoozeDialog.tsx';

export interface HostAlertsCardProps {
  org: string;
  hostId: string;
  /** `checks.read` — the same bar HostChecksCard and the org-wide Alerts
   * page both hold for monitoring data; without it nothing is requested. */
  canRead: boolean;
  /** `checks.write` — the same bar the org-wide Alerts page holds for
   * acknowledging and snoozing; without it each row is read-only. */
  canWrite?: boolean;
}

/**
 * HostAlertsCard is the host page's own alerts section (v0.4a design doc,
 * "GUI": "an alerts section on the host page reading the same endpoint
 * filtered"). It reads GET /orgs/{org}/alerts?host={hostId}&state=open —
 * the exact list route the org-wide Alerts page uses, filtered to this
 * host — never a host-specific route: alert_handlers.go's own doc comment
 * is explicit that the host view is a filter on List, not a seventh
 * endpoint, and there is nothing else to call here.
 *
 * `state=open` is deliberate, not an incidental default: a host page
 * answers "what is wrong with this machine right now", and the empty
 * state below — "no alerts, this host is healthy" — is most of this
 * card's value. That state is unreachable if closed alerts stayed in the
 * list: a host that ever had a single alert would show a row forever and
 * could never again read as healthy.
 *
 * The history is not lost, only moved: HostAlertsHistoryLink, which the
 * host page puts beside its Alerts section title, sends
 * `search={{ host: hostId }}` to the org-wide Alerts page — `alertsRoute`
 * validates that search parameter (router.tsx) and `AlertsPage` seeds its
 * own filter from it, so the operator lands pre-scoped to this host,
 * closed episodes included, with their own `resolved`/`stale` labelling,
 * rather than on the unscoped full list (fix round 2: a first version of
 * this link pointed at the org-wide page with no search at all — the
 * review finding that caught it: "it removed the only path to a host's
 * closed-alert history and pointed at a replacement that does not
 * exist").
 *
 * Each alert renders through AlertRow unmodified, so this card cannot
 * drift from the org-wide page's own three-facts rendering (whether the
 * alert is open, its episode's peak severity, how its tuple reads right
 * now) or its tense-aware flapping marker by inventing a second, slightly
 * different presentation of the same row. The row's actions are the
 * Alerts page's own as well (spec 1.2, "open alerts with snooze"):
 * `canWrite` is `checks.write`, the same gate AlertsPage uses, and this
 * card owns the same AcknowledgeDialog and SnoozeDialog and the same
 * unacknowledge/unsnooze mutations, so an operator triaging one machine
 * never has to leave its page to silence what they are looking at.
 *
 * That second question is AlertTimeline, fetched from the detail endpoint
 * (useAlert) only once an operator actually opens one alert's disclosure
 * — the same "mount only while asked for" discipline HostChecksCard's own
 * GraphPanel already uses for a metric's series, so a host with several
 * alerts costs one list request until something is actually expanded.
 */
export function HostAlertsCard({ org, hostId, canRead, canWrite = false }: HostAlertsCardProps) {
  const t = useT();
  const toast = useToast();
  const alerts = useAlerts(org, { host: hostId, state: 'open' }, canRead);
  const unacknowledge = useUnacknowledgeAlert(org);
  const unsnooze = useUnsnoozeAlert(org);
  const [open, setOpen] = useState<string | null>(null);
  const [acking, setAcking] = useState<string | null>(null);
  const [snoozing, setSnoozing] = useState<string | null>(null);

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

  // One page is the whole of this card's answer: it asks for open alerts
  // on one host, and a host with more than a page of simultaneously open
  // alerts has a problem no list length fixes. The org-wide page's own
  // Load more (and its `?host=` link, below) is where the rest lives.
  const rows = (alerts.data?.pages.flatMap((p) => p.alerts ?? []) ?? []).map(alertToRowAlert);

  return (
    <Card className="p-4">
      {!canRead && <p className="text-xs text-muted-foreground">{t.alerts.noAccess}</p>}
      {canRead && alerts.isError && (
        <p role="alert" className="text-xs text-destructive">
          {alerts.error instanceof Error ? alerts.error.message : t.alerts.hostCard.readFailed}
        </p>
      )}
      {canRead && !alerts.data && !alerts.isError && <p className="text-xs text-muted-foreground">{t.common.loading}</p>}
      {canRead && alerts.data && rows.length === 0 && (
        <p className="text-xs text-muted-foreground">{t.alerts.hostCard.none}</p>
      )}
      {canRead && alerts.data && rows.length > 0 && (
        <ul className="flex flex-col gap-3">
          {rows.map((a) => {
            const isOpen = open === a.id;
            return (
              <li key={a.id} className="rounded-md border border-border">
                <AlertRow
                  alert={a}
                  canWrite={canWrite}
                  onAcknowledge={(alert) => setAcking(alert.id)}
                  onUnacknowledge={(alert) => void onUnacknowledge(alert)}
                  onSnooze={(alert) => setSnoozing(alert.id)}
                  onUnsnooze={(alert) => void onUnsnooze(alert)}
                />
                <div className="flex justify-end border-t border-border px-4 py-1.5">
                  <Button type="button" variant="ghost" aria-expanded={isOpen} onClick={() => setOpen(isOpen ? null : a.id)}>
                    {isOpen ? t.alerts.hideTimeline : t.alerts.showTimeline}
                  </Button>
                </div>
                {isOpen && <AlertTimelinePanel org={org} alertId={a.id} />}
              </li>
            );
          })}
        </ul>
      )}
      {acking && <AcknowledgeDialog org={org} alertId={acking} onClose={() => setAcking(null)} />}
      {snoozing && <SnoozeDialog org={org} alertId={snoozing} onClose={() => setSnoozing(null)} />}
    </Card>
  );
}

/** AlertTimelinePanel is the per-alert disclosure's own body: GET
 * /alerts/{id} for exactly one alert, the only route that carries a
 * timeline at all. Split out of HostAlertsCard so useAlert is only ever
 * mounted (and only ever requests) for an alert whose disclosure is
 * actually open, mirroring HostChecksCard's own GraphPanel. */
function AlertTimelinePanel({ org, alertId }: { org: string; alertId: string }) {
  const t = useT();
  const detail = useAlert(org, alertId);

  return (
    <div className="border-t border-border p-3">
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

/** HostAlertsHistoryLink is the way from the host's open alerts to all of
 * them, closed ones included: the org-wide Alerts page pre-filtered to the
 * host (see HostAlertsCard). The host page renders it beside the Alerts
 * section's title, which names the card. Nothing without `checks.read`. */
export function HostAlertsHistoryLink({ org, hostId, canRead }: HostAlertsCardProps) {
  const t = useT();
  if (!canRead) return null;
  return (
    <Link to="/o/$org/alerts" params={{ org }} search={{ host: hostId }} className="text-xs text-muted-foreground hover:text-primary">
      {t.alerts.hostCard.fullHistory}
    </Link>
  );
}
