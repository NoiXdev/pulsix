import { describe, expect, it } from 'vitest';
import { formatNumber, isNumeric, metricLabel, sourceLabel } from './hostChecks.ts';
import type { HostCheck, HostCheckValue } from '../../api/types.ts';

const value = (over: Partial<HostCheckValue> = {}): HostCheckValue => ({
  item: '',
  metric: 'usage_percent',
  at: '2026-09-27T12:00:00Z',
  stale: false,
  ...over,
});

const check = (over: Partial<HostCheck> = {}): HostCheck => ({
  definition_id: 'd1',
  key: 'cpu',
  plugin: 'cpu',
  params: {},
  interval_seconds: 60,
  enabled: true,
  origin: 'seed',
  source: 'org',
  values: [],
  ...over,
});

describe('metricLabel', () => {
  // The exact string matters twice over: it is the graph button's
  // accessible name, and Testing Library normalises whitespace in both
  // text and name matching — so a stray leading space from an always-on
  // `${item} ${metric}` would be invisible to every rendered assertion.
  it.each([
    ['a whole-host metric is the metric alone', value({ item: '', metric: 'usage_percent' }), 'usage_percent'],
    ['a per-item metric names its item first', value({ item: '/var', metric: 'used_percent' }), '/var used_percent'],
    ['a unit is an item like any other', value({ item: 'nginx.service', metric: 'state' }), 'nginx.service state'],
  ])('%s', (_name, v, expected) => {
    expect(metricLabel(v)).toBe(expected);
  });
});

describe('formatNumber', () => {
  it.each([
    ['an integer keeps no decimal point', 42, '42'],
    ['one decimal stays one', 12.5, '12.5'],
    ['two decimals stay two', 81.25, '81.25'],
    ['a raw load average is cut to two, not rendered at sixteen digits', 1 / 3, '0.33'],
    ['rounding is half-up, not truncation', 0.125, '0.13'],
    ['a trailing zero is dropped rather than padded', 7.0999, '7.1'],
    ['zero is zero', 0, '0'],
    ['a large reading gets no locale grouping', 1234567.891, '1234567.89'],
    ['a negative reading rounds the same way', -0.456, '-0.46'],
  ])('%s', (_name, input, expected) => {
    expect(formatNumber(input)).toBe(expected);
  });
});

describe('isNumeric', () => {
  it.each([
    ['a numeric reading has a series', value({ value: 12.5 }), true],
    ['a discrete reading does not — the metrics route 400s on it', value({ label: 'active' }), false],
    ['a discrete reading is discrete even with a number beside it', value({ label: 'active', value: 1 }), false],
    ['a numeric reading whose collection failed still has history', value({ value: 91.2, error: 'boom' }), true],
    ['no reading at all is not graphable', value({}), false],
  ])('%s', (_name, v, expected) => {
    expect(isNumeric(v)).toBe(expected);
  });
});

describe('sourceLabel', () => {
  it.each([
    ['organization-bound', check({ source: 'org' }), 'Organization'],
    ['group-bound reads as the group’s name', check({ source: 'group', source_id: 'g1', source_name: 'Web tier' }), 'Web tier'],
    // source_name is optional on the DTO; an id is not a name, so it is
    // never shown raw.
    ['group-bound with no name still never shows the id', check({ source: 'group', source_id: 'g1' }), 'Monitoring group'],
    ['host-bound', check({ source: 'host', source_id: 'h1', source_name: 'web-1' }), 'This host'],
  ])('%s', (_name, c, expected) => {
    expect(sourceLabel(c)).toBe(expected);
  });
});
