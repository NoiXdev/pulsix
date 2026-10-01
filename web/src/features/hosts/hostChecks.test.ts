import { describe, expect, it } from 'vitest';
import { clockSkewLabel, formatNumber, isGraphable, metricLabel, sourceLabel, stateLabel } from './hostChecks.ts';
import type { HostCheck, HostCheckValue } from '../../api/types.ts';

const value = (over: Partial<HostCheckValue> = {}): HostCheckValue => ({
  item: '',
  metric: 'usage_percent',
  at: '2026-09-27T12:00:00Z',
  stale: false,
  // state/flapping (v0.3b-2 Task 11) and state_stale (backlog 3.36) default
  // to "never evaluated" — none of the tests in this file exercise the
  // rule engine's verdict, only the v0.3b-1 fields above it.
  state: null,
  flapping: false,
  state_stale: false,
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

describe('isGraphable', () => {
  // The shapes below are the ones check_current's own CHECK allows
  // (migration 00044): an error row carries NEITHER value nor label, so
  // "a number beside an error" is not a state to assert about. That is
  // exactly why the decision cannot be read off the latest value alone —
  // a failing numeric check must keep the graph its history supports.
  it.each([
    ['a numeric reading has a series', check(), value({ value: 12.5 }), true],
    ['a discrete reading does not — the metrics route 400s on it', check(), value({ label: 'active' }), false],
    ['a discrete reading is discrete even with a number beside it', check(), value({ label: 'active', value: 1 }), false],
    [
      'a numeric check keeps its graph while collection is failing — the series is exactly what the incident is about',
      check({ plugin: 'disk' }),
      value({ item: '/var', metric: 'used_percent', error: 'stat /var: permission denied' }),
      true,
    ],
    [
      'a discrete check offers none while it is failing either: nothing behind it is a number',
      check({ plugin: 'service' }),
      value({ item: 'nginx.service', metric: 'state', error: 'dbus: connection refused' }),
      false,
    ],
  ])('%s', (_name, c, v, expected) => {
    expect(isGraphable(c, v)).toBe(expected);
  });
});

describe('clockSkewLabel', () => {
  it.each([
    ['no measurement is not a claim of zero', undefined, null],
    ['a punctual host says nothing', 0.4, null],
    ['delivery timing is not a clock', 29, null],
    ['the floor itself is named', 30, '30 s behind'],
    ['seconds stay seconds up to a minute and a half', 89, '89 s behind'],
    // 90 is the boundary itself, and it was the one value not covered:
    // with only 89 tested, `>= 90` and `> 90` are indistinguishable and
    // the rule could quietly start reading "90 s" where it means "2 min".
    ['the boundary itself is already minutes', 90, '2 min behind'],
    ['and reads as minutes just above it too', 91, '2 min behind'],
    ['a four-minute lag is what makes every reading look stale', 243.7, '4 min behind'],
    ['a clock running fast is named as such, never as a negative number', -240, '4 min ahead of'],
    ['an hour ahead is still inside the plausibility window and still wrong', -3600, '60 min ahead of'],
  ])('%s', (_name, offset, expected) => {
    expect(clockSkewLabel(offset)).toBe(expected);
  });
});

describe('stateLabel', () => {
  // Every one of the five markers below must carry a distinct text label,
  // not merely a distinct tone: a colour-blind operator, or the same card
  // in a black-and-white screenshot, reads the word, not the dot beside it
  // (v0.3b-2 spec, "the four states must remain distinguishable without
  // colour alone").
  it.each([
    ['a tuple with no state row was never evaluated — this is not "ok"', null, { label: 'not yet evaluated', tone: 'unknown' }],
    ['ok', 'ok', { label: 'ok', tone: 'ok' }],
    ['warn', 'warn', { label: 'warn', tone: 'warn' }],
    ['crit', 'crit', { label: 'crit', tone: 'crit' }],
    // unknown is its own state, textually distinct from "not yet
    // evaluated" even though both currently share the unknown tone.
    ['unknown', 'unknown', { label: 'unknown', tone: 'unknown' }],
  ] as const)('%s', (_name, state, expected) => {
    expect(stateLabel(state)).toEqual(expected);
  });

  it('gives null and "unknown" different words, not just the same colour twice', () => {
    expect(stateLabel(null).label).not.toBe(stateLabel('unknown').label);
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
