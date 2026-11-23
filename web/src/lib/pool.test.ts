import { describe, expect, it } from 'vitest';
import { mapPool } from './pool.ts';

describe('mapPool', () => {
  it('keeps the results in order and never exceeds the limit', async () => {
    let live = 0;
    let peak = 0;
    const out = await mapPool([1, 2, 3, 4, 5, 6, 7], 3, async (n) => {
      live++;
      peak = Math.max(peak, live);
      await new Promise((r) => setTimeout(r, 5 - (n % 3)));
      live--;
      return n * 2;
    });
    expect(out).toEqual([2, 4, 6, 8, 10, 12, 14]);
    expect(peak).toBe(3);
  });

  it('answers an empty list without calling', async () => {
    expect(await mapPool([], 4, () => Promise.reject(new Error('never')))).toEqual([]);
  });

  it('hands out no new item after a rejection', async () => {
    const started: number[] = [];
    await expect(
      mapPool([1, 2, 3, 4, 5, 6], 2, async (n) => {
        started.push(n);
        await new Promise((r) => setTimeout(r, 2));
        if (n === 1) throw new Error('boom');
        return n;
      }),
    ).rejects.toThrow('boom');
    await new Promise((r) => setTimeout(r, 30));
    expect(started.length).toBeLessThan(6);
  });

  it('stops handing out items once its signal aborts, and passes the signal on', async () => {
    const ctl = new AbortController();
    const seen: AbortSignal[] = [];
    const started: number[] = [];
    const run = mapPool(
      [1, 2, 3, 4, 5, 6],
      2,
      async (n, signal) => {
        started.push(n);
        seen.push(signal as AbortSignal);
        await new Promise((r) => setTimeout(r, 2));
        if (n === 2) ctl.abort();
        return n;
      },
      ctl.signal,
    );
    await expect(run).rejects.toThrow();
    await new Promise((r) => setTimeout(r, 30));
    expect(started.length).toBeLessThan(6);
    expect(seen.every((s) => s === ctl.signal)).toBe(true);
  });

  it('rejects when a call does', async () => {
    await expect(mapPool([1, 2], 2, (n) => (n === 2 ? Promise.reject(new Error('boom')) : Promise.resolve(n)))).rejects.toThrow('boom');
  });
});
