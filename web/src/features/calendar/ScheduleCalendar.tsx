import { useState } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { ChevronLeft, ChevronRight } from 'lucide-react';
import { useT } from '../../i18n/useT.ts';
import { useFormat } from '../../i18n/format.ts';
import { Button } from '../../ui/button.tsx';
import { Pill, type Tone } from '../../ui/badge.tsx';
import { useCalendar } from '../../api/hooks/useCalendar.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import type { CalendarEntry, CalendarWindow } from '../../api/types.ts';
import { BANDS, MAINTENANCE_COLOR, type CalendarView, addDays, dayKey, entryTone, monthOf, rangeFor, shift, startOfWeek, weekCells, windowsIn } from './calendar.ts';

// entryTone never returns 'maintenance' (that tone is reserved for a
// window's own status elsewhere), but Tone includes it, so the map needs
// an entry for every member to stay a total function from Tone to colour.
const TONE_VAR: Record<Tone, string> = {
  ok: 'var(--px-ok)', warn: 'var(--px-warn)', crit: 'var(--px-crit)', unknown: 'var(--px-unknown)', accent: 'var(--px-accent)', maintenance: MAINTENANCE_COLOR,
};
const gridZone = Intl.DateTimeFormat().resolvedOptions().timeZone;

/** ScheduleCalendar is the Scheduler artboard's calendar (spec 2026-09-18
 * §8, mockup "Week · 8–14 Sep"): scheduled runs — planned ones muted, past
 * ones coloured by their result — and maintenance windows as violet bars,
 * as a week grid, a month grid or a list. Clicking an occurrence opens its
 * run, or the schedule when no run exists. */
export function ScheduleCalendar({ org }: { org: string }) {
  const t = useT();
  const fmt = useFormat();
  const navigate = useNavigate();
  const [view, setView] = useState<CalendarView>('week');
  const [anchor, setAnchor] = useState(() => startOfWeek(new Date()));
  const { from, to } = rangeFor(view, anchor);
  const cal = useCalendar(org, from.toISOString(), to.toISOString());
  // Without this guard a refused or failed range rendered as a complete
  // but empty grid — no error, no toast, no retry hint — so the operator
  // read "nothing is scheduled and nothing is in maintenance" off a
  // request that never succeeded (final review, Critical #1).
  const guard = useQueryGuard(cal);
  const entries = cal.data?.entries ?? [];
  const windows = cal.data?.maintenance ?? [];

  function open(e: CalendarEntry) {
    if (e.job_id) void navigate({ to: '/o/$org/runs/$id', params: { org, id: e.job_id } });
    else void navigate({ to: '/o/$org/schedules', params: { org }, search: { id: e.schedule_id, template: undefined } });
  }

  if (guard) return guard;

  const title =
    view === 'month'
      ? fmt.date(monthOf(anchor), { month: 'long', year: 'numeric' })
      : t.calendar.weekTitle(
          fmt.date(from, { day: 'numeric', month: 'short' }),
          fmt.date(addDays(to, -1), { day: 'numeric', month: 'short' }),
        );

  return (
    <section aria-label={t.calendar.label} className="flex flex-col overflow-hidden rounded-[10px] border border-border bg-card">
      <div className="flex items-center gap-2 border-b border-border px-4 py-2.5">
        <Button type="button" variant="ghost" aria-label={t.calendar.previous} onClick={() => setAnchor(shift(view, anchor, -1))}>
          <ChevronLeft size={14} />
        </Button>
        <h2 className="text-[13px] font-semibold">{title}</h2>
        {/* The grid buckets every occurrence by its hour in THIS
            browser's zone (calendar.ts's weekCells), so a schedule set
            to 03:00 somewhere else sits in a different band for every
            operator looking at it. Laying the grid out in a fixed zone
            is its own feature (backlog 2.77) and needs a zone to lay it
            out in, which an organization does not yet carry; saying
            which zone this is costs nothing and removes the ambiguity
            (backlog 2.88). */}
        <span className="text-[11px] text-muted-foreground">{t.calendar.timesIn(gridZone)}</span>
        <Button type="button" variant="ghost" aria-label={t.calendar.next} onClick={() => setAnchor(shift(view, anchor, 1))}>
          <ChevronRight size={14} />
        </Button>
        <div role="tablist" className="ml-auto flex gap-1">
          {(['week', 'month', 'list'] as const).map((v) => (
            <button
              key={v}
              type="button"
              role="tab"
              aria-selected={view === v}
              className={`rounded-md border px-2.5 py-1 text-xs capitalize ${view === v ? 'border-primary text-primary' : 'border-border text-muted-foreground'}`}
              onClick={() => setView(v)}
            >
              {t.calendar.views[v]}
            </button>
          ))}
        </div>
        <span className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
          <span className="h-1.5 w-1.5 rounded-full" style={{ backgroundColor: MAINTENANCE_COLOR }} /> {t.calendar.maintenance}
        </span>
      </div>
      {cal.data?.truncated && <p className="px-4 pt-2 text-[11px] text-muted-foreground">{t.calendar.truncated}</p>}
      {view === 'week' && <WeekGrid start={from} entries={entries} windows={windows} onOpen={open} />}
      {view === 'month' && <MonthGrid start={from} month={monthOf(anchor).getMonth()} entries={entries} windows={windows} onOpen={open} />}
      {view === 'list' && <ListView entries={entries} windows={windows} onOpen={open} />}
    </section>
  );
}

