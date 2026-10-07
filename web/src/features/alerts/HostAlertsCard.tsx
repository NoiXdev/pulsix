import { useState } from 'react';
import { Link } from '@tanstack/react-router';
import { Button } from '../../ui/button.tsx';
import { Card } from '../../ui/table.tsx';
import { useAlert, useAlerts } from '../../api/hooks/useAlerts.ts';
import { AlertRow } from './AlertRow.tsx';
import { alertToRowAlert } from './alertRow.ts';
import { AlertTimeline } from './AlertTimeline.tsx';

export interface HostAlertsCardProps {
  org: string;
  hostId: string;
  /** `checks.read` — the same bar HostChecksCard and the org-wide Alerts
   * page both hold for monitoring data; without it nothing is requested. */
  canRead: boolean;
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
 * The history is not lost, only moved: the link beside the heading sends
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
 * different presentation of the same row. `canWrite` is never passed
 * (AlertRow defaults it to false): acknowledging and snoozing stay the
 * Alerts page's own job — this card only answers "what does this host
 * currently have open" and, per alert, "what is its full story".
 *
 * That second question is AlertTimeline, fetched from the detail endpoint
 * (useAlert) only once an operator actually opens one alert's disclosure
 * — the same "mount only while asked for" discipline HostChecksCard's own
 * GraphPanel already uses for a metric's series, so a host with several
 * alerts costs one list request until something is actually expanded.
 */
export function HostAlertsCard({ org, hostId, canRead }: HostAlertsCardProps) {
  const alerts = useAlerts(org, { host: hostId, state: 'open' }, canRead);
  const [open, setOpen] = useState<string | null>(null);

  // One page is the whole of this card's answer: it asks for open alerts
  // on one host, and a host with more than a page of simultaneously open
  // alerts has a problem no list length fixes. The org-wide page's own
  // Load more (and its `?host=` link, below) is where the rest lives.
  const rows = (alerts.data?.pages.flatMap((p) => p.alerts ?? []) ?? []).map(alertToRowAlert);

  return (
    <Card className="p-4">
      <div className="mb-3 flex items-center justify-between gap-2">
        <h2 className="text-[13px] font-semibold">Alerts</h2>
        {canRead && (
          <Link
            to="/o/$org/alerts"
            params={{ org }}
            search={{ host: hostId }}
            className="text-xs text-muted-foreground hover:text-primary"
          >
            Full history, including closed alerts
          </Link>
        )}
      </div>

      {!canRead && <p className="text-xs text-muted-foreground">Alerts are visible to members of this organization.</p>}
      {canRead && alerts.isError && (
        <p role="alert" className="text-xs text-destructive">
          {alerts.error instanceof Error ? alerts.error.message : 'Could not read this host’s alerts.'}
        </p>
      )}
      {canRead && !alerts.data && !alerts.isError && <p className="text-xs text-muted-foreground">Loading…</p>}
      {canRead && alerts.data && rows.length === 0 && (
        <p className="text-xs text-muted-foreground">No open alerts for this host right now — it is healthy.</p>
      )}
      {canRead && alerts.data && rows.length > 0 && (
        <ul className="flex flex-col gap-3">
          {rows.map((a) => {
            const isOpen = open === a.id;
            return (
              <li key={a.id} className="rounded-md border border-border">
                <AlertRow alert={a} />
                <div className="flex justify-end border-t border-border px-4 py-1.5">
                  <Button type="button" variant="ghost" aria-expanded={isOpen} onClick={() => setOpen(isOpen ? null : a.id)}>
                    {isOpen ? 'Hide timeline' : 'Show timeline'}
                  </Button>
                </div>
                {isOpen && <AlertTimelinePanel org={org} alertId={a.id} />}
              </li>
            );
          })}
        </ul>
      )}
    </Card>
  );
}

/** AlertTimelinePanel is the per-alert disclosure's own body: GET
 * /alerts/{id} for exactly one alert, the only route that carries a
 * timeline at all. Split out of HostAlertsCard so useAlert is only ever
 * mounted (and only ever requests) for an alert whose disclosure is
 * actually open, mirroring HostChecksCard's own GraphPanel. */
function AlertTimelinePanel({ org, alertId }: { org: string; alertId: string }) {
  const detail = useAlert(org, alertId);

  return (
    <div className="border-t border-border p-3">
      {detail.isError && (
        <p role="alert" className="text-xs text-destructive">
          {detail.error instanceof Error ? detail.error.message : 'Could not read this alert’s timeline.'}
        </p>
      )}
      {!detail.data && !detail.isError && <p className="text-xs text-muted-foreground">Loading…</p>}
      {detail.data && (
        <AlertTimeline entries={detail.data.alert.timeline ?? []} closedReason={detail.data.alert.closed_reason ?? ''} />
      )}
    </div>
  );
}
