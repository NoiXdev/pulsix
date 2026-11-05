import { Link } from '@tanstack/react-router';
import { Card } from '../../ui/table.tsx';
import { Pill } from '../../ui/badge.tsx';
import { useHostAlertRoutes } from '../../api/hooks/useAlertRoutes.ts';
import { useMonitoringGroups } from '../../api/hooks/useChecks.ts';
import { useOncallRotations } from '../../api/hooks/useOncall.ts';
import { ReachesNobody, RouteEscalation, RouteFiresOn } from '../notifications/AlertRouteCells.tsx';
import { routeReachesNobody } from '../notifications/alertRoute.ts';
import type { AlertRoute, MonitoringGroup } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { useT } from '../../i18n/useT.ts';

export interface HostAlertRoutesCardProps {
  org: string;
  hostId: string;
  /** `channels.read` — the same bar AlertRoutesPage itself holds
   * (alert_route_handlers.go's own comment: a route decides where alert
   * data LEAVES the system, which weighs more than reading the alerts
   * themselves) — not `checks.read`, the gate HostAlertsCard and
   * EffectiveRulesPanel use for the data a route merely forwards. */
  canRead: boolean;
}

/**
 * HostAlertRoutesCard is the host page's own answer to "why did no mail
 * arrive?" and "why did three?" (backlog 4.1b, Service.ForHost's own doc
 * comment): every enabled alert route that covers this host, at whatever
 * scope, via GET /orgs/{org}/hosts/{id}/alert-routes.
 *
 * Routes add up rather than override — unlike a monitoring rule's single
 * winner, there is no precedence here, so this card lists every covering
 * route side by side rather than picking one. An empty list is said
 * explicitly ("no route covers this host — its alerts reach nobody")
 * rather than left as a blank card a reader could mistake for "not loaded
 * yet": this is the one view on the whole page where "nothing" is itself
 * the finding (decision 8).
 *
 * Each row carries a scope badge — Organization / the monitoring group's
 * own name / This host — the same provenance EffectiveRulesPanel's
 * originChip gives a rule's own scope, so an operator can tell a route
 * that reaches this host because it targets the whole organization apart
 * from one that targets this host by name.
 *
 * Beneath it, what the route fires on. ForHost deliberately returns every
 * route whose scope covers the host whatever its severity and event
 * selections are (its own doc comment says why), so "a route covers this
 * host" and "this alert would have reached a channel" are different
 * statements, and the second is the one the operator asked about: without
 * the selections here the card can say "three routes cover this host" and
 * still leave "why did no mail arrive?" unanswered. A route with no
 * channel left says so outright (decision 8) — it is the one case that
 * looks like coverage and delivers nothing.
 *
 * Editing stays on Settings → Alert routes, one click away through the
 * route's name.
 *
 * Each row also shows its own escalation ladder, when it has one
 * (RouteEscalation) — but routes add up here, never override, so two
 * routes covering the same host each keep their OWN ladder rather than
 * merging into one. That is the spec's own named surprise (v0.4d risk
 * section): an operator who reads one route's ladder and plans around it
 * can still get woken by a second, independent one they never looked at.
 * escalatingCount below turns that from an implicit fact (visible only by
 * noticing two separate "Escalates over N rungs" lines) into an explicit
 * sentence, the same "nothing is itself the finding" posture decision 8
 * already gives an empty route list.
 */
export function HostAlertRoutesCard({ org, hostId, canRead }: HostAlertRoutesCardProps) {
  const t = useT();
  const routes = useHostAlertRoutes(org, hostId, canRead);
  const groups = useMonitoringGroups(org, canRead);
  const groupList = groups.data?.groups ?? [];
  // The rotations are fetched for one reason: routeReachesNobody cannot
  // judge a rotation-sourced route without the rotation's own channel
  // list. Called without it, it answers "reaches somebody" for every such
  // route — including one whose cycle has been emptied, which is exactly
  // the case the indicator and the user documentation exist for. The
  // routes page already pays this cost for the same reason.
  const rotations = useOncallRotations(org, canRead);
  const rotationOf = (r: AlertRoute) =>
    r.rotation_id ? (rotations.data?.rotations ?? []).find((x) => x.id === r.rotation_id) : undefined;
  const list = routes.data?.routes ?? [];
  const escalatingCount = list.filter((r) => (r.steps ?? []).length > 0).length;

  return (
    <Card className="p-4">
      <div className="mb-3 flex items-center justify-between gap-2">
        <h3 className="text-[13px] font-semibold">{t.hosts.alertRoutes.title}</h3>
        {canRead && (
          <Link to="/o/$org/settings/alert-routes" params={{ org }} className="text-xs text-muted-foreground hover:text-primary">
            {t.hosts.alertRoutes.link}
          </Link>
        )}
      </div>

      {!canRead && <p className="text-xs text-muted-foreground">{t.hosts.alertRoutes.noAccess}</p>}
      {canRead && routes.isError && (
        <p role="alert" className="text-xs text-destructive">
          {routes.error instanceof Error ? routes.error.message : t.hosts.alertRoutes.readFailed}
        </p>
      )}
      {canRead && !routes.data && !routes.isError && <p className="text-xs text-muted-foreground">{t.common.loading}</p>}
      {canRead && routes.data && list.length === 0 && (
        <p className="text-xs text-muted-foreground">{t.hosts.alertRoutes.none}</p>
      )}
      {canRead && routes.data && list.length > 0 && (
        <>
          {escalatingCount > 1 && (
            <p className="mb-2 text-xs text-[var(--px-warn)]">
              {t.hosts.alertRoutes.escalating(escalatingCount)}
            </p>
          )}
          <ul className="flex flex-col gap-2">
            {list.map((r) => (
              <li key={r.id} className="flex flex-col gap-1.5 rounded-md border border-border px-3 py-2">
                <div className="flex items-center justify-between gap-2">
                  <Link
                    to="/o/$org/settings/alert-routes"
                    params={{ org }}
                    className="truncate text-[13px] font-medium text-foreground hover:text-primary"
                  >
                    {r.name}
                  </Link>
                  <Pill tone="accent">{scopeLabel(r, groupList, t)}</Pill>
                </div>
                <RouteFiresOn route={r} />
                <RouteEscalation route={r} />
                {routeReachesNobody(r, rotationOf(r)) && <ReachesNobody />}
              </li>
            ))}
          </ul>
        </>
      )}
    </Card>
  );
}

/** scopeLabel names a route's scope the way EffectiveRulesPanel's own
 * originChip names a rule's — a monitoring group by its name, never its
 * raw id — except "This host" rather than a group lookup when the route
 * targets hostId itself (scope "host" on this host-filtered view can only
 * ever mean this host; ForHost never returns another host's route). */
function scopeLabel(r: AlertRoute, groups: MonitoringGroup[], t: Messages): string {
  if (r.scope === 'host') return t.hosts.scope.thisHost;
  if (r.scope === 'monitoring_group') {
    const group = groups.find((g) => g.id === r.scope_id);
    return group ? group.name : t.hosts.scope.groupFallback((r.scope_id ?? '').slice(0, 8));
  }
  return t.hosts.scope.organization;
}