function Chip({ label, color, onClick }: { label: string; color: string; onClick?: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="truncate rounded-[3px] px-1 text-left text-[9px]"
      style={{ color, backgroundColor: `color-mix(in srgb, ${color} 13%, transparent)` }}
    >
      {label}
    </button>
  );
}

function WindowBar({ w }: { w: CalendarWindow }) {
  const t = useT();
  return <Chip label={t.calendar.windowChip(w.name)} color={MAINTENANCE_COLOR} />;
}

function WeekGrid({ start, entries, windows, onOpen }: { start: Date; entries: CalendarEntry[]; windows: CalendarWindow[]; onOpen: (e: CalendarEntry) => void }) {
  const fmt = useFormat();
  const cells = weekCells(entries, start);
  const days = Array.from({ length: 7 }, (_, i) => addDays(start, i));
  const today = dayKey(new Date());
  return (
    <div className="grid gap-1 px-4 py-3 text-[10px] text-muted-foreground" style={{ gridTemplateColumns: '44px repeat(7, minmax(0, 1fr))' }}>
      <span />
      {days.map((d) => (
        <span key={d.toISOString()} className={`text-center font-semibold ${dayKey(d) === today ? 'text-primary' : ''}`}>
          {fmt.date(d, { weekday: 'short', day: 'numeric' })}
        </span>
      ))}
      {BANDS.map((h, band) => (
        <WeekRow key={h} hour={h} band={band} days={days} cells={cells} windows={windows} onOpen={onOpen} />
      ))}
    </div>
  );
}

function WeekRow({ hour, band, days, cells, windows, onOpen }: {
  hour: number; band: number; days: Date[]; cells: ReturnType<typeof weekCells>; windows: CalendarWindow[]; onOpen: (e: CalendarEntry) => void;
}) {
  return (
    <>
      <span className="font-mono">{String(hour).padStart(2, '0')}:00</span>
      {days.map((d, day) => {
        const from = new Date(d.getFullYear(), d.getMonth(), d.getDate(), hour);
        const to = new Date(d.getFullYear(), d.getMonth(), d.getDate(), hour + 3);
        return (
          <div key={day} data-cell={`${day}-${band}`} className="flex min-h-[26px] flex-col gap-0.5 rounded border border-border bg-background p-0.5">
            {cells[day][band].map((item) => (
              <Chip
                key={item.scheduleId}
                label={item.count > 1 ? `${item.name} ×${item.count}` : item.name}
                color={TONE_VAR[entryTone(item.entry)]}
                onClick={() => onOpen(item.entry)}
              />
            ))}
            {windowsIn(windows, from, to).map((w) => (
              <WindowBar key={w.window_id + w.start} w={w} />
            ))}
          </div>
        );
      })}
    </>
  );
}

