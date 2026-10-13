import type { AlertRoute } from '../../api/types.ts';

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
 * one place and something else in the other is worse than either. */
export const SEVERITY_LABEL: Record<string, string> = { warn: 'Warning', crit: 'Critical', unknown: 'Unknown' };

export const EVENT_LABEL: Record<string, string> = {
  opened: 'opened',
  severity_raised: 'worsened',
  acknowledged: 'acknowledged',
  ack_voided: 'ack voided',
  snoozed: 'snoozed',
  unsnoozed: 'unsnoozed',
  closed: 'closed',
};

/** A route with no channel left reaches nobody. The API rejects creating
 * one, but deleting a channel removes the link rather than the route
 * (migration 00052's own ON DELETE CASCADE on alert_route_channels), so
 * a route can arrive here channel-less and keep looking configured while
 * being silent — the trap backlog 4.29 records for maintenance windows,
 * and the one decision 8 of the v0.4b design requires be a visible fact
 * rather than a silence. */
export function routeReachesNobody(route: AlertRoute): boolean {
  return (route.channels ?? []).length === 0;
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

export const WEEKDAY_OPTIONS: { value: Weekday; label: string }[] = [
  { value: 'mon', label: 'Mon' },
  { value: 'tue', label: 'Tue' },
  { value: 'wed', label: 'Wed' },
  { value: 'thu', label: 'Thu' },
  { value: 'fri', label: 'Fri' },
  { value: 'sat', label: 'Sat' },
  { value: 'sun', label: 'Sun' },
];

const DOW_NUMBER: Record<Weekday, number> = { sun: 0, mon: 1, tue: 2, wed: 3, thu: 4, fri: 5, sat: 6 };
const NUMBER_DOW: Record<number, Weekday> = Object.fromEntries(Object.entries(DOW_NUMBER).map(([day, n]) => [n, day as Weekday]));
const WEEKDAY_RANK: Record<Weekday, number> = Object.fromEntries(WEEKDAY_OPTIONS.map((d, i) => [d.value, i])) as Record<Weekday, number>;
const ALL_WEEKDAYS = WEEKDAY_OPTIONS.map((d) => d.value);
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
 * native format an `<input type="time">` both reads and writes. */
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
