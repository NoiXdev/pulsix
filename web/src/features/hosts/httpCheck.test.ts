import { describe, expect, it } from 'vitest';
import { en } from '../../i18n/en/index.ts';
import { de } from '../../i18n/de/index.ts';
import { formatter } from '../../i18n/format.ts';
import type { HostCheck, HostCheckValue } from '../../api/types.ts';
import { assertionRows, failureBeside, timingParts } from './httpCheck.ts';
import { discreteText } from '../checks/syntheticLabels.ts';

const NOW = new Date('2026-11-16T12:00:00Z');
const AT = '2026-11-16T11:59:00Z';

const value = (metric: string, over: Partial<HostCheckValue> = {}): HostCheckValue => ({
  item: '',
  metric,
  at: AT,
  stale: false,
  state: null,
  flapping: false,
  state_stale: false,
  ignored: false,
  ...over,
});

const check = (values: HostCheckValue[], params: Record<string, unknown> = {}, plugin = 'http'): HostCheck => ({
  key: 'web',
  plugin: plugin as HostCheck['plugin'],
  params,
  interval_seconds: 60,
  enabled: true,
  origin: 'custom',
  source: 'host',
  values,
});

describe('failureBeside', () => {
  it('words the failure reason next to <plugin>.up only', () => {
    const c = check([value('tcp.up', { value: 0 }), value('tcp.failure', { label: 'refused' })], {}, 'tcp');
    expect(failureBeside(c, c.values![0], en)).toBe('connection refused');
    expect(failureBeside(c, c.values![0], de)).toBe('Verbindung abgelehnt');
    expect(failureBeside(c, c.values![1], en)).toBeNull();
  });

  it('says nothing while the check is up, or when the reason is stale or an error', () => {
    const up = check([value('icmp.up', { value: 1 }), value('icmp.failure', { label: 'none' })], {}, 'icmp');
    expect(failureBeside(up, up.values![0], en)).toBeNull();
    const stale = check([value('icmp.up', { value: 0 }), value('icmp.failure', { label: 'timeout', stale: true })], {}, 'icmp');
    expect(failureBeside(stale, stale.values![0], en)).toBeNull();
    const failed = check([value('icmp.up', { error: 'bad' }), value('icmp.failure', { error: 'bad' })], {}, 'icmp');
    expect(failureBeside(failed, failed.values![0], en)).toBeNull();
  });

  it('shows a label it does not know as it came', () => {
    const c = check([value('dns.up', { value: 0 }), value('dns.failure', { label: 'novel' })], {}, 'dns');
    expect(failureBeside(c, c.values![0], en)).toBe('novel');
  });
});

describe('discreteText', () => {
  it.each([
    ['http.failure', 'timeout', 'timed out'],
    ['http.assertion', 'pass', 'pass'],
    ['http.assertion_actual', 'absent', 'absent'],
    ['http.assertion_actual', '"text"', '"text"'],
    ['http.cert_issuer', 'R3', 'R3'],
  ])('words %s %s as %s', (metric, label, want) => {
    expect(discreteText(en, 'http', metric, label)).toBe(want);
  });

  it('speaks German', () => {
    expect(discreteText(de, 'http', 'http.assertion', 'crit')).toBe('kritisch');
    expect(discreteText(de, 'http', 'http.assertion_actual', '(hidden)')).toBe('(verborgen)');
  });
});