function MonthGrid({ start, month, entries, windows, onOpen }: {
  start: Date; month: number; entries: CalendarEntry[]; windows: CalendarWindow[]; onOpen: (e: CalendarEntry) => void;
}) {
  const t = useT();
  const days = Array.from({ length: 42 }, (_, i) => addDays(start, i));
  const byDay = new Map<string, CalendarEntry[]>();
  for (const e of entries) {
    const k = dayKey(new Date(e.at));
    byDay.set(k, [...(byDay.get(k) ?? []), e]);
  }
  return (
    <div className="grid grid-cols-7 gap-1 px-4 py-3 text-[10px]">
      {days.map((d) => {
        const list = byDay.get(dayKey(d)) ?? [];
        const names = [...new Map(list.map((e) => [e.schedule_id, e])).values()];
        const inWindow = windowsIn(windows, d, addDays(d, 1)).length > 0;
        return (
          <div key={d.toISOString()} data-day={dayKey(d)} className={`flex min-h-[64px] flex-col gap-0.5 rounded border border-border p-1 ${d.getMonth() === month ? 'bg-background' : 'opacity-50'}`}>
            <span className="flex items-center justify-between text-muted-foreground">
              {d.getDate()}
              {inWindow && <span aria-label={t.calendar.maintenance} className="h-1.5 w-1.5 rounded-full" style={{ backgroundColor: MAINTENANCE_COLOR }} />}
            </span>
            {names.slice(0, 3).map((e) => (
              <Chip key={e.schedule_id} label={e.schedule_name} color={TONE_VAR[entryTone(e)]} onClick={() => onOpen(e)} />
            ))}
            {names.length > 3 && <span className="text-muted-foreground">{t.calendar.more(names.length - 3)}</span>}
          </div>
        );
      })}
    </div>
  );
}

function ListView({ entries, windows, onOpen }: { entries: CalendarEntry[]; windows: CalendarWindow[]; onOpen: (e: CalendarEntry) => void }) {
  const t = useT();
  const fmt = useFormat();
  type Row = { at: Date; entry?: CalendarEntry; window?: CalendarWindow };
  const rows: Row[] = [...entries.map((e) => ({ at: new Date(e.at), entry: e })), ...windows.map((w) => ({ at: new Date(w.start), window: w }))].sort(
    (a, b) => a.at.getTime() - b.at.getTime(),
  );
  if (rows.length === 0) return <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.calendar.empty}</p>;
  return (
    <ul className="flex flex-col divide-y divide-border">
      {rows.map((r, i) => (
        <li key={i} className="grid grid-cols-[150px_minmax(0,1fr)_150px] items-center gap-3 px-4 py-2 text-xs" data-list-row>
          <span className="font-mono text-muted-foreground">{fmt.dateTime(r.at, { dateStyle: 'short', timeStyle: 'short' })}</span>
          {r.entry ? (
            <button type="button" className="truncate text-left hover:text-primary" onClick={() => onOpen(r.entry!)}>
              {r.entry.schedule_name}
            </button>
          ) : (
            <span className="truncate" style={{ color: MAINTENANCE_COLOR }}>
              {t.calendar.windowUntil(r.window!.name, fmt.dateTime(r.window!.end, { dateStyle: 'short', timeStyle: 'short' }))}
            </span>
          )}
          <span>{r.entry && <Pill tone={entryTone(r.entry)}>{r.entry.state === 'triggered' ? (r.entry.job_status ? t.calendar.jobStatus[r.entry.job_status] : t.calendar.state.triggered) : t.calendar.state[r.entry.state]}</Pill>}</span>
        </li>
      ))}
    </ul>
  );
}
