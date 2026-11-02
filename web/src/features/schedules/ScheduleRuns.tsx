import { Pill } from '../../ui/badge.tsx';
import { useScheduleRuns } from '../../api/hooks/useSchedules.ts';
import { OUTCOME_TONE, outcomeLabel } from './outcome.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';

/** ScheduleRuns lists a schedule's newest recorded decisions: when, what
 * happened and why (the reason never carries variables or output). */
export function ScheduleRuns({ org, id }: { org: string; id: string }) {
  const t = useT();
  const fmt = useFormat();
  const runs = useScheduleRuns(org, id);
  const list = runs.data?.runs ?? [];
  return (
    <div className="flex flex-col gap-1.5">
      <h3 className="text-xs font-semibold">{t.schedules.runs.title}</h3>
      {list.length === 0 && <p className="text-xs text-muted-foreground">{t.schedules.runs.none}</p>}
      <ul className="flex flex-col gap-1">
        {list.map((r) => (
          <li key={r.id} className="flex items-center gap-2 text-xs">
            <Pill tone={OUTCOME_TONE[r.outcome] ?? 'unknown'}>{outcomeLabel(r.outcome, t)}</Pill>
            <span className="text-muted-foreground">{fmt.dateTime(r.decided_at)}</span>
            {r.reason && <span className="truncate">{r.reason}</span>}
          </li>
        ))}
      </ul>
    </div>
  );
}
