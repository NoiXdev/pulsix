import { BellOff } from 'lucide-react';
import { Pill } from '../../ui/badge.tsx';
import { useT } from '../../i18n/useT.ts';
import { eventLabel, severityLabel } from './alertRoute.ts';
import { formatElapsed } from './steps.ts';
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
  const t = useT();
  return (
    <span className="inline-flex items-center gap-1 text-[11px] font-semibold text-destructive">
      <BellOff size={12} aria-hidden="true" />
      {t.notifications.reachesNobody}
    </span>
  );
}

/** RouteFiresOn is a route's severity and event-kind selections: the
 * answer to "why did no mail arrive?" when a route does cover the host
 * and still said nothing. Both views need it — Service.ForHost returns
 * every route covering the host whatever its selections are, precisely so
 * this can be shown beside it (ForHost's own doc comment). */
export function RouteFiresOn({ route }: { route: AlertRoute }) {
  const t = useT();
  const severities = route.severities ?? [];
  const events = route.events ?? [];
  return (
    <span className="flex flex-col gap-1 text-xs">
      <span className="flex flex-wrap gap-1">
        {severities.map((s) => (
          <Pill key={s} tone={s === 'crit' ? 'crit' : s === 'warn' ? 'warn' : 'unknown'}>
            {severityLabel(s, t)}
          </Pill>
        ))}
      </span>
      <span className="truncate text-muted-foreground">{events.map((e) => eventLabel(e, t)).join(', ')}</span>
    </span>
  );
}

/** RouteEscalation is a route's own ladder, summarized to the one question
 * both listing views need answered without opening the editor: who gets
 * woken, and how late. "3 rungs" alone would not say that — a reader would
 * still have to add up delay_seconds by hand (StepsEditor.tsx's own doc
 * comment on why delay is relative to the rung before it, not to the
 * alert) — so this names the rung count AND formatElapsed's own cumulative
 * reading of the LAST one, the same wording the editor itself uses.
 * Renders nothing for a route with no ladder (every route before v0.4d,
 * and any that never added one since): a route that doesn't escalate has
 * nothing here worth a line saying so, the same "nothing to add" reasoning
 * ReachesNobody's absence already gives a route that reaches somebody. */
export function RouteEscalation({ route }: { route: AlertRoute }) {
  const t = useT();
  const steps = route.steps ?? [];
  if (steps.length === 0) return null;
  const total = steps.reduce((sum, s) => sum + s.delay_seconds, 0);
  return (
    <span className="text-[11px] text-muted-foreground">
      {t.notifications.escalatesOver(steps.length, formatElapsed(total, t))}
    </span>
  );
}
