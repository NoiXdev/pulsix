import { BellOff } from 'lucide-react';
import { Pill } from '../../ui/badge.tsx';
import { EVENT_LABEL, SEVERITY_LABEL } from './alertRoute.ts';
import type { AlertRoute } from '../../api/types.ts';

/** The two pieces of a route both listing views show the same way: what
 * it fires on, and whether it still reaches anybody. They live together
 * so that Settings → Alert routes and the host page's route-union card
 * cannot drift into describing the same route differently; the labels
 * and the predicate itself are in alertRoute.ts beside them. */

/** ReachesNobody states decision 8's fact — "this route is configured
 * and tells no one" — wherever a route is listed. Deliberately not a
 * Pill: the pill tones are the monitoring state colours (ui/badge.tsx's
 * own comment reserves `warn` for a degraded state), and a route whose
 * last channel was deleted is a broken configuration, not a state of the
 * fleet. */
export function ReachesNobody() {
  return (
    <span className="inline-flex items-center gap-1 text-[11px] font-semibold text-destructive">
      <BellOff size={12} aria-hidden="true" />
      Reaches nobody — no channel
    </span>
  );
}

/** RouteFiresOn is a route's severity and event-kind selections: the
 * answer to "why did no mail arrive?" when a route does cover the host
 * and still said nothing. Both views need it — Service.ForHost returns
 * every route covering the host whatever its selections are, precisely so
 * this can be shown beside it (ForHost's own doc comment). */
export function RouteFiresOn({ route }: { route: AlertRoute }) {
  const severities = route.severities ?? [];
  const events = route.events ?? [];
  return (
    <span className="flex flex-col gap-1 text-xs">
      <span className="flex flex-wrap gap-1">
        {severities.map((s) => (
          <Pill key={s} tone={s === 'crit' ? 'crit' : s === 'warn' ? 'warn' : 'unknown'}>
            {SEVERITY_LABEL[s] ?? s}
          </Pill>
        ))}
      </span>
      <span className="truncate text-muted-foreground">{events.map((e) => EVENT_LABEL[e] ?? e).join(', ')}</span>
    </span>
  );
}
