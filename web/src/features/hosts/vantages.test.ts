import { describe, expect, it } from 'vitest';
import { CHECK_PLUGINS } from '../../test/checkPlugins.ts';
import { en } from '../../i18n/en/index.ts';
import { de } from '../../i18n/de/index.ts';
import { formatter } from '../../i18n/format.ts';
import type { HostCheck } from '../../api/types.ts';
import { responseMetricOf, vantageOptions, vantageRows } from './vantages.ts';

type Vantage = NonNullable<HostCheck['vantages']>[number];

const NOW = new Date('2026-11-16T12:00:00Z');

const vantage = (over: Partial<Vantage> = {}): Vantage => ({
  vantage: 'manager',
  name: '',
  kind: 'manager',
  status: 'up',
  at: '2026-11-16T11:59:00Z',
  observations: [
    { item: '', metric: 'tcp.up', value: 1 },
    { item: '', metric: 'tcp.connect_ms', value: 12.345 },
  ],
  ...over,
});

const check = (vantages: Vantage[], plugin = 'tcp'): HostCheck => ({
  key: 'site',
  plugin: plugin as HostCheck['plugin'],
  params: {},
  interval_seconds: 60,
  enabled: true,
  origin: 'custom',
  source: 'host',
  values: [],
  fail_policy: 'majority',
  vantages,
});

describe('responseMetricOf', () => {
  it.each([
    ['icmp', 'icmp.rtt_ms'],
    ['tcp', 'tcp.connect_ms'],
    ['http', 'http.response_ms'],
    ['tls', 'tls.handshake_ms'],
    ['dns', 'dns.resolve_ms'],
    ['snmp', 'snmp.response_ms'],
    ['cpu', undefined],
    ['unknown', undefined],
  ])('%s → %s', (plugin, want) => {
    expect(responseMetricOf(plugin, CHECK_PLUGINS)).toBe(want);
  });
  it('is unknown without the descriptors', () => {
    expect(responseMetricOf('tcp', undefined)).toBeUndefined();
  });
});

describe('vantageRows', () => {
  const agent = '0190a000-0000-7000-8000-00000000000a';
  const rows = vantageRows(
    check([
      vantage(),
      vantage({ vantage: agent, name: 'edge-1', kind: 'agent', status: 'down', at: '2026-11-16T11:55:00Z', observations: [{ item: '', metric: 'tcp.up', value: 0 }, { item: '', metric: 'tcp.connect_ms', error: 'timeout' }] }),
      vantage({ vantage: '0190a000-0000-7000-8000-00000000000b', name: '', kind: 'agent', status: 'none', at: undefined, observations: [] }),
    ]),
    CHECK_PLUGINS,
    en,
    formatter('en'),
    NOW,
  );

  it('names the server and each agent, the agent by its id when its host has no name', () => {
    expect(rows.map((r) => r.name)).toEqual(['Pulsix server', 'edge-1', '0190a000-0000-7000-8000-00000000000b']);
    expect(rows.map((r) => r.key)).toEqual(['manager', agent, '0190a000-0000-7000-8000-00000000000b']);
  });

  it('labels each state with a tone', () => {
    expect(rows.map((r) => [r.status.label, r.status.tone])).toEqual([
      ['up', 'ok'],
      ['down', 'crit'],
      ['no result', 'unknown'],
    ]);
  });

  it('shows the response time of the latest round, and nothing it does not have', () => {
    expect(rows.map((r) => r.response)).toEqual(['12.35 ms', '—', '—']);
  });

  it('says how old the latest round is', () => {
    expect(rows.map((r) => r.age)).toEqual(['1 minute ago', '5 minutes ago', '—']);
  });

  it('reads German too', () => {
    const deRows = vantageRows(check([vantage(), vantage({ kind: 'agent', vantage: agent, status: 'none', name: 'edge-1' })]), CHECK_PLUGINS, de, formatter('de'), NOW);
    expect(deRows.map((r) => [r.name, r.status.label, r.age])).toEqual([
      ['Pulsix-Server', 'erreichbar', 'vor 1 Minute'],
      ['edge-1', 'kein Ergebnis', 'vor 1 Minute'],
    ]);
  });

  it('is empty for a check without vantages', () => {
    expect(vantageRows({ ...check([]), vantages: undefined }, CHECK_PLUGINS, en, formatter('en'), NOW)).toEqual([]);
  });
});

describe('vantageOptions', () => {
  it('offers the combined series first, then every vantage by name', () => {
    expect(vantageOptions(check([vantage(), vantage({ vantage: 'a1', name: 'edge-1', kind: 'agent' })]), en)).toEqual([
      { value: '', label: 'Combined' },
      { value: 'manager', label: 'Pulsix server' },
      { value: 'a1', label: 'edge-1' },
    ]);
  });
  it('offers nothing for a check without vantages', () => {
    expect(vantageOptions({ ...check([]), vantages: undefined }, en)).toBeUndefined();
  });
});
