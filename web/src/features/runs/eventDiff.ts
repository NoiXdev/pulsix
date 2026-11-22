/**
 * The differences a run event reports (ansible's `--diff`): an event's
 * payload is the raw ansible-runner event, whose `event_data.res.diff` is
 * one object or a list of them, each either `before`/`after` (with optional
 * `before_header`/`after_header`) or `prepared`, a ready unified diff. The
 * dry run's result and the run log render them alike (ui/diff).
 */

export type EventDiff =
  | { kind: 'prepared'; text: string }
  | { kind: 'compare'; before: string; after: string; beforeHeader?: string; afterHeader?: string };

const isMap = (v: unknown): v is Record<string, unknown> => v !== null && typeof v === 'object' && !Array.isArray(v);

/** A structured side (a file's state, a dict) reads as JSON with sorted
 * keys, the way ansible prints it. */
function sideText(v: unknown): string {
  if (v === undefined || v === null) return '';
  if (typeof v === 'string') return v;
  return `${JSON.stringify(v, (_key, value: unknown) => (isMap(value) ? Object.fromEntries(Object.entries(value).sort(([a], [b]) => a.localeCompare(b))) : value), 2)}\n`;
}

function entryOf(raw: unknown): EventDiff | null {
  if (!isMap(raw)) return null;
  if (typeof raw.prepared === 'string') return raw.prepared === '' ? null : { kind: 'prepared', text: raw.prepared };
  if (!('before' in raw) && !('after' in raw)) return null;
  const before = sideText(raw.before);
  const after = sideText(raw.after);
  if (before === after) return null;
  const out: EventDiff = { kind: 'compare', before, after };
  if (typeof raw.before_header === 'string') out.beforeHeader = raw.before_header;
  if (typeof raw.after_header === 'string') out.afterHeader = raw.after_header;
  return out;
}

function diffsOf(res: unknown): EventDiff[] {
  const diff = isMap(res) ? res.diff : undefined;
  const list = Array.isArray(diff) ? diff : isMap(diff) ? [diff] : [];
  return list.map(entryOf).filter((d): d is EventDiff => d !== null);
}

/** eventDiffs answers the differences an event's payload carries, none
 * when it carries none (or none that differ). A loop's own event has no
 * `res.diff`: its items' differences sit in `res.results[]`, which the
 * items' events carry again — read only with `loopResults`, for a loop
 * whose item events are missing. */
export function eventDiffs(payload: Record<string, unknown> | null | undefined, opts: { loopResults?: boolean } = {}): EventDiff[] {
  const data = payload?.event_data;
  const res = isMap(data) ? data.res : undefined;
  const own = diffsOf(res);
  if (own.length > 0 || !opts.loopResults || !isMap(res) || !Array.isArray(res.results)) return own;
  return res.results.flatMap(diffsOf);
}
