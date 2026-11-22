import { describe, expect, it } from 'vitest';
import { countsOfHost, readHostCounts, sumHostCounts, ZERO_COUNTS } from './hostCounts.ts';

const stats = {
  failed_hosts: ['h2'],
  host_counts: {
    h1: { ok: 4, changed: 2, skipped: 1, failed: 0, unreachable: 0 },
    h2: { ok: 1, changed: 0, skipped: 0, failed: 1, unreachable: 0 },
    localhost: { ok: 1, changed: 1, skipped: 0, failed: 0, unreachable: 1 },
  },
};

describe('host counts', () => {
  it('reads the counts per host', () => {
    expect(readHostCounts(stats)?.get('h1')).toEqual({ ok: 4, changed: 2, skipped: 1, failed: 0, unreachable: 0 });
    expect([...(readHostCounts(stats)?.keys() ?? [])]).toEqual(['h1', 'h2', 'localhost']);
  });

  it.each([
    ['absent stats', undefined],
    ['null stats', null],
    ['stats without host_counts', { ok_hosts: ['h1'] }],
    ['host_counts that is not a map', { host_counts: ['h1'] }],
  ])('answers null for %s (an older run)', (_name, raw) => {
    expect(readHostCounts(raw)).toBeNull();
    expect(sumHostCounts(raw)).toBeNull();
  });

  it('counts a host missing from the map as zero', () => {
    expect(countsOfHost(stats, 'h9')).toEqual(ZERO_COUNTS);
    expect(countsOfHost(undefined, 'h1')).toEqual(ZERO_COUNTS);
    expect(countsOfHost(stats, 'h2').failed).toBe(1);
  });

  it('reads a malformed count as zero and ignores a host that is not a map', () => {
    const odd = { host_counts: { h1: { ok: '3', changed: 2.5, skipped: -1, failed: null }, h2: 'x' } };
    expect(readHostCounts(odd)?.get('h1')).toEqual({ ok: 0, changed: 2, skipped: 0, failed: 0, unreachable: 0 });
    expect(readHostCounts(odd)?.has('h2')).toBe(false);
  });

  it('sums the counts over the hosts', () => {
    expect(sumHostCounts(stats)).toEqual({ ok: 6, changed: 3, skipped: 1, failed: 1, unreachable: 1 });
  });
});
