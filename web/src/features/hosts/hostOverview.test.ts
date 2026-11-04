import { describe, expect, it } from 'vitest';
import type { HostCheck } from '../../api/types.ts';
import { check, reading } from '../../test/hostDetail.tsx';
import { stateTiles, summarizeAlerts, systemLabel } from './hostOverview.ts';

const checks = (...list: unknown[]) => list as HostCheck[];

describe('summarizeAlerts', () => {
  it.each([
    { name: 'none open', severities: [], want: { open: 0, crit: 0, tone: 'ok' } },
    { name: 'warnings only', severities: ['warn', 'warn'], want: { open: 2, crit: 0, tone: 'warn' } },
    { name: 'an unknown counts as a warning', severities: ['unknown'], want: { open: 1, crit: 0, tone: 'warn' } },
    { name: 'one crit wins', severities: ['warn', 'crit', 'crit'], want: { open: 3, crit: 2, tone: 'crit' } },
  ] as const)('$name', ({ severities, want }) => {
    expect(summarizeAlerts(severities.map((severity) => ({ severity })))).toEqual(want);
  });
});

describe('systemLabel', () => {
  it.each([
    { name: 'os_version wins', facts: { os: 'linux', os_version: 'debian 13' }, want: 'debian 13' },
    { name: 'distribution plus version', facts: { distribution: 'Ubuntu', version: '24.04' }, want: 'Ubuntu 24.04' },
    { name: 'os plus version', facts: { os: 'linux', version: '6.12' }, want: 'linux 6.12' },
    { name: 'os alone', facts: { os: 'freebsd' }, want: 'freebsd' },
    { name: 'nothing usable', facts: { arch: 'amd64', os: '  ' }, want: null },
    { name: 'no facts', facts: undefined, want: null },
  ])('$name', ({ facts, want }) => {
    expect(systemLabel(facts)).toBe(want);
  });
});

describe('stateTiles', () => {
  it('finds CPU, RAM and load by plugin and metric, in that order', () => {
    const tiles = stateTiles(
      checks(
        check('mem', 'memory', [reading('mem.used_pct', 61)]),
        check('processor', 'cpu', [reading('cpu.load1', 0.84), reading('cpu.used_pct', 23, { state: 'warn' })]),
      ),
    );
    expect(tiles.map((tile) => [tile.kind, tile.check, tile.metric, tile.value, tile.tone])).toEqual([
      ['cpu', 'processor', 'cpu.used_pct', 23, 'warn'],
      ['ram', 'mem', 'mem.used_pct', 61, 'ok'],
      ['load', 'processor', 'cpu.load1', 0.84, 'ok'],
    ]);
  });

  it.each([
    {
      name: 'picks the disk item with the highest used_pct',
      values: [
        reading('disk.used_pct', 41, { item: '/' }),
        reading('disk.free_bytes', 99, { item: '/srv' }),
        reading('disk.used_pct', 87, { item: '/var', state: 'warn' }),
      ],
      want: { item: '/var', value: 87, tone: 'warn' },
    },
    {
      name: 'prefers a current reading over a fuller stale one',
      values: [reading('disk.used_pct', 95, { item: '/old', stale: true }), reading('disk.used_pct', 41, { item: '/' })],
      want: { item: '/', value: 41, tone: 'ok' },
    },
    {
      name: 'falls back to the fullest stale reading when none is current',
      values: [reading('disk.used_pct', 60, { item: '/', stale: true }), reading('disk.used_pct', 95, { item: '/var', stale: true })],
      want: { item: '/var', value: 95, tone: 'unknown' },
    },
  ])('$name', ({ values, want }) => {
    const tiles = stateTiles(checks(check('disk', 'disk', values)));
    expect(tiles).toEqual([{ kind: 'disk', check: 'disk', metric: 'disk.used_pct', ...want }]);
  });

  it('leaves out load when no check reports it', () => {
    const tiles = stateTiles(checks(check('cpu', 'cpu', [reading('cpu.used_pct', 5)])));
    expect(tiles.map((tile) => tile.kind)).toEqual(['cpu']);
  });

  it('skips disabled checks, errors and readings without a value', () => {
    const tiles = stateTiles(
      checks(
        check('cpu', 'cpu', [reading('cpu.used_pct', 5)], { enabled: false }),
        check('memory', 'memory', [reading('mem.used_pct', undefined, { error: 'boom' })]),
      ),
    );
    expect(tiles).toEqual([]);
  });

  it('reads a stale or unjudged value as unknown', () => {
    const tiles = stateTiles(
      checks(
        check('cpu', 'cpu', [reading('cpu.used_pct', 5, { state: 'crit', state_stale: true }), reading('cpu.load1', 1, { state: null })]),
        check('memory', 'memory', [reading('mem.used_pct', 50, { stale: true })]),
      ),
    );
    expect(tiles.map((tile) => tile.tone)).toEqual(['unknown', 'unknown', 'unknown']);
  });
});
