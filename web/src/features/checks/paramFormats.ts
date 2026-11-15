/**
 * The TypeScript twin of every parameter format internal/checkplugins
 * (formats.go) validates with. A check plugin's descriptor names a format
 * on a string parameter, or on each entry of a list; the form refuses what
 * the format refuses before the round trip, so the operator sees the
 * problem at the field instead of a 400. The server stays the guard.
 *
 * Both sides are tested against the same vectors,
 * internal/checkplugins/testdata/formats.json (paramFormats.test.ts here).
 * Where Go's standard library decides (url.Parse, netip.ParseAddr,
 * unicode.IsSpace) this file ports the rule rather than leaning on the
 * browser's own parsers: WHATWG URL accepts http://x/%zz and refuses
 * http://x:99999/, the opposite of Go on both.
 */

// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u001f\u007f-\u009f]/;

/** Go's unicode.IsControl, applied to every character. */
function hasControl(s: string): boolean {
  return CONTROL.test(s);
}

const byteLen = (s: string) => new TextEncoder().encode(s).length;

/** Only what Go's unicode.IsSpace calls space: JavaScript's own trim()
 * also strips U+FEFF but keeps U+0085, both unlike Go. */
const GO_SPACE_ONLY = /^[\t\n\v\f\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]*$/;

const DEVICE = /^\/dev\/[A-Za-z0-9._/,:+@-]+$/;

/** devicePath: a device path smartctl is handed — under /dev/, only
 * characters that mean nothing to a shell or an option parser, no ".."
 * segment. */
function devicePath(s: string): boolean {
  return !hasControl(s) && DEVICE.test(s) && !s.split('/').includes('..');
}

// ---------------------------------------------------------------------------
// netip.ParseAddr / ParseAddrPort (Go's net/netip), as far as the formats
// below need them: whether a string parses, and as which family.
// ---------------------------------------------------------------------------

function hexValue(c: string): number {
  if (c >= '0' && c <= '9') return c.charCodeAt(0) - 48;
  if (c >= 'a' && c <= 'f') return c.charCodeAt(0) - 87;
  if (c >= 'A' && c <= 'F') return c.charCodeAt(0) - 55;
  return -1;
}

/** parseIPv4Fields: four decimal octets, no leading zero, none above 255. */
function parseIPv4(s: string): boolean {
  let val = 0;
  let pos = 0;
  let digits = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (c >= '0' && c <= '9') {
      if (digits === 1 && val === 0) return false;
      val = val * 10 + (c.charCodeAt(0) - 48);
      digits++;
      if (val > 255) return false;
    } else if (c === '.') {
      if (i === 0 || i === s.length - 1 || s[i - 1] === '.') return false;
      if (pos === 3) return false;
      pos++;
      val = 0;
      digits = 0;
    } else {
      return false;
    }
  }
  return pos >= 3;
}

/** parseIPv6, zone (after '%', not empty) and embedded IPv4 included. */
function parseIPv6(input: string): boolean {
  let s = input;
  const pct = s.indexOf('%');
  if (pct !== -1) {
    if (pct === s.length - 1) return false;
    s = s.slice(0, pct);
  }
  let ellipsis = -1;
  if (s.length >= 2 && s[0] === ':' && s[1] === ':') {
    ellipsis = 0;
    s = s.slice(2);
    if (s.length === 0) return true;
  }
  let i = 0;
  while (i < 16) {
    let off = 0;
    let acc = 0;
    for (; off < s.length; off++) {
      const d = hexValue(s[off]);
      if (d < 0) break;
      acc = acc * 16 + d;
      if (off > 3 || acc > 0xffff) return false;
    }
    if (off === 0) return false;
    if (off < s.length && s[off] === '.') {
      if ((ellipsis < 0 && i !== 12) || i + 4 > 16) return false;
      if (!parseIPv4(s)) return false;
      s = '';
      i += 4;
      break;
    }
    i += 2;
    s = s.slice(off);
    if (s.length === 0) break;
    if (s[0] !== ':' || s.length === 1) return false;
    s = s.slice(1);
    if (s[0] === ':') {
      if (ellipsis >= 0) return false;
      ellipsis = i;
      s = s.slice(1);
      if (s.length === 0) break;
    }
  }
  if (s.length !== 0) return false;
  return i < 16 ? ellipsis >= 0 : ellipsis < 0;
}

