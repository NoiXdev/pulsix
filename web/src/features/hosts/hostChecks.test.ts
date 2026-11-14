import { describe, expect, it } from 'vitest';
import * as checks from './hostChecks.ts';
import { CHECK_PLUGINS } from '../../test/checkPlugins.ts';
import { formatNumber, isGraphable, metricKinds, metricLabel, offersForget } from './hostChecks.ts';
import { en } from '../../i18n/en/index.ts';
import { de } from '../../i18n/de/index.ts';
import type { HostCheck, HostCheckValue, ItemDecision } from '../../api/types.ts';

// The label helpers take the catalogue; these tests read English.
const clockSkewLabel = (offset: number | undefined) => checks.clockSkewLabel(offset, en);
const stateLabel = (state: HostCheckValue['state']) => checks.stateLabel(state, en);
const verdictLabel = (state: HostCheckValue['state'], ignored: boolean) => checks.verdictLabel(state, ignored, en);
const decisionOriginLabel = (d: ItemDecision) => checks.decisionOriginLabel(d, en);
const sourceLabel = (c: HostCheck) => checks.sourceLabel(c, en);

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
  // ignored (v0.3b-3) defaults to false — none of the tests in this file
  // exercise item decisions either.
  ignored: false,
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
    [
      'a failed docker.state reading is discrete: the metrics route 400s on a label metric',
      check({ plugin: 'docker' }),
      value({ item: 'web', metric: 'docker.state', error: 'inspect web: not found' }),
      false,
    ],
    [
      'a failed docker.health reading is discrete too',
      check({ plugin: 'docker' }),
      value({ item: 'web', metric: 'docker.health', error: 'inspect web: not found' }),
      false,
    ],
    [
      'a failed docker.restart_count keeps its graph: it is a number',
      check({ plugin: 'docker' }),
      value({ item: 'web', metric: 'docker.restart_count', error: 'inspect web: not found' }),
      true,
    ],
    [
      'a failed raid.state reading is discrete',
      check({ plugin: 'raid' }),
      value({ item: 'md0', metric: 'raid.state', error: 'read /proc/mdstat: denied' }),
      false,
    ],
    [
      'a raid.sync_pct reading keeps its graph',
      check({ plugin: 'raid' }),
      value({ item: 'md0', metric: 'raid.sync_pct', error: 'read /proc/mdstat: denied' }),
      true,
    ],
    [
      'temperature and smart readings are graphable',
      check({ plugin: 'smart' }),
      value({ item: '/dev/sda', metric: 'smart.temperature_celsius', error: 'smartctl failed' }),
      true,
    ],
  ])('%s', (_name, c, v, expected) => {
    expect(isGraphable(c, v, metricKinds(CHECK_PLUGINS))).toBe(expected);
  });

  it('reads the discrete metrics and the label-only plugins off the descriptors', () => {
    const kinds = metricKinds(CHECK_PLUGINS);
    expect([...kinds.discreteMetrics].sort()).toEqual(['docker.health', 'docker.state', 'mysql.replication_state', 'raid.state', 'redis.replication_link', 'redis.role', 'script.output', 'script.status', 'service.state', 'snmp.if.status', 'snmp.text']);
    expect([...kinds.labelOnlyPlugins]).toEqual(['service']);
  });

  it('follows a plugin the descriptors add: all-discrete has no graph, a discrete metric of a mixed one neither', () => {
    const kinds = metricKinds([
      { name: 'cpu', runs: 'agent', params: [], metrics: [{ name: 'x.state', kind: 'discrete' }] },
      { name: 'memory', runs: 'agent', params: [], metrics: [{ name: 'y.state', kind: 'discrete' }, { name: 'y.n', kind: 'numeric' }] },
    ]);
    expect(isGraphable(check({ plugin: 'cpu' }), value({ metric: 'x.other', error: 'e' }), kinds)).toBe(false);
    expect(isGraphable(check({ plugin: 'memory' }), value({ metric: 'y.state', error: 'e' }), kinds)).toBe(false);
    expect(isGraphable(check({ plugin: 'memory' }), value({ metric: 'y.n', error: 'e' }), kinds)).toBe(true);
  });

  it('judges by the reading alone without descriptors', () => {
    const none = metricKinds(undefined);
    expect(isGraphable(check({ plugin: 'service' }), value({ value: 1 }), none)).toBe(true);
    expect(isGraphable(check({ plugin: 'service' }), value({ label: 'active' }), none)).toBe(false);
  });
});

