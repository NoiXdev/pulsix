import { UnifiedDiff } from '../../ui/diff/UnifiedDiff.tsx';
import type { EventDiff } from './eventDiff.ts';

/** EventDiffList renders the differences a task reported (eventDiff.ts),
 * each as a unified diff: before/after compared, a prepared one as it is.
 * The run log and the playbook's latest dry run show them alike. */
export function EventDiffList({ diffs }: { diffs: readonly EventDiff[] }) {
  return (
    <>
      {diffs.map((d, i) =>
        d.kind === 'prepared' ? (
          <UnifiedDiff key={i} prepared={d.text} />
        ) : (
          <UnifiedDiff key={i} before={d.before} after={d.after} beforeHeader={d.beforeHeader} afterHeader={d.afterHeader} />
        ),
      )}
    </>
  );
}
