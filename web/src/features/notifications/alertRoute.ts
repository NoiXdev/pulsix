import type { AlertRoute, AlertRouteInput } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';

// AlertRouteWindow is the route's delivery window as the wire gives it
// (AlertRouteWindowDTO, internal/api/alert_route_dto.go): a 5-field cron
// naming the instants the window OPENS, a duration in seconds that
// carries it to its close, and the IANA zone the cron is read in. Taken
// from AlertRoute['window'] rather than re-declared, so a wire change to
// the DTO cannot drift silently out of step with this type the way a
// hand-copied one could.
export type AlertRouteWindow = NonNullable<AlertRoute['window']>;

/** How a route's selections are named to a reader. Shared by the two
 * views that show them — Settings → Alert routes and the host page's
 * route-union card — because a route that says "only fires on crit" in
 * one place and something else in the other is worse than either. A value
 * the catalogue has no label for (a newer server) is shown as it is. */
export function severityLabel(severity: string, t: Messages): string {
  const labels: Record<string, string> = t.notifications.severity;
  return Object.hasOwn(labels, severity) ? labels[severity] : severity;
}

export function eventLabel(event: string, t: Messages): string {
  const labels: Record<string, string> = t.notifications.event;
  return Object.hasOwn(labels, event) ? labels[event] : event;
}

/** A route with no channel source left reaches nobody. The API rejects
 * creating one, but deleting a channel removes the link rather than the
 * route (migration 00052's own ON DELETE CASCADE on alert_route_channels),
 * and deleting a rotation takes the route's rotation_id with it the same
 * way — so a route can arrive here naming nobody and keep looking
 * configured while being silent: the trap backlog 4.29 records for
 * maintenance windows, and the one decision 8 of the v0.4b design requires
 * be a visible fact rather than a silence.
 *
 * A rotation-sourced route is NOT one of those, however empty its channel
 * list reads: alertroutes.validateChannelSource requires exactly one
 * channel source, so a route that names a rotation carries no fixed
 * channels by design and the DTO always emits "channels": [] for it.
 * Asking only about that list therefore marked every healthy on-call route
 * broken, which left the indicator carrying no information in either
 * direction — including for the case it exists for (whole-branch review,
 * Important #2).
 *
 * That case is an emptied rotation: deleting a channel cascades it out of
 * every rotation cycle naming it, so a rotation can end up with no channel
 * to hand over to, and the follower then records `unrouted` for anything
 * routed through it. A caller that has the route's rotation in hand passes
 * it, and the indicator says so. A caller that does not — the host page's
 * card, which lists routes without fetching rotations — gets `false`
 * rather than a guess: not knowing whether a rotation is empty is not the
 * same as knowing it is. */
export function routeReachesNobody(route: AlertRoute, rotation?: { channels?: string[] | null }): boolean {
  if (route.rotation_id) {
    return !!rotation && (rotation.channels ?? []).length === 0;
  }
  return (route.channels ?? []).length === 0;
}

/** AlertRouteIdentityKey names the AlertRoute fields that describe the ROW,
 * never the configuration: a server-assigned id, its optimistic-lock
 * revision, and the two timestamps. These are the only fields routeToInput
 * is allowed to leave out of the body it builds — every other field has to
 * be named in ROUTE_INPUT_FIELDS below, or it fails to compile (see
 * there). */
type AlertRouteIdentityKey = 'id' | 'revision' | 'created_at' | 'updated_at';

/**
 * ROUTE_INPUT_FIELDS is a compile-time guard against the exact bug
 * routeToInput has now caused twice: a field added to AlertRouteDTO that
 * nobody taught this function, so it vanishes the next time a user clicks
 * something as unrelated as enable/disable (whole-branch review, Important
 * #3 — window/breakthrough_severities/rotation_id; v0.4d Task 8 — steps).
 *
 * `Record<K, true>` forces exhaustiveness in both directions: TypeScript
 * requires every key of K to appear as a property (a new AlertRouteDTO
 * field left out here is a missing-property error), and rejects any
 * property not in K (a field removed from the DTO, or a typo, is an
 * excess-property error on this literal). K is `keyof AlertRoute` minus
 * the identity keys above, so the day a twelfth configuration field joins
 * the route, this line itself refuses to compile until the new key is
 * listed — at which point whoever is adding it is standing right next to
 * routeToInput's own body and the field can't be forgotten in the one
 * silent way it was twice before.
 *
 * This only proves the field was ACKNOWLEDGED, not that routeToInput's own
 * object literal actually carries it through correctly — that half is
 * still the job of the round-trip test in alertRoute.test.ts, which this
 * guard cannot replace, only force into existing.
 */
