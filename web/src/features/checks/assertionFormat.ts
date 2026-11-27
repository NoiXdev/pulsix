/**
 * The TypeScript twin of the synthetic checks' assertion grammar
 * (internal/probe/assert, backlog 3.13b for http, 3.87 for tcp, dns and
 * tls): one entry of `assertions`, `name=expression` with an optional
 * ` @warn` or ` @crit`, in the subjects of the check's plugin.
 *
 *   assertion = subject SP op [SP operand]
 *
 * It mirrors the server's parser token for token, so both agree on every
 * vector of internal/checkplugins/testdata/formats.json. What only Go's
 * libraries judge — whether a regular expression compiles in RE2, a CSS
 * selector parses, a JSONPath is in the server's subset — is left to the
 * server beyond its length and shape: the form lets it through and the
 * API's 400 names the reason.
 */

// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u001f\u007f-\u009f]/;
const NAME = /^[A-Za-z0-9_.-]{1,64}$/;
const NUMBER = /^[+-]?[0-9]+(\.[0-9]+)?$/;
/** httpguts.ValidHeaderFieldName: an RFC 7230 token. */
const HEADER_NAME = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/;

const MAX_ENTRY = 512;
const MAX_REGEX = 256;
const MAX_SELECTOR = 256;
const MAX_ANSWER = 256;

const byteLen = (s: string) => new TextEncoder().encode(s).length;

export type AssertionSubject =
  | 'status'
  | 'response_ms'
  | 'ttfb_ms'
  | 'size'
  | 'redirects'
  | 'cert_days'
  | 'cert_issuer'
  | 'body'
  | 'header'
  | 'json'
  | 'html'
  | 'html count'
  | 'connect_ms'
  | 'response'
  | 'resolve_ms'
  | 'answer_count'
  | 'answers'
  | 'answer'
  | 'handshake_ms'
  | 'cert_valid'
  | 'cert_subject'
  | 'cert_sans'
  | 'version'
  | 'cipher';

/** The plugins with assertions (backlog 3.13b, 3.87). */
export type AssertionPlugin = 'http' | 'tcp' | 'dns' | 'tls';

/** DIALECTS: each plugin's subjects as an expression spells them, in the
 * server's order (internal/probe/assert dialect.go); `html count` is
 * spelled with `html`. */
const DIALECTS: Readonly<Record<AssertionPlugin, readonly AssertionSubject[]>> = {
  http: ['status', 'response_ms', 'ttfb_ms', 'size', 'redirects', 'cert_days', 'cert_issuer', 'body', 'header', 'json', 'html'],
  tcp: ['connect_ms', 'response_ms', 'response'],
  dns: ['resolve_ms', 'answer_count', 'answers', 'answer'],
  tls: ['handshake_ms', 'cert_valid', 'cert_days', 'cert_issuer', 'cert_subject', 'cert_sans', 'version', 'cipher'],
};

/** isAssertionPlugin: a plugin whose checks take assertions. */
export function isAssertionPlugin(plugin: string): plugin is AssertionPlugin {
  return Object.hasOwn(DIALECTS, plugin);
}

/** dialectSubjects: the subjects a plugin's assertions take, `html count`
 * listed after `html`, in the order an editor offers them. */
export function dialectSubjects(plugin: AssertionPlugin): AssertionSubject[] {
  return DIALECTS[plugin].flatMap((s) => (s === 'html' ? (['html', 'html count'] as const) : [s]));
}

/** takesArg: the subjects written with a quoted argument. */
export function subjectTakesArg(s: AssertionSubject): boolean {
  return s === 'header' || s === 'json' || s === 'html' || s === 'html count' || s === 'answer';
}

const NUMERIC_OPS = ['<', '<=', '>', '>=', '==', '!=', 'in'];
const TEXT_OPS = ['==', '!=', 'contains', '!contains', 'matches', '!matches'];
const PRESENCE_OPS = ['exists', '!exists'];

/** isNumericSubject: the subjects that compare a number. */
export function isNumericSubject(s: AssertionSubject): boolean {
  return [
    'status',
    'response_ms',
    'ttfb_ms',
    'size',
    'redirects',
    'cert_days',
    'html count',
    'connect_ms',
    'resolve_ms',
    'answer_count',
    'handshake_ms',
    'cert_valid',
    'version',
  ].includes(s);
}

/** assertionOps: the operators a subject takes, in the server's order. */
export function assertionOps(s: AssertionSubject): string[] {
  if (isNumericSubject(s)) return NUMERIC_OPS;
  if (s === 'answer') return PRESENCE_OPS;
  if (s === 'header' || s === 'json' || s === 'html') return [...TEXT_OPS, ...PRESENCE_OPS];
  return TEXT_OPS;
}

/** One parsed entry; operand is as written ('' for exists and !exists). */
export interface AssertionShape {
  name: string;
  subject: AssertionSubject;
  arg: string;
  op: string;
  operand: string;
  severity: 'warn' | 'crit';
}

const isLetter = (c: string) => /^[A-Za-z_]$/.test(c);
const isSymbol = (c: string) => c === '<' || c === '>' || c === '=' || c === '!';

/** jsonPathDescendants counts a JSONPath's descendant segments (..)
 * outside string literals, as the server's parser does. */
