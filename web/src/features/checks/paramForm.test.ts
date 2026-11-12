import { describe, expect, it } from 'vitest';
import { en } from '../../i18n/en/index.ts';
import { CHECK_PLUGINS, checkPlugin } from '../../test/checkPlugins.ts';
import {
  keyAfterPluginChange,
  paramsFrom,
  paramText,
  paramValuesFrom,
  placeholderOf,
  runsOnServer,
  validateParams,
  type ParamValues,
} from './paramForm.ts';

// The cases below were syntheticParams.test.ts and hardwareParams.test.ts
// before the form was built from the descriptors (backlog 3.54): the same
// inputs, the same params sent, the same refusals.

describe('runsOnServer', () => {
  it.each([
    ['icmp', true], ['tcp', true], ['http', true], ['tls', true], ['dns', true],
    ['cpu', false], ['network', false], ['docker', false], ['smart', false], ['', false],
  ])('%s → %s', (plugin, want) => expect(runsOnServer(CHECK_PLUGINS, plugin)).toBe(want));
});

describe('paramsFrom', () => {
  it.each<[string, string, ParamValues, Record<string, unknown>]>([
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
    ['docker sends nothing untouched', 'docker', {}, {}],
    ['docker label and socket are trimmed', 'docker', { selection: 'labeled', label: ' k=v ', socket: ' /x.sock ' }, { selection: 'labeled', label: 'k=v', socket: '/x.sock' }],
    ['an empty temperature list is {}', 'temperature', { sensors: '' }, {}],
    ['a blank-lines-only list is {}', 'temperature', { sensors: ' \n \n' }, {}],
    ['list entries are trimmed lines', 'temperature', { sensors: 'coretemp_core_0\n nvme_composite \n' }, { sensors: ['coretemp_core_0', 'nvme_composite'] }],
    ['smart devices', 'smart', { devices: '/dev/sda\n/dev/nvme0' }, { devices: ['/dev/sda', '/dev/nvme0'] }],
    ['raid arrays', 'raid', { arrays: 'md0' }, { arrays: ['md0'] }],
    ['an empty disk list is {}', 'disk', { mounts: '' }, {}],
    ['disk mounts', 'disk', { mounts: '/\n\n/var\n' }, { mounts: ['/', '/var'] }],
    ['an empty network list is {}', 'network', {}, {}],
    ['service units are always sent', 'service', { units: 'sshd' }, { units: ['sshd'] }],
    ['an untouched required list is sent empty, for the server to refuse', 'service', {}, { units: [] }],
    ['cpu takes none', 'cpu', { x: '1' }, {}],
  ])('%s', (_, plugin, values, want) => expect(paramsFrom(checkPlugin(plugin), values)).toEqual(want));
});