export const ROUTE_INPUT_FIELDS: Record<Exclude<keyof AlertRoute, AlertRouteIdentityKey>, true> = {
  name: true,
  enabled: true,
  scope: true,
  scope_id: true,
  severities: true,
  events: true,
  channels: true,
  rotation_id: true,
  steps: true,
  window: true,
  breakthrough_severities: true,
};

/** routeToInput turns a stored route back into the create/update body.
 *
 * PATCH is a full replace (AlertRouteBody's own doc comment), so every
 * field left out of the body is deleted from the route. That makes
 * assembling the body by hand, at a call site that means to change one
 * field, a standing trap: the list's enable/disable toggle did exactly
 * that and silently dropped the delivery window, the breakthrough
 * severities and the rotation — and 400'd outright on a rotation route,
 * whose resulting empty channel list named no channel source at all
 * (whole-branch review, Important #3). One function, beside the route's
 * own wire types, is what keeps "what a route is" from having to be
 * remembered in two places the next time the route grows a field.
 *
 * RouteDialog deliberately does not use this: it composes its body from
 * form state, not from a stored route, and its own draft is the authority
 * on what the user just edited.
 *
 * scope_id, window, breakthrough_severities, rotation_id and steps are
 * omitted rather than sent empty, matching what the dialog sends and what
 * the server's own Validate accepts. steps joined this list for the exact
 * reason window/breakthrough/rotation are already on it (v0.4d, Task 8):
 * leaving a route's escalation ladder out of a PATCH this function builds
 * would silently delete it on every quick list-page toggle (enable/disable)
 * that round-trips a route through here without ever opening the ladder
 * editor at all. */
export function routeToInput(route: AlertRoute): AlertRouteInput {
  return {
    name: route.name,
    enabled: route.enabled,
    scope: route.scope,
    ...(route.scope !== 'org' && route.scope_id ? { scope_id: route.scope_id } : {}),
    severities: route.severities ?? [],
    events: route.events ?? [],
    ...(route.rotation_id ? { rotation_id: route.rotation_id } : { channels: route.channels ?? [] }),
    ...(route.steps?.length ? { steps: route.steps } : {}),
    ...(route.window ? { window: route.window } : {}),
    ...(route.breakthrough_severities?.length ? { breakthrough_severities: route.breakthrough_severities } : {}),
  };
}

// Weekday is the dialog's own vocabulary for a delivery window's days,
// Monday-first to match the only other place this codebase already asks
// an operator to tick weekday boxes (features/schedules/simple.ts's own
// WEEKDAYS). cronexpr's day-of-week field (internal/cronexpr/parse.go)
// numbers Sunday 0 through Saturday 6, Vixie cron's own convention, not
// this one — DOW_NUMBER below is the one place that difference is
// bridged, so a Monday-first UI and a Sunday-zero wire format never have
// to agree with each other anywhere else in this file.
export type Weekday = 'mon' | 'tue' | 'wed' | 'thu' | 'fri' | 'sat' | 'sun';

export const WEEKDAYS: Weekday[] = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];

const DOW_NUMBER: Record<Weekday, number> = { sun: 0, mon: 1, tue: 2, wed: 3, thu: 4, fri: 5, sat: 6 };
const NUMBER_DOW: Record<number, Weekday> = Object.fromEntries(Object.entries(DOW_NUMBER).map(([day, n]) => [n, day as Weekday]));
const WEEKDAY_RANK: Record<Weekday, number> = Object.fromEntries(WEEKDAYS.map((d, i) => [d, i])) as Record<Weekday, number>;
const ALL_WEEKDAYS = WEEKDAYS;
const MINUTES_PER_DAY = 24 * 60;

