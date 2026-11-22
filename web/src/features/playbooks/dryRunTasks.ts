import type { JobEvent } from '../../api/types.ts';
import { eventDiffs, type EventDiff } from '../runs/eventDiff.ts';
import { ZERO_COUNTS, type RecapCounts } from '../runs/hostCounts.ts';

/**
 * A run's events folded into one result per task and host. A loop reports
 * each item (runner_item_on_*) and then itself (runner_on_*), all with the
 * task's state, so counting or listing events would show a five-item loop
 * six times. A task is told apart by its task_uuid (two tasks may share a
 * name), else by its name.
 */

/** The recap's count kinds, in the order the dry run's tiles show them. */
export const COUNT_KEYS = ['ok', 'changed', 'skipped', 'failed', 'unreachable'] as const;

type CountKey = (typeof COUNT_KEYS)[number];

/** Which state an unfinished loop shows when its items differ. */
const RANK: Record<CountKey, number> = { skipped: 0, ok: 1, changed: 2, failed: 3, unreachable: 4 };

const isMap = (v: unknown): v is Record<string, unknown> => v !== null && typeof v === 'object' && !Array.isArray(v);

const isCount = (state: string): state is CountKey => (COUNT_KEYS as readonly string[]).includes(state);

const isItem = (e: JobEvent) => typeof e.payload.event === 'string' && e.payload.event.startsWith('runner_item_');

function taskKey(e: JobEvent): string {
  const data = e.payload.event_data;
  const uuid = isMap(data) && typeof data.task_uuid === 'string' ? data.task_uuid : '';
  return `${e.host}\u0000${uuid || e.task}`;
}

interface TaskResult {
  host: string;
  task: string;
  state: CountKey;
  items: JobEvent[];
  own: JobEvent[];
}

/** taskResults folds the result events by task and host, in the order
 * each task first reported. The task's own event decides its state; until
 * it arrives (a loop still running), the weightiest item state does. */
function taskResults(events: readonly JobEvent[]): TaskResult[] {
  const byKey = new Map<string, TaskResult>();
  for (const e of events) {
    if (!isCount(e.state)) continue;
    const key = taskKey(e);
    let r = byKey.get(key);
    if (!r) {
      r = { host: e.host, task: e.task, state: e.state, items: [], own: [] };
      byKey.set(key, r);
    }
    if (isItem(e)) {
      r.items.push(e);
      if (r.own.length === 0 && RANK[e.state] > RANK[r.state]) r.state = e.state;
    } else {
      r.own.push(e);
      r.state = e.state;
    }
  }
  return [...byKey.values()];
}

/** countTaskResults counts one result per task and host: the live stand-in
 * for the recap's counts until the run has one. */
export function countTaskResults(events: readonly JobEvent[]): RecapCounts {
  const out: RecapCounts = { ...ZERO_COUNTS };
  for (const r of taskResults(events)) out[r.state]++;
  return out;
}

export interface ChangedTask {
  key: string;
  host: string;
  task: string;
  diffs: EventDiff[];
}

/** changedTasks lists each task that would change, per host, with its
 * differences: the items' when they carry any, else the task's own (a
 * loop's from its res.results). */
export function changedTasks(events: readonly JobEvent[]): ChangedTask[] {
  return taskResults(events)
    .filter((r) => r.state === 'changed')
    .map((r) => {
      const fromItems = r.items.flatMap((e) => eventDiffs(e.payload));
      const diffs = fromItems.length > 0 ? fromItems : r.own.flatMap((e) => eventDiffs(e.payload, { loopResults: true }));
      return { key: `${r.host}-${(r.own[0] ?? r.items[0]).seq}`, host: r.host, task: r.task, diffs };
    });
}
