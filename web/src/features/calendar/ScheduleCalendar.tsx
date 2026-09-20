import { useState } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { ChevronLeft, ChevronRight } from 'lucide-react';
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
const dayLabel = (d: Date) => d.toLocaleDateString(undefined, { weekday: 'short', day: 'numeric' });

/** ScheduleCalendar is the Scheduler artboard's calendar (spec 2026-09-18
 * §8, mockup "Week · 8–14 Sep"): scheduled runs — planned ones muted, past
 * ones coloured by their result — and maintenance windows as violet bars,
 * as a week grid, a month grid or a list. Clicking an occurrence opens its
 * run, or the schedule when no run exists. */
export function ScheduleCalendar({ org }: { org: string }) {
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
      ? monthOf(anchor).toLocaleDateString(undefined, { month: 'long', year: 'numeric' })
      : `Week · ${from.toLocaleDateString(undefined, { day: 'numeric', month: 'short' })} – ${addDays(to, -1).toLocaleDateString(undefined, { day: 'numeric', month: 'short' })}`;

  return (
    <section aria-label="Calendar" className="flex flex-col overflow-hidden rounded-[10px] border border-border bg-card">
      <div className="flex items-center gap-2 border-b border-border px-4 py-2.5">
        <Button type="button" variant="ghost" aria-label="Previous" onClick={() => setAnchor(shift(view, anchor, -1))}>
          <ChevronLeft size={14} />
        </Button>
        <h2 className="text-[13px] font-semibold">{title}</h2>
        <Button type="button" variant="ghost" aria-label="Next" onClick={() => setAnchor(shift(view, anchor, 1))}>
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
              {v}
            </button>
          ))}
        </div>
        <span className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
          <span className="h-1.5 w-1.5 rounded-full" style={{ backgroundColor: MAINTENANCE_COLOR }} /> maintenance
        </span>
      </div>
      {cal.data?.truncated && <p className="px-4 pt-2 text-[11px] text-muted-foreground">Some very frequent schedules or maintenance windows are shown in part.</p>}
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
  return <Chip label={`Maintenance · ${w.name}`} color={MAINTENANCE_COLOR} />;
}

function WeekGrid({ start, entries, windows, onOpen }: { start: Date; entries: CalendarEntry[]; windows: CalendarWindow[]; onOpen: (e: CalendarEntry) => void }) {
  const cells = weekCells(entries, start);
  const days = Array.from({ length: 7 }, (_, i) => addDays(start, i));
  const today = dayKey(new Date());
  return (
    <div className="grid gap-1 px-4 py-3 text-[10px] text-muted-foreground" style={{ gridTemplateColumns: '44px repeat(7, minmax(0, 1fr))' }}>
      <span />
      {days.map((d) => (
        <span key={d.toISOString()} className={`text-center font-semibold ${dayKey(d) === today ? 'text-primary' : ''}`}>
          {dayLabel(d)}
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
              {inWindow && <span aria-label="maintenance" className="h-1.5 w-1.5 rounded-full" style={{ backgroundColor: MAINTENANCE_COLOR }} />}
            </span>
            {names.slice(0, 3).map((e) => (
              <Chip key={e.schedule_id} label={e.schedule_name} color={TONE_VAR[entryTone(e)]} onClick={() => onOpen(e)} />
            ))}
            {names.length > 3 && <span className="text-muted-foreground">+{names.length - 3} more</span>}
          </div>
        );
      })}
    </div>
  );
}

function ListView({ entries, windows, onOpen }: { entries: CalendarEntry[]; windows: CalendarWindow[]; onOpen: (e: CalendarEntry) => void }) {
  type Row = { at: Date; entry?: CalendarEntry; window?: CalendarWindow };
  const rows: Row[] = [...entries.map((e) => ({ at: new Date(e.at), entry: e })), ...windows.map((w) => ({ at: new Date(w.start), window: w }))].sort(
    (a, b) => a.at.getTime() - b.at.getTime(),
  );
  if (rows.length === 0) return <p className="px-4 py-6 text-center text-xs text-muted-foreground">Nothing scheduled in this range.</p>;
  return (
    <ul className="flex flex-col divide-y divide-border">
      {rows.map((r, i) => (
        <li key={i} className="grid grid-cols-[150px_minmax(0,1fr)_150px] items-center gap-3 px-4 py-2 text-xs" data-list-row>
          <span className="font-mono text-muted-foreground">{r.at.toLocaleString(undefined, { dateStyle: 'short', timeStyle: 'short' })}</span>
          {r.entry ? (
            <button type="button" className="truncate text-left hover:text-primary" onClick={() => onOpen(r.entry!)}>
              {r.entry.schedule_name}
            </button>
          ) : (
            <span className="truncate" style={{ color: MAINTENANCE_COLOR }}>
              Maintenance · {r.window!.name} (until {new Date(r.window!.end).toLocaleString(undefined, { dateStyle: 'short', timeStyle: 'short' })})
            </span>
          )}
          <span>{r.entry && <Pill tone={entryTone(r.entry)}>{r.entry.state === 'triggered' ? r.entry.job_status ?? 'triggered' : r.entry.state}</Pill>}</span>
        </li>
      ))}
    </ul>
  );
}
