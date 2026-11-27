import type { CheckPluginDescriptor, CheckPluginParam } from '../../api/types.ts';
import type { ParamText } from '../../i18n/en/checks.ts';
import type { Messages } from '../../i18n/types.ts';
import { isAssertionPlugin } from './assertionFormat.ts';
import { formatProblem } from './paramFormats.ts';

/**
 * The parameter half of a check form, built from the plugin's descriptor
 * (GET /check-plugins, backlog 3.54) for both the organization's check
 * dialog and the instance's shared check form: the values the form holds,
 * the params a save sends, and the client side of the validation
 * internal/checkplugins enforces. The server stays the guard; this exists
 * so an operator sees a field error before the round trip instead of a 400
 * naming the parameter.
 *
 * A form holds only the values the user set (or the stored definition
 * had): a field left untouched is absent, shows its default as a
 * placeholder or a preselection, and is not sent — the server applies the
 * default itself. A list field holds the textarea's text, one entry per
 * line; an empty list is not sent either (an optional list left out means
 * every mount, sensor, …).
 */

/** The form's value for each parameter it holds: the text of a text,
 * number or list field, the chosen option of a select, the state of a
 * switch. */
export type ParamValues = Record<string, string | boolean>;

/**
 * The parameters whose text field takes \r, \n, \t and \\ escapes, decoded
 * on save and encoded on load: a single-line input cannot hold the line
 * break a tcp probe's `send` usually ends with. A rule of this form only —
 * the API takes the decoded string.
 */
const ESCAPED: ReadonlySet<string> = new Set(['tcp.send']);

export function takesEscapes(plugin: string, param: string): boolean {
  return ESCAPED.has(`${plugin}.${param}`);
}

/** isAssertions: a check's assertions (backlog 3.13b for http, 3.87 for
 * tcp, dns and tls), which the forms edit with AssertionEditor rather
 * than a plain textarea. */
export function isAssertions(plugin: string, param: string): boolean {
  return isAssertionPlugin(plugin) && param === 'assertions';
}

/** isMultiline: a string parameter edited in a textarea, its line breaks
 * sent as typed — the http check's request body (backlog 3.84). */
export function isMultiline(plugin: string, param: string): boolean {
  return plugin === 'http' && param === 'body';
}

/** The http methods that ask the target to change something; the form
 * warns while one is chosen (backlog 3.84). */
const STATE_CHANGING: ReadonlySet<string> = new Set(['POST', 'PUT', 'PATCH', 'DELETE']);

/** changesState says whether the form's http method is one that asks the
 * target to change something. */
export function changesState(plugin: string, values: ParamValues): boolean {
  return plugin === 'http' && typeof values.method === 'string' && STATE_CHANGING.has(values.method);
}

/** customRequest says whether the form's http check sends a method other
 * than GET or HEAD, headers or a body (backlog 3.84): such a check is
 * probed only from the Pulsix server (the plugin's own rule), since an
 * agent of an earlier release would send a plain GET. */
export function customRequest(plugin: string, values: ParamValues): boolean {
  if (plugin !== 'http') return false;
  const text = (name: string) => (typeof values[name] === 'string' ? (values[name] as string) : '');
  const method = text('method');
  return (
    (method !== '' && method !== 'GET' && method !== 'HEAD') ||
    lines(text('headers')).length > 0 ||
    text('body') !== '' ||
    text('content_type').trim() !== ''
  );
}

/** The vantage-point parameters every synthetic plugin takes (backlog
 * 3.13a). The organization's check dialog edits them as their own section
 * (VantageFields); the instance's shared check form, which knows no
 * organization's monitoring groups, leaves them out, so a shared check is
 * probed from the Pulsix server. */
export const VANTAGE_MANAGER = 'vantage_manager';
export const VANTAGE_GROUPS = 'vantage_groups';
export const VANTAGE_AGENTS_PER_GROUP = 'agents_per_group';
export const VANTAGE_FAIL_POLICY = 'fail_policy';
export const VANTAGE_PARAMS: ReadonlySet<string> = new Set([
  VANTAGE_MANAGER, VANTAGE_GROUPS, VANTAGE_AGENTS_PER_GROUP, VANTAGE_FAIL_POLICY,
]);

/** vantageGroupsOf is the monitoring group ids the form holds, in order. */
export function vantageGroupsOf(values: ParamValues): string[] {
  const value = values[VANTAGE_GROUPS];
  return typeof value === 'string' ? lines(value) : [];
}

