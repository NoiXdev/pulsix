import type { Messages } from '../../i18n/types.ts';

/**
 * The synthetic plugins' parameters (backlog 3.13): the five plugins the
 * Pulsix server probes itself, their fields, and the client side of the
 * validation internal/checks/synthetic_params.go enforces. The server stays
 * the guard; this exists so an operator sees a field error before the
 * round trip instead of a 400 naming the parameter.
 *
 * A form holds only the values the user set (or the stored definition
 * had): a field left untouched is absent, shows its default as a
 * placeholder or a preselection, and is not sent — the server applies the
 * default itself.
 */

export const SYNTHETIC_PLUGINS = ['icmp', 'tcp', 'http', 'tls', 'dns'] as const;
export type SyntheticPlugin = (typeof SYNTHETIC_PLUGINS)[number];

export function isSynthetic(plugin: string): plugin is SyntheticPlugin {
  return (SYNTHETIC_PLUGINS as readonly string[]).includes(plugin);
}

/** The form's value for each parameter it holds: the text of a text or
 * number input, the chosen option of a select, the state of a switch. */
export type SyntheticValues = Record<string, string | boolean>;

type Label = keyof Messages['checks']['synthetic']['fields'];
type Hint = keyof Messages['checks']['synthetic']['hints'];

interface Base {
  /** The parameter's name in the API. */
  name: string;
  label: Label;
  hint?: Hint;
}

/** One parameter field. `placeholder` shows the default the server applies
 * while the field is empty. */
export type SyntheticField =
  | (Base & { kind: 'number'; min: number; max: number; placeholder?: string; required?: boolean })
  | (Base & { kind: 'text'; check?: 'url' | 'server'; trim?: boolean; placeholder?: string; required?: boolean })
  | (Base & { kind: 'select'; options: readonly string[]; default: string })
  | (Base & { kind: 'bool'; default: boolean });

const timeout = (max: number, dflt: number): SyntheticField => ({
  name: 'timeout_s', label: 'timeout_s', kind: 'number', min: 1, max, placeholder: String(dflt),
});

/** The spec's Plugins table, in the order the form shows the fields. */
export const SYNTHETIC_FIELDS: Record<SyntheticPlugin, readonly SyntheticField[]> = {
  icmp: [
    { name: 'count', label: 'count', kind: 'number', min: 1, max: 10, placeholder: '3' },
    timeout(30, 5),
  ],
  tcp: [
    { name: 'port', label: 'port', kind: 'number', min: 1, max: 65535, required: true },
    { name: 'send', label: 'send', kind: 'text' },
    { name: 'expect', label: 'expect', hint: 'expect', kind: 'text' },
    timeout(30, 5),
  ],
  http: [
    { name: 'url', label: 'url', hint: 'url', kind: 'text', check: 'url', trim: true, required: true, placeholder: 'https://{address}/' },
    { name: 'method', label: 'method', kind: 'select', options: ['GET', 'HEAD'], default: 'GET' },
    { name: 'body_contains', label: 'body_contains', hint: 'body_contains', kind: 'text' },
    timeout(60, 10),
    { name: 'follow_redirects', label: 'follow_redirects', kind: 'bool', default: true },
    { name: 'verify_tls', label: 'verify_tls', kind: 'bool', default: true },
  ],
  tls: [
    { name: 'port', label: 'port', kind: 'number', min: 1, max: 65535, placeholder: '443' },
    { name: 'server_name', label: 'server_name', hint: 'server_name', kind: 'text', trim: true },
    timeout(30, 10),
  ],
  dns: [
    { name: 'name', label: 'name', hint: 'name', kind: 'text', trim: true, placeholder: '{address}' },
    { name: 'type', label: 'type', kind: 'select', options: ['A', 'AAAA', 'CNAME', 'MX', 'TXT', 'NS'], default: 'A' },
    { name: 'server', label: 'server', hint: 'server', kind: 'text', check: 'server', trim: true },
    { name: 'expect', label: 'answer', hint: 'answer', kind: 'text' },
    timeout(30, 5),
  ],
};

/** maxParamStringLen in internal/checks: every string parameter's bound,
 * in bytes. */
const MAX_TEXT_BYTES = 500;

/** textOf is a text field's value as it would be sent: trimmed where the
 * field is a name or address, verbatim where it is a substring to look
 * for. */
function textOf(field: SyntheticField, value: string | boolean | undefined): string {
  if (typeof value !== 'string') return '';
  return field.kind === 'text' && field.trim ? value.trim() : value;
}

/** syntheticParams builds the params object a save sends: only the fields
 * the form holds a value for, a blank text or number field counting as
 * not set. */
export function syntheticParams(plugin: SyntheticPlugin, values: SyntheticValues): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const field of SYNTHETIC_FIELDS[plugin]) {
    const value = values[field.name];
    if (value === undefined) continue;
    switch (field.kind) {
      case 'number':
        if (typeof value === 'string' && value.trim() !== '') out[field.name] = Number(value);
        break;
      case 'text': {
        const text = textOf(field, value);
        if (text !== '') out[field.name] = text;
        break;
      }
      case 'select':
        if (typeof value === 'string') out[field.name] = value;
        break;
      case 'bool':
        if (typeof value === 'boolean') out[field.name] = value;
        break;
    }
  }
  return out;
}