export function jsonPathDescendants(path: string): number {
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

class Scanner {
  i = 0;
  constructor(readonly s: string) {}

  done(): boolean {
    return this.i >= this.s.length;
  }

  peek(): string {
    return this.s[this.i] ?? '';
  }

  spaces(): void {
    while (this.peek() === ' ') this.i++;
  }

  word(accept: (c: string) => boolean): string {
    const start = this.i;
    while (!this.done() && accept(this.peek())) this.i++;
    return this.s.slice(start, this.i);
  }

  rest(): string {
    return this.word((c) => c !== ' ');
  }

  /** str reads a JSON string literal, or returns null. */
  str(): string | null {
    if (this.peek() !== '"') return null;
    let end = this.i + 1;
    for (; end < this.s.length; end++) {
      if (this.s[end] === '\\') {
        end++;
        continue;
      }
      if (this.s[end] === '"') break;
    }
    if (end >= this.s.length) return null;
    try {
      const v: unknown = JSON.parse(this.s.slice(this.i, end + 1));
      this.i = end + 1;
      return typeof v === 'string' ? v : null;
    } catch {
      return null;
    }
  }
}

function finiteNumber(s: string): number | null {
  if (!NUMBER.test(s)) return null;
  const n = Number(s);
  return Number.isFinite(n) ? n : null;
}

function argValid(subject: AssertionSubject, arg: string): boolean {
  switch (subject) {
    case 'header':
      return HEADER_NAME.test(arg);
    case 'json':
      return arg.startsWith('$') && jsonPathDescendants(arg) <= 1;
    case 'answer':
      return arg.trim() !== '' && byteLen(arg) <= MAX_ANSWER;
    default:
      return arg.trim() !== '' && byteLen(arg) <= MAX_SELECTOR;
  }
}

/** parseAssertion parses one entry of a plugin's check (http by
 * default) the way the server does, or returns null when the server's
 * grammar refuses it. */
export function parseAssertion(entry: string, plugin: AssertionPlugin = 'http'): AssertionShape | null {
  if (byteLen(entry) > MAX_ENTRY || CONTROL.test(entry)) return null;
  const eq = entry.indexOf('=');
  if (eq < 0 || !NAME.test(entry.slice(0, eq))) return null;
  const p = new Scanner(entry.slice(eq + 1));

  p.spaces();
  const word = p.word(isLetter);
  if (!(DIALECTS[plugin] as readonly string[]).includes(word)) return null;
  let subject = word as AssertionSubject;
  let arg = '';
  const takesArg = subjectTakesArg(subject);
  if (takesArg !== (p.peek() === '(')) return null;
  if (takesArg) {
    p.i++;
    const a = p.str();
    if (a === null || p.peek() !== ')') return null;
    p.i++;
    if (!argValid(subject, a)) return null;
    arg = a;
    if (subject === 'html') {
      const save = p.i;
      p.spaces();
      if (p.word(isLetter) === 'count' && (p.done() || p.peek() === ' ')) subject = 'html count';
      else p.i = save;
    }
  }

  p.spaces();
  let op: string;
  if (p.peek() === '!' && isLetter(p.s[p.i + 1] ?? '')) {
    p.i++;
    op = '!' + p.word(isLetter);
  } else if (isLetter(p.peek())) {
    op = p.word(isLetter);
  } else {
    op = p.word(isSymbol);
  }
  if (!assertionOps(subject).includes(op)) return null;

  p.spaces();
  let operand = '';
  if (PRESENCE_OPS.includes(op)) {
    if (!p.done() && p.peek() !== '@') return null;
  } else if (op === 'in') {
    operand = p.rest();
    const dots = operand.indexOf('..');
    if (dots < 0) return null;
    const lo = finiteNumber(operand.slice(0, dots));
    const hi = finiteNumber(operand.slice(dots + 2));
    if (lo === null || hi === null || lo > hi) return null;
  } else if (isNumericSubject(subject)) {
    operand = p.rest();
    if (finiteNumber(operand) === null) return null;
  } else {
    const start = p.i;
    const text = p.str();
    if (text === null) return null;
    if ((op === 'matches' || op === '!matches') && byteLen(text) > MAX_REGEX) return null;
    operand = p.s.slice(start, p.i);
  }

  let severity: 'warn' | 'crit' = 'crit';
  p.spaces();
  if (p.peek() === '@') {
    const start = p.i;
    const tag = p.rest();
    if (tag === '@warn') severity = 'warn';
    else if (tag !== '@crit') p.i = start;
    p.spaces();
  }
  if (!p.done()) return null;
  return { name: entry.slice(0, eq), subject, arg, op, operand, severity };
}

/** assertion: the format of an http check's `assertions` entry. */
export function assertion(s: string): boolean {
  return parseAssertion(s) !== null;
}

/** tcpAssertion, dnsAssertion, tlsAssertion: the formats of the other
 * plugins' entries (backlog 3.87), the same grammar with their subjects. */
export const tcpAssertion = (s: string): boolean => parseAssertion(s, 'tcp') !== null;
export const dnsAssertion = (s: string): boolean => parseAssertion(s, 'dns') !== null;
export const tlsAssertion = (s: string): boolean => parseAssertion(s, 'tls') !== null;
