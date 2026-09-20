import type { Tone } from '../../ui/badge.tsx';
import type { CalendarEntry, CalendarWindow } from '../../api/types.ts';

/** MAINTENANCE_COLOR draws maintenance windows (the mockup's violet — not
 * a state colour, so it cannot be mistaken for one). */
export const MAINTENANCE_COLOR = '#A78BFA';

export type CalendarView = 'week' | 'month' | 'list';

/** startOfWeek is Monday 00:00 (local time) of d's week. */
export function startOfWeek(d: Date): Date {
  const out = new Date(d.getFullYear(), d.getMonth(), d.getDate());
  const shift = (out.getDay() + 6) % 7; // Monday = 0
  out.setDate(out.getDate() - shift);
  return out;
}

export function addDays(d: Date, n: number): Date {
  const out = new Date(d);
  out.setDate(out.getDate() + n);
  return out;
}

/** monthOf is the month an anchor week belongs to: the month of its
 * Thursday (the ISO week rule), so a week starting on 31 August belongs to
 * September. */
export function monthOf(anchor: Date): Date {
  const mid = addDays(anchor, 3);
  return new Date(mid.getFullYear(), mid.getMonth(), 1);
}

/** rangeFor is what a view shows around anchor (a Monday): a week, or the
 * six weeks from the Monday on or before the first of the anchor's month
 * (the API's maximum range). */
export function rangeFor(view: CalendarView, anchor: Date): { from: Date; to: Date } {
  if (view === 'month') {
    const from = startOfWeek(monthOf(anchor));
    return { from, to: addDays(from, 42) };
  }
  return { from: anchor, to: addDays(anchor, 7) };
}

/** shift moves the anchor one view back or forth; in the month view to
 * the week of the 8th of the next or previous month, whose Thursday always
 * lies in that month. */
export function shift(view: CalendarView, anchor: Date, dir: 1 | -1): Date {
  if (view === 'month') {
    const m = monthOf(anchor);
    return startOfWeek(new Date(m.getFullYear(), m.getMonth() + dir, 8));
  }
  return addDays(anchor, 7 * dir);
}

/** entryTone colours an occurrence: planned muted, a run by its status,
 * skipped and missed as warnings, a failed start as critical. */
export function entryTone(e: CalendarEntry): Tone {
  switch (e.state) {
    case 'planned':
      return 'unknown';
    case 'triggered':
      if (e.job_status === 'succeeded') return 'ok';
      if (e.job_status === 'failed') return 'crit';
      if (e.job_status === 'cancelled') return 'warn';
      return 'accent';
    case 'failed':
      return 'crit';
    default:
      return 'warn';
  }
}

export const BANDS = [0, 3, 6, 9, 12, 15, 18, 21];

export interface CellItem {
  scheduleId: string;
  name: string;
  count: number;
  /** The last occurrence in the cell, what a click opens. */
  entry: CalendarEntry;
}

/** weekCells buckets entries into 7 days × 8 three-hour bands (local
 * time), one item per schedule and cell with a count.
 *
 * The day index comes from matching calendar dates (dayKey), not from
 * dividing a millisecond difference by a fixed 24h — a day the grid spans
 * is not always 24h of real time (a DST transition makes it 23h or 25h),
 * so that division silently misplaced an occurrence by one column when a
 * short day fell before the target within the displayed week. */
export function weekCells(entries: CalendarEntry[], start: Date): CellItem[][][] {
  const cells: CellItem[][][] = Array.from({ length: 7 }, () => BANDS.map(() => []));
  const dayIndex = new Map<string, number>();
  for (let i = 0; i < 7; i++) dayIndex.set(dayKey(addDays(start, i)), i);
  for (const e of entries) {
    const at = new Date(e.at);
    const day = dayIndex.get(dayKey(at));
    if (day === undefined) continue;
    const band = Math.floor(at.getHours() / 3);
    const cell = cells[day][band];
    const item = cell.find((c) => c.scheduleId === e.schedule_id);
    if (item) {
      item.count++;
      item.entry = e;
    } else {
      cell.push({ scheduleId: e.schedule_id, name: e.schedule_name, count: 1, entry: e });
    }
  }
  return cells;
}

/** windowsIn lists the windows overlapping [from, to). */
export function windowsIn(windows: CalendarWindow[], from: Date, to: Date): CalendarWindow[] {
  return windows.filter((w) => new Date(w.start) < to && new Date(w.end) > from);
}

/** dayEntries groups entries by local day (yyyy-mm-dd). */
export function dayKey(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}
