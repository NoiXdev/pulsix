import { describe, expect, it } from 'vitest';
import { checkStatus, keyValue, orderChecks } from './checkSummary.ts';
import { en } from '../../i18n/en/index.ts';
import type { HostCheck, HostCheckValue } from '../../api/types.ts';

const val = (over: Partial<HostCheckValue>): HostCheckValue => ({
  item: '',
  metric: 'cpu.used_pct',
  at: '2026-09-27T12:00:00Z',
  stale: false,
  state: null,
  state_stale: false,
  flapping: false,
  ignored: false,
  vanished: false,
  ...over,
});

const chk = (plugin: string, values: HostCheckValue[], key = plugin): HostCheck =>
  ({ key, plugin, params: {}, interval_seconds: 60, enabled: true, origin: 'seed', source: 'org', values }) as HostCheck;

describe('checkStatus', () => {
  it.each([
    [['ok', 'crit', 'warn'], 'crit'],
    [['ok', 'warn'], 'warn'],
    [['ok', 'ok'], 'ok'],
    [['ok', 'unknown'], 'unknown'],
    [['unknown', null], 'unknown'],
    [[], 'unknown'],
  ] as const)('%j is %s', (states, want) => {
    const c = chk('disk', states.map((state, i) => val({ item: `/${i}`, state })));
    expect(checkStatus(c)).toBe(want);
  });

  it('counts a disabled check and a stale verdict as unknown, like the Overview', () => {
    expect(checkStatus({ ...chk('cpu', [val({ state: 'crit' })]), enabled: false })).toBe('unknown');
    expect(checkStatus(chk('cpu', [val({ state: 'crit', state_stale: true })]))).toBe('unknown');
    expect(checkStatus(chk('cpu', [val({ state: 'ok', stale: true })]))).toBe('unknown');
  });

  it('does not count an ignored item', () => {
    expect(checkStatus(chk('disk', [val({ state: 'crit', ignored: true }), val({ item: '/b', state: 'ok' })]))).toBe('ok');
  });
});

describe('orderChecks', () => {
  it('puts critical first, then warning, and keeps the given order otherwise', () => {
    const a = chk('cpu', [val({ state: 'ok' })], 'a');
    const b = chk('cpu', [val({ state: 'warn' })], 'b');
    const c = chk('cpu', [], 'c');
    const d = chk('cpu', [val({ state: 'crit' })], 'd');
    expect(orderChecks([a, b, c, d], false).map((x) => x.key)).toEqual(['d', 'b', 'a', 'c']);
  });

  it('keeps the order while the verdicts are frozen', () => {
    const a = chk('cpu', [val({ state: 'ok' })], 'a');
    const b = chk('cpu', [val({ state: 'crit' })], 'b');
    expect(orderChecks([a, b], true).map((x) => x.key)).toEqual(['a', 'b']);
  });
});

describe('keyValue', () => {
  const kv = (c: HostCheck) => keyValue(c, en);

  it('http: status code and response time', () => {
    const c = chk('http', [val({ metric: 'http.status_code', value: 200 }), val({ metric: 'http.response_ms', value: 123.456 })]);
    expect(kv(c)).toBe('200 · 123.46 ms');
  });
  it('http: the status code alone without a response time', () => {
    expect(kv(chk('http', [val({ metric: 'http.status_code', value: 503 })]))).toBe('503');
  });
  it('disk: the worst used percentage with its mount', () => {
    const c = chk('disk', [
      val({ item: '/', metric: 'disk.used_pct', value: 40 }),
      val({ item: '/var', metric: 'disk.used_pct', value: 81.25 }),
      val({ item: '/var', metric: 'disk.free_bytes', value: 5 }),
    ]);
    expect(kv(c)).toBe('81.25 % on /var');
  });
  it('tls: days left', () => {
    expect(kv(chk('tls', [val({ metric: 'tls.cert_expiry_days', value: 41 })]))).toBe('41 days left');
  });
  it('cpu and memory: the used percentage', () => {
    expect(kv(chk('cpu', [val({ value: 12.5 })]))).toBe('12.5 %');
    expect(kv(chk('memory', [val({ metric: 'mem.used_pct', value: 70 })]))).toBe('70 %');
  });
  it('icmp, tcp, dns: the response time', () => {
    expect(kv(chk('icmp', [val({ metric: 'icmp.rtt_ms', value: 4 })]))).toBe('4 ms');
    expect(kv(chk('tcp', [val({ metric: 'tcp.connect_ms', value: 9 })]))).toBe('9 ms');
    expect(kv(chk('dns', [val({ metric: 'dns.resolve_ms', value: 2 })]))).toBe('2 ms');
  });
  it('default: the first metric with a reading, a label as it is', () => {
    expect(kv(chk('service', [val({ item: 'nginx.service', metric: 'state', label: 'active' })]))).toBe('state active');
    expect(kv(chk('uptime', [val({ metric: 'uptime.seconds', value: 90 })]))).toBe('uptime.seconds 90');
  });
  it('is null without a reading, and ignores failed readings', () => {
    expect(kv(chk('cpu', []))).toBeNull();
    expect(kv(chk('cpu', [val({ error: 'boom' })]))).toBeNull();
  });
});
