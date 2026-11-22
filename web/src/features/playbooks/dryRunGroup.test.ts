import { describe, expect, it } from 'vitest';
import type { JobDetail, JobEvent } from '../../api/types.ts';
import { groupChanges, groupCounts, groupStatus, type RunFeed } from './dryRunGroup.ts';

const feed = (id: string, stats: Record<string, unknown>, events: Partial<JobEvent>[] = []): RunFeed => ({
  job: { id, stats } as unknown as JobDetail,
  status: 'succeeded',
  events: events.map((e, i) => ({ seq: i + 1, attempt: 1, at: '', host: '', task: '', state: 'ok', payload: { event: 'runner_on_ok' }, ...e }) as JobEvent),
});

describe('groupStatus', () => {
  it.each([
    [['succeeded'], 'succeeded'],
    [['succeeded', 'succeeded'], 'succeeded'],
    [['succeeded', 'failed'], 'failed'],
    [['cancelled', 'failed'], 'failed'],
    [['cancelled', 'succeeded'], 'cancelled'],
    [['failed', 'error'], 'error'],
    [['succeeded', 'running'], 'running'],
    [['failed', 'queued'], 'queued'],
    [['queued', 'queued'], 'queued'],
    [['queued', 'claimed'], 'running'],
    [['cancelling', 'succeeded'], 'cancelling'],
  ])('%j reads as %s', (statuses, want) => {
    expect(groupStatus(statuses)).toBe(want);
  });
});

describe('groupCounts', () => {
  it("adds each job's recap counts, or its event counts while it has none", () => {
    const withRecap = feed('a', { host_counts: { h1: { ok: 2, changed: 1, skipped: 0, failed: 0, unreachable: 0 } } });
    const live = feed('b', {}, [{ host: 'web-02', task: 'x', state: 'changed' }, { host: 'web-02', task: 'y', state: 'failed' }]);
    expect(groupCounts([withRecap, live])).toEqual({ ok: 2, changed: 2, skipped: 0, failed: 1, unreachable: 0 });
  });
});

describe('groupChanges', () => {
  it('lists the changes of every job with keys that name the job', () => {
    const changed = { state: 'changed', payload: { event: 'runner_on_ok', event_data: { res: { changed: true } } } };
    const a = feed('a', {}, [{ ...changed, host: 'web-01', task: 'conf' }]);
    const b = feed('b', {}, [{ ...changed, host: 'web-02', task: 'conf' }]);
    const out = groupChanges([a, b]);
    expect(out.map((c) => [c.host, c.task])).toEqual([
      ['web-01', 'conf'],
      ['web-02', 'conf'],
    ]);
    expect(new Set(out.map((c) => c.key)).size).toBe(2);
    expect(out[0].key.startsWith('a-')).toBe(true);
  });
});
