import type { AlertRouteStep } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';

/**
 * The pure reading/validity rules behind StepsEditor and RouteEscalation,
 * kept beside them rather than inside StepsEditor.tsx — the same split
 * alertRoute.ts already draws for RouteDialog and alertTimeline.ts draws
 * for AlertTimeline (both files' own doc comments: a file exporting a
 * component may not also export plain functions without breaking fast
 * refresh).
 */

/** formatElapsed names a cumulative time the way an operator reads a
 * ladder, not the way its own delay_seconds counts it: delay_seconds is
 * relative to the rung before it (AlertRouteStepDTO's own doc comment,
 * "after the previous rung actually fired"), so showing only that number
 * beside each rung would make a reader do running addition in their head
 * to find out when a later rung actually fires — and get it wrong under
 * pressure. 0 is the initial delivery itself, before any rung has had a
 * chance to wait on anything. Shared by StepsEditor (one rung at a time)
 * and HostAlertRoutesCard/AlertRoutesPage, through RouteEscalation (the
 * last rung's own total), so both read the identical wording. */
export function formatElapsed(totalSeconds: number, t: Messages): string {
  const e = t.notifications.elapsed;
  if (totalSeconds <= 0) return e.atOnce;
  const minutes = Math.round(totalSeconds / 60);
  if (minutes < 60) return e.minutes(minutes);
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  return rest === 0 ? e.hours(hours) : e.hoursMinutes(hours, rest);
}

/** cumulativeDelays turns each rung's own delay_seconds (relative to the
 * rung before it) into the running total up to and including that rung —
 * what formatElapsed needs to name a rung by when it actually fires. A
 * plain function rather than computed inline inside StepsEditor's render
 * body: mutating an accumulator across a .map/.reduce callback directly in
 * a component triggers the react-compiler lint's own immutability rule,
 * which a call to an ordinary function (its mutation fully local to one
 * call) does not. */
export function cumulativeDelays(steps: AlertRouteStep[]): number[] {
  const out: number[] = [];
  let total = 0;
  for (const s of steps) {
    total += s.delay_seconds;
    out.push(total);
  }
  return out;
}

/** stepsAreComplete answers the one question RouteDialog needs back from
 * StepsEditor to decide whether Save may be enabled: does every rung name
 * a channel source that actually reaches somewhere. This mirrors the
 * route's own channelsValid check (RouteDialog.tsx) rather than
 * reimplementing alertroutes.validateChannelSource — it is the same
 * "fixed needs at least one channel, rotation needs to be chosen" rule the
 * dialog already applies to its own channel section, just asked once per
 * rung instead of once for the route. Position gaps and non-positive
 * delays are the two other things ValidateSteps rejects, but the editor
 * cannot construct either: addStep/removeStep always renumber to a
 * gap-free run, and the delay input's own `min` constraint (StepsEditor's
 * own doc comment) blocks the rest — so neither needs a mirrored check
 * here. rotation_id !== undefined, not truthiness: a rung just switched to
 * "rotation" carries rotation_id === '' until one is chosen, which must
 * still read as an incomplete ROTATION rung, never silently as a complete
 * fixed one. */
export function stepsAreComplete(steps: AlertRouteStep[]): boolean {
  return steps.every((s) => (s.rotation_id !== undefined ? !!s.rotation_id : (s.channel_ids ?? []).length > 0));
}