describe('timingParts', () => {
  it('lists the phases the latest attempt reported, in order, skipping absent ones', () => {
    const c = check([
      value('http.transfer_ms', { value: 3 }),
      value('http.connect_ms', { value: 10.456 }),
      value('http.ttfb_ms', { value: 120 }),
      value('http.response_ms', { value: 140 }),
      // An address URL resolves no name; an http URL has no TLS.
    ]);
    expect(timingParts(c, en)).toEqual([
      { key: 'connect', label: 'connect', value: 10.456, display: '10.46 ms', shade: 1 },
      { key: 'ttfb', label: 'first byte', value: 120, display: '120 ms', shade: 3 },
      { key: 'transfer', label: 'transfer', value: 3, display: '3 ms', shade: 4 },
    ]);
  });

  it('leaves out stale and failed phases', () => {
    const c = check([
      value('http.up', { value: 1 }),
      value('http.dns_ms', { value: 4, stale: true }),
      value('http.tls_ms', { error: 'x' }),
      value('http.ttfb_ms', { value: 1 }),
    ]);
    expect(timingParts(c, de).map((p) => p.label)).toEqual(['erstes Byte']);
  });

  it('keeps only the phases of the latest attempt, even before an older one turns stale', () => {
    const latest = '2026-11-16T11:59:30Z';
    const c = check([
      value('http.up', { value: 1, at: latest }),
      value('http.response_ms', { value: 30, at: latest }),
      // The URL named a host a minute ago; it is an address now.
      value('http.dns_ms', { value: 4, at: AT }),
      value('http.connect_ms', { value: 9, at: latest }),
      value('http.ttfb_ms', { value: 20, at: latest }),
    ]);
    expect(timingParts(c, en).map((p) => p.key)).toEqual(['connect', 'ttfb']);
  });

  it('times the attempt by http.up when response_ms failed, and has no parts without either', () => {
    const c = check([value('http.up', { value: 0 }), value('http.response_ms', { error: 'x' }), value('http.dns_ms', { value: 2 })]);
    expect(timingParts(c, en).map((p) => p.key)).toEqual(['dns']);
    expect(timingParts(check([value('http.dns_ms', { value: 2 })]), en)).toEqual([]);
  });
});

describe('assertionRows', () => {
  const params = { assertions: ['ok=status in 200..299', 'ct=header("Content-Type")   contains "json" @warn', 'gone=json("$.x") exists'] };

  it('reads each assertion of the parameter with its latest result', () => {
    const c = check(
      [
        value('http.assertion', { item: 'ok', label: 'pass' }),
        value('http.assertion_actual', { item: 'ok', label: '200' }),
        value('http.assertion', { item: 'ct', label: 'warn' }),
        value('http.assertion_actual', { item: 'ct', label: 'absent' }),
      ],
      params,
    );
    expect(assertionRows(c, en, formatter('en'), NOW)).toEqual([
      { key: 'ok', name: 'ok', expression: 'status in 200..299', severity: 'crit', status: { label: 'pass', tone: 'ok' }, actual: '200', age: '1 minute ago' },
      { key: 'ct', name: 'ct', expression: 'header("Content-Type") contains "json"', severity: 'warn', status: { label: 'warn', tone: 'warn' }, actual: 'absent', age: '1 minute ago' },
      { key: 'gone', name: 'gone', expression: 'json("$.x") exists', severity: 'crit', status: { label: 'no result', tone: 'unknown' }, actual: '—', age: '—' },
    ]);
  });

  it('says when an assertion could not be evaluated, and when its result is stale', () => {
    const c = check(
      [
        value('http.assertion', { item: 'ok', error: 'the body is not JSON' }),
        value('http.assertion_actual', { item: 'ok', error: 'the body is not JSON' }),
        value('http.assertion', { item: 'ct', label: 'crit', stale: true }),
      ],
      params,
    );
    const rows = assertionRows(c, de, formatter('de'), NOW);
    expect(rows[0]).toMatchObject({ status: { label: 'nicht ausgewertet', tone: 'unknown' }, actual: 'the body is not JSON', severity: 'kritisch' });
    expect(rows[1]).toMatchObject({ status: { label: 'kritisch', tone: 'crit' }, severity: 'Warnung' });
    expect(rows[1].age).toMatch(/\(veraltet\)$/);
  });

  it('has no rows without assertions', () => {
    expect(assertionRows(check([]), en, formatter('en'), NOW)).toEqual([]);
  });
});