/** parseAddr is netip.ParseAddr: 4 or 6 for the family it parsed as
 * (an IPv4-mapped IPv6 address is 6, as Go's Is6 says), null when it does
 * not parse. */
function parseAddr(s: string): 4 | 6 | null {
  for (const c of s) {
    if (c === '.') return parseIPv4(s) ? 4 : null;
    if (c === ':') return parseIPv6(s) ? 6 : null;
    if (c === '%') return null;
  }
  return null;
}

/** parseAddrPort is netip.ParseAddrPort: the port when s is ip:port or
 * [ipv6]:port, null otherwise. */
function parseAddrPort(s: string): number | null {
  const colon = s.lastIndexOf(':');
  if (colon === -1) return null;
  let ip = s.slice(0, colon);
  const port = s.slice(colon + 1);
  if (ip === '' || port === '') return null;
  let v6 = false;
  if (ip[0] === '[') {
    if (ip.length < 2 || ip[ip.length - 1] !== ']') return null;
    ip = ip.slice(1, -1);
    v6 = true;
  }
  if (!/^[0-9]+$/.test(port) || Number(port) > 65535) return null;
  const family = parseAddr(ip);
  if (family === null || (v6 && family === 4) || (!v6 && family === 6)) return null;
  return Number(port);
}

/** server: an IP address with an optional port (not 0), an IPv6 address
 * with a port in brackets — validDNSServer. */
function server(s: string): boolean {
  const port = parseAddrPort(s);
  if (port !== null) return port !== 0;
  if (s.startsWith('[')) return s.endsWith(']') && s.length >= 2 && parseAddr(s.slice(1, -1)) === 6;
  return parseAddr(s) !== null;
}

// ---------------------------------------------------------------------------
// url.Parse (Go's net/url), as far as validProbeURL needs it: whether the
// parse succeeds, the scheme, whether user info is present and whether
// the host name is empty.
// ---------------------------------------------------------------------------

/** The bytes Go lets a host (and an IPv6 zone) carry unescaped:
 * shouldEscape(c, encodeHost) is false for exactly these. */
