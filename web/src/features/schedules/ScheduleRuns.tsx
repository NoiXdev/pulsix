import { Pill } from '../../ui/badge.tsx';
import { useScheduleRuns } from '../../api/hooks/useSchedules.ts';
import { OUTCOME_TONE } from './outcome.ts';

/** ScheduleRuns lists a schedule's newest recorded decisions: when, what
 * happened and why (the reason never carries variables or output). */
export function ScheduleRuns({ org, id }: { org: string; id: string }) {
  const runs = useScheduleRuns(org, id);
  const list = runs.data?.runs ?? [];
  return (
    <div className="flex flex-col gap-1.5">
      <h3 className="text-xs font-semibold">Recent runs</h3>
      {list.length === 0 && <p className="text-xs text-muted-foreground">No runs yet.</p>}
      <ul className="flex flex-col gap-1">
        {list.map((r) => (
          <li key={r.id} className="flex items-center gap-2 text-xs">
            <Pill tone={OUTCOME_TONE[r.outcome] ?? 'unknown'}>{r.outcome}</Pill>
            <span className="text-muted-foreground">{new Date(r.decided_at).toLocaleString()}</span>
            {r.reason && <span className="truncate">{r.reason}</span>}
          </li>
        ))}
      </ul>
    </div>
  );
}
