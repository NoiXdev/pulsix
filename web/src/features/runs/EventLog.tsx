import type { JobEvent } from '../../api/types.ts';

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
 * events in the state colours with the host and task that produced them.
 * A job the manager requeued streams every attempt into the same log (seq
 * keeps increasing across attempts); once the log spans more than one
 * attempt, each attempt starts under its own "Attempt N" divider.
 * Presentational only — the events come from the run detail page's live
 * log hook.
 */
export function EventLog({ events }: { events: readonly JobEvent[] }) {
  if (events.length === 0) {
    return <p className="px-4 py-6 text-center text-xs text-muted-foreground">No events yet.</p>;
  }
  const multiAttempt = new Set(events.map((e) => e.attempt)).size > 1;
  return (
    <ol aria-label="Run events" className="flex flex-col gap-0.5 overflow-x-auto px-4 py-3">
      {events.flatMap((e, i) => {
        const rows = [];
        if (multiAttempt && (i === 0 || events[i - 1].attempt !== e.attempt)) {
          rows.push(
            <li key={`attempt-${e.attempt}-${e.seq}`} className="mt-2 border-t border-border pt-1 text-[11px] font-medium text-muted-foreground first:mt-0 first:border-t-0 first:pt-0">
              Attempt {e.attempt}
            </li>,
          );
        }
        rows.push(<EventRow key={e.seq} e={e} />);
        return rows;
      })}
    </ol>
  );
}

function EventRow({ e }: { e: JobEvent }) {
  const text = typeof e.payload.text === 'string' ? (e.payload.text as string) : null;
  if (text !== null && text !== '') {
    return <li className="whitespace-pre-wrap break-all font-mono text-[11px] leading-5 text-foreground">{text}</li>;
  }
  const color = STATE_COLOR[e.state] ?? 'var(--px-unknown)';
  return (
    <li className="flex items-start gap-2 font-mono text-[11px] leading-5">
      <span style={{ color }}>{e.state}</span>
      {e.host && <span className="text-muted-foreground">{e.host}</span>}
      {e.task && <span className="break-all text-foreground">{e.task}</span>}
      {text === '' && <span className="text-muted-foreground">(empty stdout)</span>}
    </li>
  );
}