/** findPlugin is the descriptor of the plugin called name, if the list
 * has one. */
export function findPlugin(plugins: readonly CheckPluginDescriptor[], name: string): CheckPluginDescriptor | undefined {
  return plugins.find((p) => p.name === name);
}

/** runsOnServer says whether the Pulsix server, not the host's agent,
 * runs the plugin's check (the synthetic probes). */
export function runsOnServer(plugins: readonly CheckPluginDescriptor[], name: string): boolean {
  return findPlugin(plugins, name)?.runs === 'server';
}

const ESCAPES: Record<string, string> = { r: '\r', n: '\n', t: '\t', '\\': '\\' };

/** decodeEscapes turns \r, \n, \t and \\ into the characters they name;
 * any other backslash is kept as typed. */
export function decodeEscapes(s: string): string {
  return s.replace(/\\([rnt\\])/g, (_, c: string) => ESCAPES[c]);
}

/** encodeEscapes is decodeEscapes' inverse: decodeEscapes(encodeEscapes(s))
 * is s for every s. */
export function encodeEscapes(s: string): string {
  return s.replace(/[\\\r\n\t]/g, (c) => ({ '\\': '\\\\', '\r': '\\r', '\n': '\\n', '\t': '\\t' })[c] ?? c);
}

/** lines turns a textarea's content into the string list the API takes:
 * trimmed lines, blank ones dropped, so a trailing newline is not an
 * entry. */
function lines(text: string): string[] {
  return text.split('\n').map((l) => l.trim()).filter(Boolean);
}

/** textOf is a string field's value as it would be sent: escapes decoded
 * where it takes them, trimmed where it has a format (a name or an
 * address), verbatim where it is a substring to look for. */
function textOf(plugin: string, spec: CheckPluginParam, value: string | boolean | undefined): string {
  if (typeof value !== 'string') return '';
  if (takesEscapes(plugin, spec.name)) return decodeEscapes(value);
  return spec.format ? value.trim() : value;
}

/** paramsFrom builds the params object a save sends: only the parameters
 * the form holds a value for, a blank text, number or list field counting
 * as not set. A required list is sent even empty; validateParams refuses
 * it first. */
export function paramsFrom(plugin: CheckPluginDescriptor, values: ParamValues): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const spec of plugin.params) {
    const value = values[spec.name];
    if (value === undefined) {
      if (spec.type === 'list' && spec.required) out[spec.name] = [];
      continue;
    }
    switch (spec.type) {
      case 'integer':
        if (typeof value === 'string' && value.trim() !== '') out[spec.name] = Number(value);
        break;
      case 'string': {
        const text = textOf(plugin.name, spec, value);
        if (text !== '') out[spec.name] = text;
        break;
      }
      case 'enum':
        if (typeof value === 'string') out[spec.name] = value;
        break;
      case 'bool':
        if (typeof value === 'boolean') out[spec.name] = value;
        break;
      case 'list': {
        const list = typeof value === 'string' ? lines(value) : [];
        if (list.length > 0 || spec.required) out[spec.name] = list;
        break;
      }
    }
  }
  return out;
}

/** paramValuesFrom reads a stored definition's params back into form
 * values, so an edit starts from what is saved and saves it back
 * unchanged. Keys the plugin does not know, and values of the wrong type,
 * are dropped: the server refuses both anyway. */
export function paramValuesFrom(plugin: CheckPluginDescriptor | undefined, params: Record<string, unknown> | undefined): ParamValues {
  const out: ParamValues = {};
  if (!plugin || !params) return out;
  for (const spec of plugin.params) {
    const value = params[spec.name];
    switch (spec.type) {
      case 'integer':
        if (typeof value === 'number') out[spec.name] = String(value);
        break;
      case 'string':
        if (typeof value === 'string') out[spec.name] = takesEscapes(plugin.name, spec.name) ? encodeEscapes(value) : value;
        break;
      case 'enum':
        if (typeof value === 'string') out[spec.name] = value;
        break;
      case 'bool':
        if (typeof value === 'boolean') out[spec.name] = value;
        break;
      case 'list':
        if (Array.isArray(value)) out[spec.name] = value.filter((v): v is string => typeof v === 'string').join('\n');
        break;
    }
  }
  return out;
}

