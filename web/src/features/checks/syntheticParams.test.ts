import { describe, expect, it } from 'vitest';
import { en } from '../../i18n/en/index.ts';
import {
  isSynthetic,
  keyAfterPluginChange,
  syntheticParams,
  syntheticValuesFrom,
  validateSynthetic,
  type SyntheticPlugin,
  type SyntheticValues,
} from './syntheticParams.ts';

describe('isSynthetic', () => {
  it.each([
    ['icmp', true], ['tcp', true], ['http', true], ['tls', true], ['dns', true],
    ['cpu', false], ['network', false], ['', false],
  ])('%s → %s', (plugin, want) => expect(isSynthetic(plugin)).toBe(want));
});

describe('syntheticParams', () => {
  it.each<[string, SyntheticPlugin, SyntheticValues, Record<string, unknown>]>([
    ['nothing set sends nothing', 'icmp', {}, {}],
    ['blank fields are left out', 'tcp', { port: '22', send: '', expect: '', timeout_s: '' }, { port: 22 }],
    ['numbers are sent as numbers', 'icmp', { count: '5', timeout_s: '10' }, { count: 5, timeout_s: 10 }],
    ['url is trimmed, a substring is sent verbatim', 'http', { url: ' https://{address}/ ', body_contains: ' ok ' }, { url: 'https://{address}/', body_contains: ' ok ' }],
    ['a switch the user set is sent, false included', 'http', { url: 'http://x', follow_redirects: false, verify_tls: true }, { url: 'http://x', follow_redirects: false, verify_tls: true }],
    ['a select the user set is sent', 'dns', { type: 'AAAA' }, { type: 'AAAA' }],
    ['fields of another plugin are ignored', 'tls', { port: '8443', url: 'http://x', count: '3' }, { port: 8443 }],
    ['blank-only names are left out', 'dns', { name: '   ', server: ' 192.0.2.53 ' }, { server: '192.0.2.53' }],
  ])('%s', (_, plugin, values, want) => expect(syntheticParams(plugin, values)).toEqual(want));
});

describe('syntheticValuesFrom', () => {
  it('reads every known parameter back as a form value', () => {
    expect(
      syntheticValuesFrom('http', { url: 'https://a/', method: 'HEAD', follow_redirects: false, verify_tls: true, body_contains: 'ok', timeout_s: 20 }),
    ).toEqual({ url: 'https://a/', method: 'HEAD', follow_redirects: false, verify_tls: true, body_contains: 'ok', timeout_s: '20' });
  });

  it('round-trips a stored definition unchanged', () => {
    const stored = { name: '{address}', type: 'MX', server: '[2001:db8::53]:53', expect: 'mx.example.org', timeout_s: 3 };
    expect(syntheticParams('dns', syntheticValuesFrom('dns', stored))).toEqual(stored);
  });

  it('ignores unknown keys, wrong types and a non-synthetic plugin', () => {
    expect(syntheticValuesFrom('tcp', { port: 22, banner: 'x', send: 5 })).toEqual({ port: '22' });
    expect(syntheticValuesFrom('cpu', { port: 22 })).toEqual({});
    expect(syntheticValuesFrom('tcp', undefined)).toEqual({});
  });
});

describe('validateSynthetic', () => {
  const t = en;
  const range = (lo: number, hi: number) => t.checks.synthetic.errors.wholeRange(lo, hi);
  it.each<[string, SyntheticPlugin, SyntheticValues, Record<string, string>]>([
    ['icmp with nothing set is fine', 'icmp', {}, {}],
    ['icmp count above 10', 'icmp', { count: '11' }, { count: range(1, 10) }],
    ['icmp count not whole', 'icmp', { count: '1.5' }, { count: range(1, 10) }],
    ['icmp timeout 0', 'icmp', { timeout_s: '0' }, { timeout_s: range(1, 30) }],
    ['tcp needs a port', 'tcp', {}, { port: t.validation.required }],
    ['tcp port out of range', 'tcp', { port: '65536' }, { port: range(1, 65535) }],
    ['tcp port negative', 'tcp', { port: '-1' }, { port: range(1, 65535) }],
    ['tcp with a port is fine', 'tcp', { port: '22', expect: 'SSH-' }, {}],
    ['tcp send too long', 'tcp', { port: '22', send: 'x'.repeat(501) }, { send: t.validation.maxChars(500) }],
    ['http needs a url', 'http', {}, { url: t.validation.required }],
    ['http allows {address}', 'http', { url: 'https://{address}:8443/health' }, {}],
    ['http refuses another scheme', 'http', { url: 'ftp://example.org/' }, { url: t.checks.synthetic.errors.url }],
    ['http refuses user info', 'http', { url: 'https://user:pass@example.org/' }, { url: t.checks.synthetic.errors.url }],
    ['http refuses a bare word', 'http', { url: 'example.org' }, { url: t.checks.synthetic.errors.url }],
    ['http timeout allows 60', 'http', { url: 'http://x', timeout_s: '60' }, {}],
    ['http timeout refuses 61', 'http', { url: 'http://x', timeout_s: '61' }, { timeout_s: range(1, 60) }],
    ['tls port out of range', 'tls', { port: '0' }, { port: range(1, 65535) }],
    ['tls timeout above 30', 'tls', { timeout_s: '31' }, { timeout_s: range(1, 30) }],
    ['dns server IPv4', 'dns', { server: '192.0.2.53' }, {}],
    ['dns server IPv4 with port', 'dns', { server: '192.0.2.53:5353' }, {}],
    ['dns server IPv6', 'dns', { server: '2001:db8::53' }, {}],
    ['dns server bracketed IPv6', 'dns', { server: '[2001:db8::53]' }, {}],
    ['dns server bracketed IPv6 with port', 'dns', { server: '[2001:db8::53]:53' }, {}],
    ['dns server a host name', 'dns', { server: 'dns.example.org' }, { server: t.checks.synthetic.errors.server }],
    ['dns server port 0', 'dns', { server: '192.0.2.53:0' }, { server: t.checks.synthetic.errors.server }],
    ['dns server port too big', 'dns', { server: '192.0.2.53:65536' }, { server: t.checks.synthetic.errors.server }],
    ['dns server octet too big', 'dns', { server: '192.0.2.256' }, { server: t.checks.synthetic.errors.server }],
    ['dns server bracketed IPv4', 'dns', { server: '[192.0.2.53]:53' }, { server: t.checks.synthetic.errors.server }],
    ['dns server junk in brackets', 'dns', { server: '[::1]/x[' }, { server: t.checks.synthetic.errors.server }],
  ])('%s', (_, plugin, values, want) => expect(validateSynthetic(plugin, values, t)).toEqual(want));
});

describe('keyAfterPluginChange', () => {
  it.each<[string, string, string, string, string]>([
    ['a blank key takes a synthetic plugin name', '', 'cpu', 'http', 'http'],
    ['a key that was the old synthetic name follows', 'http', 'http', 'tcp', 'tcp'],
    ['leaving the five clears the derived key', 'tcp', 'tcp', 'cpu', ''],
    ['a key of the user own stays', 'web-health', 'http', 'tcp', 'web-health'],
    ['a key of the user own stays leaving the five', 'web-health', 'http', 'cpu', 'web-health'],
    ['between agent plugins nothing changes', '', 'cpu', 'disk', ''],
    ['an agent plugin key stays between agent plugins', 'cpu', 'cpu', 'memory', 'cpu'],
  ])('%s', (_, key, from, to, want) => expect(keyAfterPluginChange(key, from, to)).toBe(want));
});
