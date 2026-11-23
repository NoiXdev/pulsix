import { memo } from 'react';
import type { JobEvent } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { eventDiffs } from './eventDiff.ts';
import { EventDiffList } from './EventDiffList.tsx';

const STATE_COLOR: Record<string, string> = {
  ok: 'var(--px-ok)',
  changed: 'var(--px-accent)',
  failed: 'var(--px-crit)',
  unreachable: 'var(--px-crit)',
  skipped: 'var(--px-unknown)',
};

/**
 * EventLog renders a run's streamed events in JetBrains Mono: bare stdout
 * lines as plain text, and task/ok/changed/failed/skipped/unreachable
 * events in the state colours with the host and task that produced them,
 * their display text (already redacted by the worker) beneath, and the
 * differences a task reported (`--diff`, eventDiff.ts) as unified diffs. A
 * loop's own event repeats its items' differences, which the items' events
 * already show, so it is read without `loopResults`.
 * A job the manager requeued streams every attempt into the same log (seq
 * keeps increasing across attempts); once the log spans more than one
 * attempt, each attempt starts under its own "Attempt N" divider.
 * Presentational only — the events come from the run detail page's live
 * log hook.
 */
export function EventLog({ events }: { events: readonly JobEvent[] }) {
  const t = useT();
  if (events.length === 0) {
    return <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.runs.events.empty}</p>;
  }
  const multiAttempt = new Set(events.map((e) => e.attempt)).size > 1;
  return (
    <ol aria-label={t.runs.events.label} className="flex flex-col gap-0.5 overflow-x-auto px-4 py-3">
      {events.flatMap((e, i) => {
        const rows = [];
        if (multiAttempt && (i === 0 || events[i - 1].attempt !== e.attempt)) {
          rows.push(
            <li key={`attempt-${e.attempt}-${e.seq}`} className="mt-2 border-t border-border pt-1 text-[11px] font-medium text-muted-foreground first:mt-0 first:border-t-0 first:pt-0">
              {t.runs.events.attempt(e.attempt)}
            </li>,
          );
        }
        rows.push(<EventRow key={e.seq} e={e} />);
        return rows;
      })}
    </ol>
  );
}

/** One event's row. Memoised on the event object: the log hook keeps each
 * event's object across merges (mergeEvents copies the array, not the
 * events), so `e` is the only prop and is stable, and a log of thousands of
 * rows renders only the one an arriving event adds. */
const EventRow = memo(function EventRow({ e }: { e: JobEvent }) {
  const t = useT();
  const text = typeof e.payload.text === 'string' ? (e.payload.text as string) : null;
  if (text !== null && text !== '') {
    return <li className="whitespace-pre-wrap break-all font-mono text-[11px] leading-5 text-foreground">{text}</li>;
  }
  const color = STATE_COLOR[e.state] ?? 'var(--px-unknown)';
  // A task event carries its own display text (the worker forwards each
  // ansible event once); it renders under the state line.
  const stdout = typeof e.payload.stdout === 'string' ? (e.payload.stdout as string) : '';
  const diffs = eventDiffs(e.payload);
  return (
    <li className="flex flex-col font-mono text-[11px] leading-5">
      <span className="flex items-start gap-2">
        <span style={{ color }}>{e.state}</span>
        {e.host && <span className="text-muted-foreground">{e.host}</span>}
        {e.task && <span className="break-all text-foreground">{e.task}</span>}
        {text === '' && <span className="text-muted-foreground">{t.runs.events.emptyStdout}</span>}
      </span>
      {stdout !== '' && <span className="whitespace-pre-wrap break-all pl-4 text-muted-foreground">{stdout}</span>}
      {diffs.length > 0 && (
        <div className="flex flex-col gap-1 py-1 pl-4 font-sans">
          <EventDiffList diffs={diffs} />
        </div>
      )}
    </li>
  );
});
