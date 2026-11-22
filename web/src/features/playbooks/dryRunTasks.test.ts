import { describe, expect, it } from 'vitest';
import type { JobEvent } from '../../api/types.ts';
import { changedTasks, countTaskResults } from './dryRunTasks.ts';

let seq = 0;
const ev = (event: string, host: string, task: string, state: string, res: Record<string, unknown> = {}, uuid = `u-${task}`): JobEvent => ({
  seq: ++seq, attempt: 1, at: '', host, task, state,
  payload: { event, event_data: { host, task, task_uuid: uuid, res } },
});

const diff = (before: string, after: string) => ({ before, after });

/** A loop over two files on web-01: two item events and the loop's own
 * event, whose diffs sit in res.results. */
const LOOP = () => [
  ev('runner_on_start', 'web-01', 'Render configs', 'task'),
  ev('runner_item_on_ok', 'web-01', 'Render configs', 'changed', { changed: true, diff: diff('a\n', 'A\n') }),
  ev('runner_item_on_ok', 'web-01', 'Render configs', 'changed', { changed: true, diff: diff('b\n', 'B\n') }),
  ev('runner_on_ok', 'web-01', 'Render configs', 'changed', { changed: true, results: [{ diff: diff('a\n', 'A\n') }, { diff: diff('b\n', 'B\n') }] }),
];

describe('dry run task results', () => {
  it('makes one change of a looped task, with the diffs of its items', () => {
    const tasks = changedTasks(LOOP());
    expect(tasks).toHaveLength(1);
    expect(tasks[0]).toMatchObject({ host: 'web-01', task: 'Render configs' });
    expect(tasks[0].diffs).toEqual([
      { kind: 'compare', before: 'a\n', after: 'A\n' },
      { kind: 'compare', before: 'b\n', after: 'B\n' },
    ]);
  });

  it("takes the loop's own results when no item event carries a diff", () => {
    const events = LOOP().filter((e) => e.payload.event !== 'runner_item_on_ok');
    expect(changedTasks(events)[0].diffs).toHaveLength(2);
  });

  it('keeps the same task on two hosts, and two tasks of one name apart by their uuid', () => {
    const events = [
      ev('runner_on_ok', 'web-01', 'Render', 'changed', { diff: diff('x\n', 'y\n') }, 'u1'),
      ev('runner_on_ok', 'web-02', 'Render', 'changed', {}, 'u1'),
      ev('runner_on_ok', 'web-01', 'Render', 'changed', {}, 'u2'),
    ];
    expect(changedTasks(events).map((t) => [t.host, t.task, t.diffs.length])).toEqual([
      ['web-01', 'Render', 1],
      ['web-02', 'Render', 0],
      ['web-01', 'Render', 0],
    ]);
  });

  it('counts one result per task and host, not one per loop item', () => {
    const events = [...LOOP(), ev('runner_on_ok', 'web-01', 'Install', 'ok'), ev('runner_on_skipped', 'web-02', 'Install', 'skipped')];
    expect(countTaskResults(events)).toEqual({ ok: 1, changed: 1, skipped: 1, failed: 0, unreachable: 0 });
  });

  it('counts a loop still running by its items: a failed item wins over a change', () => {
    const items = [
      ev('runner_item_on_ok', 'web-01', 'Loop', 'changed'),
      ev('runner_item_on_failed', 'web-01', 'Loop', 'failed'),
      ev('runner_item_on_ok', 'web-02', 'Loop', 'ok'),
    ];
    expect(countTaskResults(items)).toEqual({ ok: 1, changed: 0, skipped: 0, failed: 1, unreachable: 0 });
    expect(changedTasks(items)).toEqual([]);
  });
});
