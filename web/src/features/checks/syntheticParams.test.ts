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
    ['tcp send decodes \\r, \\n and \\t', 'tcp', { port: '6379', send: 'PING\\r\\n\\t' }, { port: 6379, send: 'PING\r\n\t' }],
    ['tcp send decodes \\\\ to one backslash', 'tcp', { port: '1', send: 'a\\\\nb' }, { port: 1, send: 'a\\nb' }],
    ['tcp send leaves an unknown escape alone', 'tcp', { port: '1', send: 'a\\xb\\' }, { port: 1, send: 'a\\xb\\' }],
    ['tcp expect is sent verbatim', 'tcp', { port: '1', expect: 'a\\nb' }, { port: 1, expect: 'a\\nb' }],
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

  it('shows tcp send with its control characters escaped', () => {
    expect(syntheticValuesFrom('tcp', { port: 6379, send: 'PING\r\n\tx\\y' })).toEqual({ port: '6379', send: 'PING\\r\\n\\tx\\\\y' });
  });

  it.each([
    'PING\r\n',
    'stats\r\n',
    'GET / HTTP/1.0\r\nHost: x\r\n\r\n',
    'a\\nb',
    'tab\there',
    'trailing\\',
    '\\\\r',
  ])('round-trips tcp send %j through the form', (send) => {
    expect(syntheticParams('tcp', syntheticValuesFrom('tcp', { port: 1, send }))).toEqual({ port: 1, send });
  });

  it('ignores unknown keys, wrong types and a non-synthetic plugin', () => {
    expect(syntheticValuesFrom('tcp', { port: 22, banner: 'x', send: 5 })).toEqual({ port: '22' });
    expect(syntheticValuesFrom('cpu', { port: 22 })).toEqual({});
    expect(syntheticValuesFrom('tcp', undefined)).toEqual({});
  });
});

/** dnsName is a valid DNS name of exactly n bytes: 49-byte labels. */
function dnsName(n: number): string {
  let s = '';
  while (s.length < n) {
    if (s.length > 0) s += '.';
    s += 'a'.repeat(Math.min(49, n - s.length));
  }
  return s;
}

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
    ['http refuses a single slash', 'http', { url: 'http:/x' }, { url: t.checks.synthetic.errors.url }],
    ['http refuses no slashes', 'http', { url: 'http:example.com' }, { url: t.checks.synthetic.errors.url }],
    ['http accepts an upper-case scheme', 'http', { url: 'HTTPS://example.org/' }, {}],
    ['http timeout allows 60', 'http', { url: 'http://x', timeout_s: '60' }, {}],
    ['http timeout refuses 61', 'http', { url: 'http://x', timeout_s: '61' }, { timeout_s: range(1, 60) }],
    ['tls port out of range', 'tls', { port: '0' }, { port: range(1, 65535) }],
    ['tls timeout above 30', 'tls', { timeout_s: '31' }, { timeout_s: range(1, 30) }],
    ['dns name', 'dns', { name: 'example.com' }, {}],
    ['dns name with a trailing dot', 'dns', { name: 'example.com.' }, {}],
    ['dns name service style', 'dns', { name: '_sip._tcp.example.com' }, {}],
    ['dns name with {address}', 'dns', { name: 'www.{address}' }, {}],
    ['dns name of 253 bytes', 'dns', { name: dnsName(253) }, {}],
    ['dns name with a space', 'dns', { name: 'exa mple.com' }, { name: t.checks.synthetic.errors.name }],
    ['dns name with an empty label', 'dns', { name: 'example..com' }, { name: t.checks.synthetic.errors.name }],
    ['dns name with a leading dot', 'dns', { name: '.example.com' }, { name: t.checks.synthetic.errors.name }],
    ['dns name root only', 'dns', { name: '.' }, { name: t.checks.synthetic.errors.name }],
    ['dns name of 254 bytes', 'dns', { name: dnsName(254) }, { name: t.checks.synthetic.errors.name }],
    ['dns name with a 64-byte label', 'dns', { name: `${'a'.repeat(64)}.example` }, { name: t.checks.synthetic.errors.name }],
    ['tcp send counts its decoded bytes', 'tcp', { port: '1', send: '\\n'.repeat(500) }, {}],
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