describe('offersForget', () => {
  // Backlog 3.12b: Forget acts on the whole item, so it is offered only
  // when EVERY value of (check, item) is stale — one dropped or renamed
  // metric on a live item must not offer it — and once per item, on the
  // item's first row in render order. A whole-host metric is not an item;
  // a viewer has no checks.write. Items the check stopped asking for never
  // reach the card at all (the server's stillAsked).
  const old1 = value({ item: '/old', metric: 'used_percent', stale: true });
  const old2 = value({ item: '/old', metric: 'free_bytes', stale: true, error: 'gone' });
  const mixedStale = value({ item: '/mix', metric: 'used_percent', stale: true });
  const mixedFresh = value({ item: '/mix', metric: 'free_bytes', stale: false });
  const fresh = value({ item: '/var', stale: false });
  const wholeHost = value({ item: '', stale: true });
  const disk = check({ key: 'disk', plugin: 'disk', values: [old1, mixedStale, old2, mixedFresh, fresh, wholeHost] });
  it.each([
    ["the first row of an item whose every value is stale", old1, true, true],
    ['a later row of the same item (one button per item)', old2, true, false],
    ['an item with one stale and one fresh metric', mixedStale, true, false],
    ['the fresh metric of that item', mixedFresh, true, false],
    ['a fresh item', fresh, true, false],
    ['a stale whole-host metric', wholeHost, true, false],
    ['a stale item without checks.write', old1, false, false],
  ])('%s', (_name, v, canWrite, want) => {
    expect(offersForget(disk, v, canWrite)).toBe(want);
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

  it.each([
    ['a slow clock', 243.7, '4 Min. nach', 'Die Uhr dieses Hosts geht gegenüber der des Managers etwa 4 Min. nach.'],
    ['a fast clock', -45, '45 s vor', 'Die Uhr dieses Hosts geht gegenüber der des Managers etwa 45 s vor.'],
  ])('reads as German for %s', (_name, offset, label, sentence) => {
    expect(checks.clockSkewLabel(offset, de)).toBe(label);
    expect(de.hosts.checks.clockSkew(label).slice(0, sentence.length)).toBe(sentence);
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

describe('verdictLabel', () => {
  // The decisive case (review focus): an item that already had a state
  // row before it was ignored reads as "ignored", never "not yet
  // evaluated" — internal/items' own Apply deletes that row the moment
  // the decision takes effect, so the next read reports state: null, and
  // conflating that with "nobody has judged this yet" is the lie this
  // milestone's predecessor went out of its way to avoid for the other
  // four states.
  it('says "ignored" for a tuple whose state row was just deleted, not "not yet evaluated"', () => {
    expect(verdictLabel(null, true)).toEqual({ label: 'ignored', tone: 'unknown' });
  });

  it('says "ignored" even when a stale state value is still attached, since the decision is what governs the row now', () => {
    expect(verdictLabel('crit', true)).toEqual({ label: 'ignored', tone: 'unknown' });
  });

  it('falls through to the ordinary rule-engine verdict when nothing ignores this tuple', () => {
    expect(verdictLabel('warn', false)).toEqual({ label: 'warn', tone: 'warn' });
    expect(verdictLabel(null, false)).toEqual({ label: 'not yet evaluated', tone: 'unknown' });
  });
});

describe('decisionOriginLabel', () => {
  const decision = (over: Partial<ItemDecision> = {}): ItemDecision => ({
    id: 'd1',
    scope: 'org',
    check_key: 'disk',
    match_kind: 'glob',
    pattern: '/mnt/scratch-*',
    decision: 'ignore',
    created_at: '',
    updated_at: '',
    ...over,
  });

  it('names the organization pattern verbatim', () => {
    expect(decisionOriginLabel(decision({ scope: 'org', pattern: '/mnt/scratch-*' }))).toBe(
      'By the organization pattern `/mnt/scratch-*`',
    );
  });

  it('says only "set on this host" for a host-scoped decision — the operator is already standing on the one host it could name', () => {
    expect(decisionOriginLabel(decision({ scope: 'host', host_id: 'h1', match_kind: 'exact', pattern: '/var' }))).toBe(
      'Set on this host',
    );
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
    // Backlog 3.5a: a check from the host's own agent.yaml has no
    // definition behind it at all — not "this host"'s binding of one.
    ['defined on the host', check({ definition_id: undefined, origin: 'local', source: 'local' }), 'Defined on the host'],
  ])('%s', (_name, c, expected) => {
    expect(sourceLabel(c)).toBe(expected);
  });

  it('names a check defined on the host in German', () => {
    expect(checks.sourceLabel(check({ definition_id: undefined, origin: 'local', source: 'local' }), de)).toBe('Auf dem Host definiert');
  });
});