function minutesSinceMidnight(hhmm: string): number {
  const [h, m] = hhmm.split(':').map(Number);
  return h * 60 + (m || 0);
}

const pad2 = (n: number) => String(n).padStart(2, '0');

/** A delivery window as the dialog edits it: weekday checkboxes plus a
 * from/until pair, the shape {@link compileWindow} turns into the wire's
 * {cron, duration_seconds, timezone} and {@link decompileWindow} reads
 * back out of it. `from`/`until` are "HH:MM" 24-hour wall time, the
 * format a TimeField both reads and writes. */
export interface WindowDraft {
  days: Weekday[];
  from: string;
  until: string;
  timezone: string;
}

/**
 * compileWindow turns the weekday checkboxes plus a from/until pair into
 * the {cron, duration_seconds, timezone} triple alertroutes.Window stores
 * (internal/alertroutes/window.go): the cron names the instants the
 * window OPENS, so its minute and hour always come from `from`, never
 * `until` — and the duration is the from→until span, carrying the window
 * to its close.
 *
 * A window that runs past midnight needs no special case on this end
 * either (window.go's own comment makes the same claim about the cron
 * library): quiet hours from 22:00 to 07:00 are nine hours, not minus
 * fifteen — `until` not strictly after `from` just means the span wraps
 * through midnight before it closes.
 *
 * `from === until` is the one case that arithmetic cannot answer on its
 * own: naively it is a zero-length span, but a window that opens and
 * closes at the same instant would deliver nothing, which is never what
 * leaving the two fields at the same value was for. This function's own
 * decision is that it means the whole day instead — the reading that
 * matches "I set a window but didn't narrow the hours."
 */
export function compileWindow(draft: WindowDraft): { cron: string; duration_seconds: number; timezone: string } {
  const fromMinutes = minutesSinceMidnight(draft.from);
  const untilMinutes = minutesSinceMidnight(draft.until);

  let spanMinutes: number;
  if (fromMinutes === untilMinutes) {
    spanMinutes = MINUTES_PER_DAY;
  } else {
    spanMinutes = untilMinutes - fromMinutes;
    if (spanMinutes < 0) spanMinutes += MINUTES_PER_DAY;
  }

  const fromHour = Math.floor(fromMinutes / 60);
  const fromMinute = fromMinutes % 60;
  const dowNumbers = [...new Set(draft.days.map((d) => DOW_NUMBER[d]))].sort((a, b) => a - b);
  const dow = dowNumbers.length === 0 || dowNumbers.length === 7 ? '*' : dowNumbers.join(',');

  return {
    cron: `${fromMinute} ${fromHour} * * ${dow}`,
    duration_seconds: spanMinutes * 60,
    timezone: draft.timezone,
  };
}

/** decompileWindow is compileWindow's inverse, for prefilling the dialog
 * when editing a route that already has a window: it reads the cron's
 * minute, hour and day-of-week fields and adds the duration back onto
 * `from` (wrapping past midnight the same way compileWindow wraps it
 * away) to recover `until`. A full-day window compiled from `from ===
 * until` round-trips back to that same from === until, rather than some
 * other instant 24 hours later that would print identically anyway. */
export function decompileWindow(window: { cron: string; duration_seconds: number; timezone: string }): WindowDraft {
  const fields = window.cron.trim().split(/\s+/);
  const minute = Number(fields[0]) || 0;
  const hour = Number(fields[1]) || 0;
  const dowField = fields[4];
  const fromMinutes = hour * 60 + minute;
  const untilMinutes = (fromMinutes + Math.round(window.duration_seconds / 60)) % MINUTES_PER_DAY;

  const days: Weekday[] =
    !dowField || dowField === '*'
      ? ALL_WEEKDAYS
      : dowField
          .split(',')
          .map((n) => NUMBER_DOW[Number(n) % 7])
          .filter((d): d is Weekday => !!d)
          .sort((a, b) => WEEKDAY_RANK[a] - WEEKDAY_RANK[b]);

  return {
    days,
    from: `${pad2(hour)}:${pad2(minute)}`,
    until: `${pad2(Math.floor(untilMinutes / 60))}:${pad2(untilMinutes % 60)}`,
    timezone: window.timezone,
  };
}