/** syntheticValuesFrom reads a stored definition's params back into form
 * values, so an edit starts from what is saved and saves it back
 * unchanged. Keys the plugin does not know, and values of the wrong type,
 * are dropped: the server refuses both anyway. */
export function syntheticValuesFrom(plugin: string, params: Record<string, unknown> | undefined): SyntheticValues {
  const out: SyntheticValues = {};
  if (!isSynthetic(plugin) || !params) return out;
  for (const field of SYNTHETIC_FIELDS[plugin]) {
    const value = params[field.name];
    if (field.kind === 'number' && typeof value === 'number') out[field.name] = String(value);
    else if ((field.kind === 'text' || field.kind === 'select') && typeof value === 'string') out[field.name] = value;
    else if (field.kind === 'bool' && typeof value === 'boolean') out[field.name] = value;
  }
  return out;
}

const IPV4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;

function isIPv4(s: string): boolean {
  return IPV4.test(s);
}

/** isIPv6 leans on the URL parser's own IPv6 grammar, after making sure
 * the text holds nothing but address characters (so it cannot close the
 * brackets and smuggle in a path). Zone ids are not accepted. */
function isIPv6(s: string): boolean {
  if (!/^[0-9a-fA-F:.]+$/.test(s) || !s.includes(':')) return false;
  try {
    new URL(`http://[${s}]/`);
    return true;
  } catch {
    return false;
  }
}

function isPort(s: string): boolean {
  return /^\d{1,5}$/.test(s) && Number(s) >= 1 && Number(s) <= 65535;
}

/** isDNSServer mirrors paramDNSServer: an IP with an optional port, an
 * IPv6 address with a port in brackets. */
export function isDNSServer(s: string): boolean {
  const bracketed = /^\[([^\]]*)\](?::(.*))?$/.exec(s);
  if (bracketed) return isIPv6(bracketed[1]) && (bracketed[2] === undefined || isPort(bracketed[2]));
  const colons = s.split(':').length - 1;
  if (colons >= 2) return isIPv6(s);
  if (colons === 1) {
    const [ip, port] = s.split(':');
    return isIPv4(ip) && isPort(port);
  }
  return isIPv4(s);
}

/** isProbeURL mirrors paramHTTPURL: an http(s) URL with a host and no
 * user info, once {address} stands for a host name. */
export function isProbeURL(s: string): boolean {
  try {
    const u = new URL(s.replaceAll('{address}', 'address.invalid'));
    return (u.protocol === 'http:' || u.protocol === 'https:') && u.hostname !== '' && u.username === '' && u.password === '';
  } catch {
    return false;
  }
}

/** validateSynthetic returns one message per refused field, keyed by the
 * parameter's name; an empty object means the values may be sent. */
export function validateSynthetic(plugin: SyntheticPlugin, values: SyntheticValues, t: Messages): Record<string, string> {
  const errors: Record<string, string> = {};
  const msg = t.checks.synthetic.errors;
  for (const field of SYNTHETIC_FIELDS[plugin]) {
    const value = values[field.name];
    if (field.kind === 'number') {
      const text = typeof value === 'string' ? value.trim() : '';
      if (text === '') {
        if (field.required) errors[field.name] = t.validation.required;
      } else if (!/^\d+$/.test(text) || Number(text) < field.min || Number(text) > field.max) {
        errors[field.name] = msg.wholeRange(field.min, field.max);
      }
    } else if (field.kind === 'text') {
      const text = textOf(field, value);
      if (text === '') {
        if (field.required) errors[field.name] = t.validation.required;
      } else if (new TextEncoder().encode(text).length > MAX_TEXT_BYTES) {
        errors[field.name] = t.validation.maxChars(MAX_TEXT_BYTES);
      } else if (field.check === 'url' && !isProbeURL(text)) {
        errors[field.name] = msg.url;
      } else if (field.check === 'server' && !isDNSServer(text)) {
        errors[field.name] = msg.server;
      }
    }
  }
  return errors;
}

/**
 * keyAfterPluginChange is the key a new definition's form shows after its
 * plugin changes. Alert rules match a synthetic metric only on the check
 * whose key is the plugin name (the rules catalogue binds http.up to the
 * key "http"), so picking one of the five fills that key in rather than
 * leaving it to the server's blank-key default, where nobody sees it. A
 * key the user typed is theirs and stays; a key this filled in follows
 * the plugin, and goes back to blank when the plugin is no longer
 * synthetic (the server then defaults it to the plugin name, as before).
 */
export function keyAfterPluginChange(key: string, from: string, to: string): string {
  if (isSynthetic(to) && (key === '' || key === from)) return to;
  if (isSynthetic(from) && !isSynthetic(to) && key === from) return '';
  return key;
}
