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

  it('rejects when a call does', async () => {
    await expect(mapPool([1, 2], 2, (n) => (n === 2 ? Promise.reject(new Error('boom')) : Promise.resolve(n)))).rejects.toThrow('boom');
  });
});
