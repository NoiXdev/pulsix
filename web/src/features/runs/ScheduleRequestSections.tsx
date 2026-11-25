import { Card } from '../../ui/table.tsx';
import { Section } from '../../ui/detail-layout.tsx';
import type { RunPicture } from '../../api/hooks/useRunRequests.ts';
import { useT } from '../../i18n/useT.ts';
import { varText } from './pictureVars.ts';

export type PictureSchedule = NonNullable<RunPicture['schedule']>;
type ScheduleState = NonNullable<PictureSchedule['new']>;

const TABLE = 'w-full text-xs';
const TH = 'px-4 py-2 text-left text-[11px] font-semibold tracking-wide text-muted-foreground uppercase';
const TD = 'border-t border-border px-4 py-2 align-top';

/**
 * ScheduleRequestSections is the picture of a schedule request (spec
 * 2026-11-25 C): what the approval does (create, change or resume), the
 * schedule as it is now next to how the approval leaves it, with every
 * setting that changes marked, secrets masked. It says when the limit and
 * extra vars are the assistant's, and when the schedule is gone.
 */
export function ScheduleRequestSections({ schedule }: { schedule: PictureSchedule }) {
  const t = useT();
  const d = t.runs.requests.detail;
  const sd = d.schedule;
  const { action, current } = schedule;
  // A resume leaves the schedule as it is, active again.
  const next: ScheduleState | undefined = schedule.new ?? (action === 'resume' && current ? { ...current, paused: false } : undefined);
  const columns: { key: string; label: string; state: ScheduleState }[] = [];
  if (current) columns.push({ key: 'current', label: sd.current, state: current });
  if (next) columns.push({ key: 'next', label: sd.next, state: next });

  const vars = (s: ScheduleState) => {
    const entries = Object.entries(s.extra_vars ?? {});
    if (entries.length === 0) return sd.none;
    return entries
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([k, v]) => `${k}: ${varText(v, d.secret)}`)
      .join('\n');
  };
  const rows: {
    key: string;
    label: string;
    value: (s: ScheduleState) => string;
    mono?: boolean;
  }[] = [
    { key: 'template', label: sd.template, value: (s) => s.template_name },
    { key: 'name', label: sd.name, value: (s) => s.name },
    {
      key: 'timetable',
      label: sd.timetable,
      value: (s) => [s.description, s.cron].filter(Boolean).join('\n'),
    },
    {
      key: 'timezone',
      label: sd.timezone,
      value: (s) => s.timezone,
      mono: true,
    },
    {
      key: 'overlap',
      label: sd.overlap,
      value: (s) => sd.overlapValue[s.guards.overlap || 'skip'] ?? s.guards.overlap,
    },
    {
      key: 'catchUp',
      label: sd.catchUp,
      value: (s) => (s.guards.catch_up?.enabled ? sd.catchUpOn(s.guards.catch_up.window) : sd.catchUpOff),
    },
    {
      key: 'maintenance',
      label: sd.maintenance,
      value: (s) => sd.maintenanceValue[s.guards.maintenance || 'ignore'] ?? s.guards.maintenance ?? '',
    },
    {
      key: 'limit',
      label: sd.limit,
      value: (s) => s.limit ?? sd.templateLimit,
      mono: true,
    },
    { key: 'vars', label: sd.extraVars, value: vars, mono: true },
    {
      key: 'state',
      label: sd.state,
      value: (s) => (s.paused ? sd.paused : sd.active),
    },
  ];

  return (
    <Section id="schedule" title={sd.section}>
      <div className="flex flex-col gap-3">
        {schedule.gone && (
          <p role="alert" className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
            {sd.gone}
          </p>
        )}
        <p className="text-xs text-muted-foreground">{sd.intro[action]}</p>
        {schedule.assistant_overrides && (
          <p role="note" className="rounded-md border border-[var(--px-warn)] px-3 py-2 text-xs text-[var(--px-warn)]">
            {sd.assistantValues}
          </p>
        )}
        {columns.length > 0 && (
          <Card>
            <table className={TABLE}>
              <thead>
                <tr>
                  <th className={TH}>{sd.field}</th>
                  {columns.map((c) => (
                    <th key={c.key} className={TH}>
                      {c.label}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => {
                  const values = columns.map((c) => row.value(c.state));
                  const changes = values.length === 2 && values[0] !== values[1];
                  return (
                    <tr key={row.key}>
                      <th scope="row" className={`${TD} text-left font-normal text-muted-foreground`}>
                        {row.label}
                      </th>
                      {values.map((v, i) => (
                        <td key={columns[i].key} className={`${TD} whitespace-pre-line break-all ${row.mono ? 'font-mono' : ''}`}>
                          {v}
                          {changes && i === 1 && <span className="ml-2 font-sans text-[11px] whitespace-nowrap text-[var(--px-warn)]">{sd.changedMark}</span>}
                        </td>
                      ))}
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </Card>
        )}
      </div>
    </Section>
  );
}
