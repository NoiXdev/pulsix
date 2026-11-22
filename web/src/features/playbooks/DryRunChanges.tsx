import { useId } from 'react';
import { UnifiedDiff } from '../../ui/diff/UnifiedDiff.tsx';
import type { JobEvent } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import type { RecapCounts } from '../runs/hostCounts.ts';
import { changedTasks, COUNT_KEYS } from './dryRunTasks.ts';

const COUNT_COLOR: Record<(typeof COUNT_KEYS)[number], string> = {
  ok: 'var(--px-ok)',
  changed: 'var(--px-accent)',
  skipped: 'var(--px-unknown)',
  failed: 'var(--px-crit)',
  unreachable: 'var(--px-crit)',
};

/** DryRunCounts is the dry run's tiles: ok / would change / skipped /
 * failed / unreachable, summed over its hosts. */
export function DryRunCounts({ counts }: { counts: RecapCounts }) {
  const t = useT();
  const l = t.playbooks.page.dryRun.latest;
  return (
    <ul aria-label={l.countsLabel} className="grid grid-cols-2 gap-2 sm:grid-cols-5">
      {COUNT_KEYS.map((k) => (
        <li key={k} className="flex flex-col rounded-md border border-border bg-background px-3 py-2">
          <span className="font-mono text-lg font-semibold" style={{ color: counts[k] > 0 ? COUNT_COLOR[k] : undefined }}>
            {counts[k]}
          </span>
          <span className="text-[11px] text-muted-foreground">{l.counts[k]}</span>
        </li>
      ))}
    </ul>
  );
}

/**
 * DryRunChanges lists each task a dry run would change, per host, once —
 * a loop's items and its own event fold into one (dryRunTasks.ts) — with
 * the differences the task reported:
 * before/after compared line by line, a prepared diff as it is, a long one
 * collapsed (ui/diff). `final` says the run has ended, so "nothing would
 * change" is the answer rather than "nothing yet".
 */
export function DryRunChanges({ events, final }: { events: readonly JobEvent[]; final: boolean }) {
  const t = useT();
  const l = t.playbooks.page.dryRun.latest;
  const headingId = useId();
  const changed = changedTasks(events);
  if (changed.length === 0) return final ? <p className="text-xs text-muted-foreground">{l.noChanges}</p> : null;
  return (
    <>
      <h3 id={headingId} className="text-xs font-semibold tracking-wide text-muted-foreground uppercase">
        {l.changes}
      </h3>
      <ul aria-labelledby={headingId} className="flex flex-col gap-3">
        {changed.map((c) => (
          <li key={c.key} className="flex min-w-0 flex-col gap-1.5">
            <h4 className="text-xs font-medium break-words">{[c.task, c.host, l.wouldChange].filter(Boolean).join(' · ')}</h4>
            {c.diffs.length === 0 ? (
              <p className="text-[11px] text-muted-foreground">{l.noDiff}</p>
            ) : (
              c.diffs.map((d, i) =>
                d.kind === 'prepared' ? (
                  <UnifiedDiff key={i} prepared={d.text} />
                ) : (
                  <UnifiedDiff key={i} before={d.before} after={d.after} beforeHeader={d.beforeHeader} afterHeader={d.afterHeader} />
                ),
              )
            )}
          </li>
        ))}
      </ul>
    </>
  );
}