const byteLen = (s: string) => new TextEncoder().encode(s).length;

/** formatMessage explains why a value fails a format; a format without a
 * message of its own (one added on the server first) is named. */
function formatMessage(t: Messages, format: string, value: string): string | undefined {
  const problem = formatProblem(format, value);
  if (problem === null) return undefined;
  const messages: Readonly<Record<string, string>> = t.checks.paramErrors.formats;
  return messages[problem] ?? problem;
}

/** problemOf is the message for one parameter's value, or undefined when
 * it may be sent — the bounds and formats of its spec, in the order the
 * server checks them. */
function problemOf(plugin: string, spec: CheckPluginParam, value: string | boolean | undefined, t: Messages): string | undefined {
  const msg = t.checks.paramErrors;
  switch (spec.type) {
    case 'integer': {
      const text = typeof value === 'string' ? value.trim() : '';
      if (text === '') return spec.required ? t.validation.required : undefined;
      const min = spec.min ?? 0;
      const max = spec.max ?? Number.MAX_SAFE_INTEGER;
      const n = Number(text);
      return /^\d+$/.test(text) && n >= min && n <= max ? undefined : msg.wholeRange(min, max);
    }
    case 'string': {
      const text = textOf(plugin, spec, value);
      if (text === '') return spec.required ? t.validation.required : undefined;
      if (spec.max_bytes && byteLen(text) > spec.max_bytes) return t.validation.maxChars(spec.max_bytes);
      return spec.format ? formatMessage(t, spec.format, text) : undefined;
    }
    case 'list': {
      const list = typeof value === 'string' ? lines(value) : [];
      if (list.length === 0) return spec.required ? t.validation.required : undefined;
      if (spec.max_entries && list.length > spec.max_entries) return msg.tooMany(spec.max_entries);
      for (const entry of list) {
        if (spec.max_bytes && byteLen(entry) > spec.max_bytes) return msg.tooLong(spec.max_bytes);
        const problem = spec.format ? formatMessage(t, spec.format, entry) : undefined;
        if (problem) return problem;
      }
      return undefined;
    }
    default:
      // A select only offers the spec's options, a switch only true or
      // false.
      return undefined;
  }
}

/** validateParams returns one message per refused field, keyed by the
 * parameter's name; an empty object means the values may be sent. */
export function validateParams(plugin: CheckPluginDescriptor, values: ParamValues, t: Messages): Record<string, string> {
  const errors: Record<string, string> = {};
  for (const spec of plugin.params) {
    const problem = problemOf(plugin.name, spec, values[spec.name], t);
    if (problem) errors[spec.name] = problem;
  }
  return errors;
}

/** paramText is a parameter field's label, hint, placeholder and option
 * labels (t.checks.params), undefined for a parameter the catalogue does
 * not know yet. */
export function paramText(t: Messages, plugin: string, param: string): ParamText | undefined {
  const all: Readonly<Record<string, Readonly<Record<string, ParamText>> | undefined>> = t.checks.params;
  return Object.hasOwn(all, plugin) ? all[plugin]?.[param] : undefined;
}

/** placeholderOf is what an empty field shows: the text the catalogue
 * gives it, else the default the server applies, else the descriptor's
 * example. */
export function placeholderOf(spec: CheckPluginParam, text: ParamText | undefined): string | undefined {
  if (text?.placeholder !== undefined) return text.placeholder;
  if (spec.default !== undefined) return String(spec.default);
  return spec.example;
}

/**
 * keyAfterPluginChange is the key a new definition's form shows after its
 * plugin changes. Alert rules match a server-run plugin's metric only on
 * the check whose key is the plugin name (the rules catalogue binds
 * http.up to the key "http"), so picking one fills that key in rather
 * than leaving it to the server's blank-key default, where nobody sees
 * it. A key the user typed is theirs and stays; a key this filled in
 * follows the plugin, and goes back to blank when the plugin no longer
 * runs on the server (the server then defaults it to the plugin name, as
 * before).
 */
export function keyAfterPluginChange(key: string, from: string, to: string, plugins: readonly CheckPluginDescriptor[]): string {
  const toServer = runsOnServer(plugins, to);
  const fromServer = runsOnServer(plugins, from);
  if (toServer && (key === '' || key === from)) return to;
  if (fromServer && !toServer && key === from) return '';
  return key;
}
