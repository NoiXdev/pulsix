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
 * Presentational only — the events come from the run detail page's live
 * log hook.
 */
export function EventLog({ events }: { events: readonly JobEvent[] }) {
  if (events.length === 0) {
    return <p className="px-4 py-6 text-center text-xs text-muted-foreground">No events yet.</p>;
  }
  return (
    <ol aria-label="Run events" className="flex flex-col gap-0.5 overflow-x-auto px-4 py-3">
      {events.map((e) => {
        const text = typeof e.payload.text === 'string' ? (e.payload.text as string) : null;
        if (text !== null && text !== '') {
          return (
            <li key={e.seq} className="whitespace-pre-wrap break-all font-mono text-[11px] leading-5 text-foreground">
              {text}
            </li>
          );
        }
        const color = STATE_COLOR[e.state] ?? 'var(--px-unknown)';
        return (
          <li key={e.seq} className="flex items-start gap-2 font-mono text-[11px] leading-5">
            <span style={{ color }}>{e.state}</span>
            {e.host && <span className="text-muted-foreground">{e.host}</span>}
            {e.task && <span className="break-all text-foreground">{e.task}</span>}
            {text === '' && <span className="text-muted-foreground">(empty stdout)</span>}
          </li>
        );
      })}
    </ol>
  );
}