const HOST_BYTE = /^[A-Za-z0-9!$&'()*+,;=:[\]<>"\-_.~]$/;

/** validOptionalPort: "" or a colon followed by digits only. */
function validOptionalPort(port: string): boolean {
  return port === '' || /^:[0-9]*$/.test(port);
}

/** unescape checks s's percent escapes for a URL part and decodes them
 * (each escaped byte becomes one character, enough to tell an empty host
 * name and to hand an address to parseAddr). `host` and `zone` also
 * refuse an unescaped byte a host cannot carry; null is Go's error. */
function unescape(s: string, mode: 'host' | 'zone' | 'other'): string | null {
  let out = '';
  for (let i = 0; i < s.length; ) {
    if (s[i] === '%') {
      if (i + 2 >= s.length || hexValue(s[i + 1]) < 0 || hexValue(s[i + 2]) < 0) return null;
      const escape = s.slice(i, i + 3);
      const v = hexValue(s[i + 1]) * 16 + hexValue(s[i + 2]);
      if (mode === 'host' && hexValue(s[i + 1]) < 8 && escape !== '%25') return null;
      if (mode === 'zone' && escape !== '%25' && v !== 0x20 && !(v < 0x80 && HOST_BYTE.test(String.fromCharCode(v)))) return null;
      out += String.fromCharCode(v);
      i += 3;
    } else {
      if (mode !== 'other' && s.charCodeAt(i) < 0x80 && !HOST_BYTE.test(s[i])) return null;
      out += s[i];
      i++;
    }
  }
  return out;
}

/** parseHost: the host (and port) of an authority without user info,
 * unescaped, or null. A bracketed host must be an IPv6 address; an
 * unbracketed one takes at most one colon (Go's strict colons for http). */
function parseHost(host: string): string | null {
  const open = host.lastIndexOf('[');
  if (open > 0) return null;
  if (open === 0) {
    const close = host.lastIndexOf(']');
    if (close < 0) return null;
    const colonPort = host.slice(close + 1);
    if (!validOptionalPort(colonPort)) return null;
    const name = host.slice(1, close);
    const zone = name.indexOf('%25');
    const address = zone >= 0 ? joinOrNull(unescape(name.slice(0, zone), 'host'), unescape(name.slice(zone), 'zone')) : unescape(name, 'host');
    if (address === null || parseAddr(address) !== 6) return null;
    return `[${address}]${colonPort}`;
  }
  const colon = host.indexOf(':');
  if (colon !== -1 && !validOptionalPort(host.slice(colon))) return null;
  return unescape(host, 'host');
}

function joinOrNull(a: string | null, b: string | null): string | null {
  return a === null || b === null ? null : a + b;
}

/** hostname is URL.Hostname(): the host without its port and brackets. */
function hostname(host: string): string {
  let h = host;
  const colon = h.lastIndexOf(':');
  if (colon !== -1 && validOptionalPort(h.slice(colon))) h = h.slice(0, colon);
  if (h.startsWith('[') && h.endsWith(']')) h = h.slice(1, -1);
  return h;
}

/** url: an http:// or https:// URL with a host and no user info, once
 * {address} stands for a host name — validProbeURL over url.Parse. */
function probeURL(raw: string): boolean {
  const s = raw.replaceAll('{address}', 'address.invalid');
  const hash = s.indexOf('#');
  const main = hash < 0 ? s : s.slice(0, hash);
  const fragment = hash < 0 ? '' : s.slice(hash + 1);
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(main)) return false;
  const scheme = /^([A-Za-z][A-Za-z0-9+.-]*):/.exec(main);
  if (!scheme || !['http', 'https'].includes(scheme[1].toLowerCase())) return false;
  let rest = main.slice(scheme[0].length);
  const query = rest.indexOf('?');
  if (query >= 0) rest = rest.slice(0, query);
  // Without "//" Go reads an opaque URL or a path: no host either way.
  if (!rest.startsWith('//')) return false;
  let authority = rest.slice(2);
  let path = '';
  const slash = authority.indexOf('/');
  if (slash >= 0) {
    path = authority.slice(slash);
    authority = authority.slice(0, slash);
  }
  // Any '@' is user info (Go sets URL.User, possibly to an empty name) or
  // a parse error.
  if (authority.includes('@')) return false;
  const host = parseHost(authority);
  if (host === null || unescape(path, 'other') === null) return false;
  if (fragment !== '' && unescape(fragment, 'other') === null) return false;
  return hostname(host) !== '';
}

/** statusURL: a probe URL fetched as written — no {address} placeholder and
 * no '#' anywhere — validStatusURL. */
function statusURL(s: string): boolean {
  return !s.includes('{address}') && !s.includes('#') && probeURL(s);
}

// ---------------------------------------------------------------------------

/** dnsName: dot-separated labels of letters, digits, '-' and '_', each
 * 1–63 bytes, an optional trailing dot, at most 253 bytes without it, once
 * {address} stands for a label. */
function dnsName(s: string): boolean {
  let name = s.replaceAll('{address}', 'address');
  if (name.endsWith('.')) name = name.slice(0, -1);
  if (name === '' || byteLen(name) > 253) return false;
  return name.split('.').every((label) => /^[A-Za-z0-9_-]{1,63}$/.test(label));
}

/** dockerLabel: a key (1–255 of letters, digits, '.', '_', '/', '-') with
 * an optional =value split at the first '=', the value at most 255 bytes
 * without control characters and possibly empty. */
function dockerLabel(s: string): boolean {
  const eq = s.indexOf('=');
  const key = eq < 0 ? s : s.slice(0, eq);
  const value = eq < 0 ? '' : s.slice(eq + 1);
  return /^[A-Za-z0-9._/-]{1,255}$/.test(key) && byteLen(value) <= 255 && !hasControl(value);
}

const PIPE_PREFIX = 'npipe:////./pipe/';