describe('paramValuesFrom', () => {
  it('reads every known parameter back as a form value', () => {
    expect(
      paramValuesFrom(checkPlugin('http'), { url: 'https://a/', method: 'HEAD', follow_redirects: false, verify_tls: true, body_contains: 'ok', timeout_s: 20 }),
    ).toEqual({ url: 'https://a/', method: 'HEAD', follow_redirects: false, verify_tls: true, body_contains: 'ok', timeout_s: '20' });
  });

  it('reads a list back as one entry per line', () => {
    expect(paramValuesFrom(checkPlugin('network'), { interfaces: ['eth0', 'bond0'] })).toEqual({ interfaces: 'eth0\nbond0' });
    expect(paramValuesFrom(checkPlugin('smart'), { devices: ['/dev/sda', 5] })).toEqual({ devices: '/dev/sda' });
  });

  it('round-trips a stored definition unchanged', () => {
    const stored = { name: '{address}', type: 'MX', server: '[2001:db8::53]:53', expect: 'mx.example.org', timeout_s: 3 };
    expect(paramsFrom(checkPlugin('dns'), paramValuesFrom(checkPlugin('dns'), stored))).toEqual(stored);
  });

  it.each<[string, Record<string, unknown>]>([
    ['cpu', {}],
    ['disk', { mounts: ['/', '/var'] }],
    ['service', { units: ['sshd', 'nginx'] }],
    ['network', { interfaces: ['eth0'] }],
    ['uptime', {}],
    ['docker', { selection: 'labeled', label: 'watch', socket: '/x.sock' }],
    ['temperature', { sensors: ['coretemp_core_0'] }],
    ['smart', { devices: ['/dev/sda'] }],
    ['raid', { arrays: ['md0', 'md1'] }],
    ['icmp', { count: 4, timeout_s: 9 }],
    ['tcp', { port: 6379, send: 'PING\r\n', expect: '+PONG', timeout_s: 2 }],
    ['http', { url: 'https://{address}/', method: 'HEAD', body_contains: 'ok', timeout_s: 20, follow_redirects: false, verify_tls: false }],
    ['tls', { port: 8443, server_name: 'example.org', timeout_s: 7 }],
    ['dns', { name: 'www.{address}', type: 'AAAA', server: '192.0.2.53', expect: '2001:db8::1', timeout_s: 3 }],
  ])('round-trips a stored %s definition through the form', (plugin, stored) => {
    const d = checkPlugin(plugin);
    expect(paramsFrom(d, paramValuesFrom(d, stored))).toEqual(stored);
    expect(validateParams(d, paramValuesFrom(d, stored), en)).toEqual({});
  });

  it('round-trips every plugin the descriptors list', () => {
    // A plugin added to the manager without a case above still has to
    // survive an edit unchanged.
    for (const d of CHECK_PLUGINS) {
      const stored: Record<string, unknown> = {};
      for (const p of d.params) {
        if (p.type === 'list') stored[p.name] = ['a'];
        else if (p.type === 'integer') stored[p.name] = p.min ?? 1;
        else if (p.type === 'bool') stored[p.name] = false;
        else if (p.type === 'enum') stored[p.name] = p.options?.[0];
        else stored[p.name] = 'x';
      }
      expect([d.name, paramsFrom(d, paramValuesFrom(d, stored))]).toEqual([d.name, stored]);
    }
  });

  it('shows tcp send with its control characters escaped', () => {
    expect(paramValuesFrom(checkPlugin('tcp'), { port: 6379, send: 'PING\r\n\tx\\y' })).toEqual({ port: '6379', send: 'PING\\r\\n\\tx\\\\y' });
  });

  it.each(['PING\r\n', 'stats\r\n', 'GET / HTTP/1.0\r\nHost: x\r\n\r\n', 'a\\nb', 'tab\there', 'trailing\\', '\\\\r'])(
    'round-trips tcp send %j through the form',
    (send) => {
      expect(paramsFrom(checkPlugin('tcp'), paramValuesFrom(checkPlugin('tcp'), { port: 1, send }))).toEqual({ port: 1, send });
    },
  );

  it('ignores unknown keys, wrong types and a plugin without parameters', () => {
    expect(paramValuesFrom(checkPlugin('tcp'), { port: 22, banner: 'x', send: 5 })).toEqual({ port: '22' });
    expect(paramValuesFrom(checkPlugin('cpu'), { port: 22 })).toEqual({});
    expect(paramValuesFrom(checkPlugin('tcp'), undefined)).toEqual({});
    expect(paramValuesFrom(undefined, { port: 22 })).toEqual({});
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

describe('validateParams', () => {
  const t = en;
  const range = (lo: number, hi: number) => t.checks.paramErrors.wholeRange(lo, hi);
  const fmt = t.checks.paramErrors.formats;
  it.each<[string, string, ParamValues, Record<string, string>]>([
    ['docker with nothing set is fine', 'docker', {}, {}],
    ['docker label key only', 'docker', { label: 'pulsix.monitor' }, {}],
    ['docker label key=value', 'docker', { label: 'com.example/x-y_z=true' }, {}],
    ['docker label splits at the first =', 'docker', { label: 'k=a=b' }, {}],
    ['docker label empty value', 'docker', { label: 'k=' }, {}],
    ['docker label value of 255 bytes', 'docker', { label: `k=${'a'.repeat(255)}` }, {}],
    ['docker label key of 255 bytes', 'docker', { label: 'a'.repeat(255) }, {}],
    ['docker label key of 256 bytes', 'docker', { label: 'a'.repeat(256) }, { label: fmt.dockerLabel }],
    ['docker label with a space', 'docker', { label: 'bad key' }, { label: fmt.dockerLabel }],
    ['docker label with an empty key', 'docker', { label: '=x' }, { label: fmt.dockerLabel }],
    ['docker label value of 256 bytes', 'docker', { label: `k=${'a'.repeat(256)}` }, { label: fmt.dockerLabel }],
    ['docker label value of 128 two-byte characters', 'docker', { label: `k=${'ä'.repeat(128)}` }, { label: fmt.dockerLabel }],
    ['docker label value with a control character', 'docker', { label: 'k=a\u0001b' }, { label: fmt.dockerLabel }],
    ['docker socket path', 'docker', { socket: '/var/run/docker.sock' }, {}],
    ['docker socket pipe', 'docker', { socket: 'npipe:////./pipe/docker_engine' }, {}],
    ['docker socket relative', 'docker', { socket: 'docker.sock' }, { socket: fmt.dockerSocket }],
    ['docker socket tcp', 'docker', { socket: 'tcp://127.0.0.1:2375' }, { socket: fmt.dockerSocket }],
    ['docker socket pipe without a name', 'docker', { socket: 'npipe:////./pipe/' }, { socket: fmt.dockerSocket }],
    ['docker socket of 255 bytes', 'docker', { socket: `/${'a'.repeat(254)}` }, {}],
    // The descriptor bounds the socket at 255 bytes (max_bytes), checked
    // before its format as on the server.
    ['docker socket of 256 bytes', 'docker', { socket: `/${'a'.repeat(255)}` }, { socket: t.validation.maxChars(255) }],
    ['docker socket with a control character', 'docker', { socket: '/x\u0000y' }, { socket: fmt.dockerSocket }],
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
    ['http refuses another scheme', 'http', { url: 'ftp://example.org/' }, { url: fmt.url }],
    ['http refuses user info', 'http', { url: 'https://user:pass@example.org/' }, { url: fmt.url }],
    ['http refuses a bare word', 'http', { url: 'example.org' }, { url: fmt.url }],
    ['http refuses a single slash', 'http', { url: 'http:/x' }, { url: fmt.url }],
    ['http refuses no slashes', 'http', { url: 'http:example.com' }, { url: fmt.url }],
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
    ['dns name with a space', 'dns', { name: 'exa mple.com' }, { name: fmt.dnsName }],
    ['dns name with an empty label', 'dns', { name: 'example..com' }, { name: fmt.dnsName }],
    ['dns name with a leading dot', 'dns', { name: '.example.com' }, { name: fmt.dnsName }],
    ['dns name root only', 'dns', { name: '.' }, { name: fmt.dnsName }],
    ['dns name of 254 bytes', 'dns', { name: dnsName(254) }, { name: fmt.dnsName }],
    ['dns name with a 64-byte label', 'dns', { name: `${'a'.repeat(64)}.example` }, { name: fmt.dnsName }],
    ['tcp send counts its decoded bytes', 'tcp', { port: '1', send: '\\n'.repeat(500) }, {}],
    ['dns server IPv4', 'dns', { server: '192.0.2.53' }, {}],
    ['dns server IPv4 with port', 'dns', { server: '192.0.2.53:5353' }, {}],
    ['dns server IPv6', 'dns', { server: '2001:db8::53' }, {}],
    ['dns server bracketed IPv6', 'dns', { server: '[2001:db8::53]' }, {}],
    ['dns server bracketed IPv6 with port', 'dns', { server: '[2001:db8::53]:53' }, {}],
    ['dns server a host name', 'dns', { server: 'dns.example.org' }, { server: fmt.server }],
    ['dns server port 0', 'dns', { server: '192.0.2.53:0' }, { server: fmt.server }],
    ['dns server port too big', 'dns', { server: '192.0.2.53:65536' }, { server: fmt.server }],
    ['dns server octet too big', 'dns', { server: '192.0.2.256' }, { server: fmt.server }],
    ['dns server bracketed IPv4', 'dns', { server: '[192.0.2.53]:53' }, { server: fmt.server }],
    ['dns server junk in brackets', 'dns', { server: '[::1]/x[' }, { server: fmt.server }],
    ['service needs a unit', 'service', {}, { units: t.validation.required }],
    ['service with blank lines only needs a unit', 'service', { units: ' \n ' }, { units: t.validation.required }],
    ['service with a unit is fine', 'service', { units: 'sshd' }, {}],
    ['disk and network lists are optional', 'disk', { mounts: '' }, {}],
  ])('%s', (_, plugin, values, want) => expect(validateParams(checkPlugin(plugin), values, t)).toEqual(want));

  const big = 'x'.repeat(256);
  it.each([
    ['empty is fine', 'temperature', 'sensors', '', undefined],
    ['a normal sensor', 'temperature', 'sensors', 'coretemp core 0', undefined],
    ['255 bytes pass', 'temperature', 'sensors', 'x'.repeat(255), undefined],
    ['256 bytes fail', 'temperature', 'sensors', big, t.checks.paramErrors.tooLong(255)],
    ['128 two-byte characters are 256 bytes', 'temperature', 'sensors', 'ä'.repeat(128), t.checks.paramErrors.tooLong(255)],
    ['a control character fails', 'raid', 'arrays', 'md\u00070', fmt.noControl],
    ['64 entries pass', 'raid', 'arrays', Array.from({ length: 64 }, (_, i) => `md${i}`).join('\n'), undefined],
    ['65 entries fail', 'raid', 'arrays', Array.from({ length: 65 }, (_, i) => `md${i}`).join('\n'), t.checks.paramErrors.tooMany(64)],
    ['a device path passes', 'smart', 'devices', '/dev/disk/by-id/ata-X_1,2:3+4@5', undefined],
    ['a bare name fails', 'smart', 'devices', 'sda', fmt.devicePath],
    ['a space fails', 'smart', 'devices', '/dev/sd a', fmt.devicePath],
    ['a control character in a device fails as one', 'smart', 'devices', '/dev/sd\ta', fmt.noControl],
    ['just /dev/ fails', 'smart', 'devices', '/dev/', fmt.devicePath],
    ['a second bad line fails', 'smart', 'devices', '/dev/sda\nnvme0', fmt.devicePath],
    ['a 256-byte device fails', 'smart', 'devices', `/dev/${'a'.repeat(251)}`, t.checks.paramErrors.tooLong(255)],
    ['a .. segment fails', 'smart', 'devices', '/dev/../x', fmt.devicePath],
    ['a trailing .. segment fails', 'smart', 'devices', '/dev/disk/..', fmt.devicePath],
    ['dots inside a name pass', 'smart', 'devices', '/dev/disk/by-id/nvme-eui.00..25', undefined],
    ['multibyte over 255 bytes fails', 'smart', 'devices', `/dev/${'ä'.repeat(126)}`, t.checks.paramErrors.tooLong(255)],
  ])('hardware list: %s', (_n, plugin, param, text, want) => {
    expect(validateParams(checkPlugin(plugin), { [param]: text }, t)[param]).toBe(want);
  });
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
    ['docker runs on the agent', '', 'cpu', 'docker', ''],
  ])('%s', (_, key, from, to, want) => expect(keyAfterPluginChange(key, from, to, CHECK_PLUGINS)).toBe(want));
});

describe('placeholderOf', () => {
  it.each<[string, string, string | undefined]>([
    ['icmp', 'count', '3'],
    ['tls', 'port', '443'],
    ['tcp', 'port', undefined],
    ['http', 'url', 'https://{address}/'],
    ['dns', 'name', '{address}'],
    ['docker', 'label', 'pulsix.monitor=true'],
    ['docker', 'socket', '/var/run/docker.sock'],
    ['disk', 'mounts', '/\n/var'],
    ['service', 'units', 'sshd'],
    ['network', 'interfaces', 'eth0\nbond0'],
    ['temperature', 'sensors', 'coretemp_core_0\nnvme_composite'],
    ['smart', 'devices', '/dev/sda\n/dev/nvme0'],
    ['raid', 'arrays', 'md0\nmd1'],
    ['tls', 'server_name', undefined],
  ])('%s %s → %j', (plugin, param, want) => {
    const spec = checkPlugin(plugin).params.find((p) => p.name === param);
    expect(spec).toBeDefined();
    expect(placeholderOf(spec!, paramText(en, plugin, param))).toBe(want);
  });

  it('knows no text for a parameter the catalogue does not list', () => {
    expect(paramText(en, 'tcp', 'nope')).toBeUndefined();
    expect(paramText(en, 'nope', 'port')).toBeUndefined();
    expect(paramText(en, 'constructor', 'name')).toBeUndefined();
  });
});