/** dockerSocket: an absolute path or a Windows named pipe, without
 * control characters. */
function dockerSocket(s: string): boolean {
  if (hasControl(s)) return false;
  return s.startsWith('/') || (s.startsWith(PIPE_PREFIX) && s.length > PIPE_PREFIX.length);
}

/** secretName: the name of a secret in the agent's agent.yaml — 1 to 64
 * letters, digits, '_', '.' or '-'. */
function secretName(s: string): boolean {
  return /^[A-Za-z0-9_.-]{1,64}$/.test(s);
}

/** commandName: the name of a command under commands: in the agent's
 * agent.yaml — the same rule as secretName. */
const commandName = secretName;

/** snmpOID: one entry of snmp's oids, "name=OID" split at the first '=':
 * the name follows secretName, the OID is numeric and dotted with an
 * optional leading dot, 2 to 128 arcs, each a decimal of at most
 * 4294967295 without leading zeros, and BER-encodable (X.690 8.19): the
 * first arc 0, 1 or 2, the second at most 39 under 0 and 1, and
 * 40*first+second at most 4294967295. */
function snmpOID(s: string): boolean {
  const eq = s.indexOf('=');
  if (eq < 0 || !secretName(s.slice(0, eq))) return false;
  let oid = s.slice(eq + 1);
  if (oid.startsWith('.')) oid = oid.slice(1);
  const arcs = oid.split('.');
  if (
    arcs.length < 2 ||
    arcs.length > 128 ||
    !arcs.every((arc) => /^(0|[1-9][0-9]{0,9})$/.test(arc) && Number(arc) <= 4294967295)
  ) {
    return false;
  }
  const first = Number(arcs[0]);
  const second = Number(arcs[1]);
  return first <= 2 && (first === 2 || second <= 39) && 40 * first + second <= 4294967295;
}

/** jsonPathDescendants counts the descendant segments of a JSONPath, as
 * the server's parser (internal/jpath) counts them: every ".." outside a
 * string literal, filters included. */
function jsonPathDescendants(path: string): number {
  let n = 0;
  let quote = '';
  for (let i = 0; i < path.length; i++) {
    const c = path[i];
    if (quote) {
      if (c === '\\') i++;
      else if (c === quote) quote = '';
    } else if (c === "'" || c === '"') {
      quote = c;
    } else if (c === '.' && path[i + 1] === '.') {
      n++;
      i++;
    }
  }
  return n;
}

/** jsonValue: one entry of http's json_values, "name=path" split at the
 * first '=': the name follows secretName, the path starts with $, is at
 * most 512 bytes and has at most one descendant segment (..). Whether the
 * path is in the supported JSONPath subset the server decides; its parser
 * has no twin here. */
function jsonValue(s: string): boolean {
  const eq = s.indexOf('=');
  if (eq < 0 || !secretName(s.slice(0, eq))) return false;
  const path = s.slice(eq + 1);
  return path.startsWith('$') && byteLen(path) <= 512 && jsonPathDescendants(path) <= 1;
}

/** Every format, by the name the descriptors use. */
export const FORMATS: Readonly<Record<string, (s: string) => boolean>> = {
  notBlank: (s) => !GO_SPACE_ONLY.test(s),
  noControl: (s) => !hasControl(s),
  devicePath,
  url: probeURL,
  statusURL,
  dnsName,
  server,
  dockerLabel,
  dockerSocket,
  secretName,
  commandName,
  snmpOID,
  jsonValue,
};

/** formatValid reports whether s passes the named format. An unknown
 * format accepts nothing, as on the server. */
export function formatValid(format: string, s: string): boolean {
  const check = Object.hasOwn(FORMATS, format) ? FORMATS[format] : undefined;
  return check ? check(s) : false;
}

/** formatProblem names the format whose message explains why s fails
 * `format`, or returns null when it passes. A device path with a control
 * character is refused for the control character, as the server says. */
export function formatProblem(format: string, s: string): string | null {
  if (formatValid(format, s)) return null;
  if (format === 'devicePath' && hasControl(s)) return 'noControl';
  return format;
}
