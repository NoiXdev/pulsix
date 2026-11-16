import { assertionOps, isNumericSubject, parseAssertion, type AssertionSubject } from './assertionFormat.ts';

/**
 * The row model of the http check's assertion editor (backlog 3.13b): one
 * row per `assertions` entry, broken into the parts the form edits, and
 * the way back to the canonical text the server stores. Parsing is the
 * grammar twin's (assertionFormat.ts); this file only moves between its
 * shape and the form's fields, so the editor and its text toggle can never
 * disagree about an entry.
 *
 * The canonical text is the server's (internal/probe/assert Entry): single
 * spaces, numbers printed the shortest way, strings and arguments as JSON
 * string literals, " @warn" for a warn entry and nothing for crit.
 */

/** One row of the editor. Texts are decoded: the argument and a text
 * operand are what the operator typed, not JSON literals. */
export interface AssertionRow {
  name: string;
  subject: AssertionSubject;
  /** The header name, JSONPath or CSS selector; '' for a subject without
   * an argument. */
  arg: string;
  op: string;
  /** The number, the lower bound of `in`, or the text; '' for exists and
   * !exists. */
  operand: string;
  /** The upper bound of `in`; '' otherwise. */
  hi: string;
  severity: 'warn' | 'crit';
}

/** Every subject in the order the editor offers them. */
export const ASSERTION_SUBJECTS: readonly AssertionSubject[] = [
  'status',
  'response_ms',
  'ttfb_ms',
  'size',
  'redirects',
  'cert_days',
  'cert_issuer',
  'body',
  'header',
  'json',
  'html',
  'html count',
];

/** takesArg: the subjects written with a quoted argument. */
export function takesArg(subject: AssertionSubject): boolean {
  return subject === 'header' || subject === 'json' || subject === 'html' || subject === 'html count';
}

/** takesOperand: every operator but exists and !exists. */
export function takesOperand(op: string): boolean {
  return op !== 'exists' && op !== '!exists';
}

/** emptyRow is the row "Add assertion" appends. */
export function emptyRow(): AssertionRow {
  return { name: '', subject: 'status', arg: '', op: '==', operand: '', hi: '', severity: 'crit' };
}

/** isBlank: a row the operator added and never filled in; it is not
 * sent. */
export function isBlank(row: AssertionRow): boolean {
  return row.name.trim() === '' && row.arg === '' && row.operand.trim() === '' && row.hi.trim() === '';
}

/** withSubject changes a row's subject, keeping the operator when the new
 * subject takes it (else its first) and the argument when both take one. */
export function withSubject(row: AssertionRow, subject: AssertionSubject): AssertionRow {
  const ops = assertionOps(subject);
  const op = ops.includes(row.op) ? row.op : ops[0];
  return {
    ...row,
    subject,
    op,
    arg: takesArg(subject) && takesArg(row.subject) ? row.arg : '',
    operand: takesOperand(op) ? row.operand : '',
    hi: op === 'in' ? row.hi : '',
  };
}

/** withOp changes a row's operator, dropping an operand it no longer
 * takes. */
export function withOp(row: AssertionRow, op: string): AssertionRow {
  return { ...row, op, operand: takesOperand(op) ? row.operand : '', hi: op === 'in' ? row.hi : '' };
}

/** rowFromEntry reads one entry into a row, or returns null when the
 * grammar refuses it. */
export function rowFromEntry(entry: string): AssertionRow | null {
  const shape = parseAssertion(entry);
  if (!shape) return null;
  let operand = shape.operand;
  let hi = '';
  if (shape.op === 'in') {
    const dots = operand.indexOf('..');
    hi = operand.slice(dots + 2);
    operand = operand.slice(0, dots);
  } else if (operand !== '' && !isNumericSubject(shape.subject)) {
    operand = JSON.parse(operand) as string;
  }
  return { name: shape.name, subject: shape.subject, arg: shape.arg, op: shape.op, operand, hi, severity: shape.severity };
}

/** canonicalNumber prints a number the way the server does (Go's
 * FormatFloat 'f', -1): "+5" is 5, "200.50" is 200.5, -0 is 0. A number
 * JavaScript would print with an exponent, or text that is no number,
 * stays as typed; the server judges it. */
export function canonicalNumber(s: string): string {
  const text = s.trim();
  if (!/^[+-]?[0-9]+(\.[0-9]+)?$/.test(text)) return text;
  const printed = String(Number(text));
  return printed.includes('e') ? text : printed;
}

/** subjectText: the subject as an expression writes it. */
function subjectText(row: AssertionRow): string {
  const arg = JSON.stringify(row.arg);
  if (row.subject === 'html count') return `html(${arg}) count`;
  if (takesArg(row.subject)) return `${row.subject}(${arg})`;
  return row.subject;
}

/** composeExpression: the row's canonical expression, without its name
 * and severity. */
export function composeExpression(row: AssertionRow): string {
  const parts = [subjectText(row), row.op];
  if (row.op === 'in') parts.push(`${canonicalNumber(row.operand)}..${canonicalNumber(row.hi)}`);
  else if (!takesOperand(row.op)) {
    // exists, !exists: nothing follows.
  } else if (isNumericSubject(row.subject)) parts.push(canonicalNumber(row.operand));
  else parts.push(JSON.stringify(row.operand));
  return parts.join(' ').trimEnd();
}

/** composeRow: the row's canonical entry. */
export function composeRow(row: AssertionRow): string {
  return `${row.name.trim()}=${composeExpression(row)}${row.severity === 'warn' ? ' @warn' : ''}`;
}

/** composeRows: the list's text, one entry per line, blank rows left
 * out. */
export function composeRows(rows: readonly AssertionRow[]): string {
  return rows.filter((r) => !isBlank(r)).map(composeRow).join('\n');
}

/** entriesOf: the non-blank lines of a list's text, trimmed. */
export function entriesOf(text: string): string[] {
  return text.split('\n').map((l) => l.trim()).filter(Boolean);
}

/** parseRows reads a list's text into rows; `bad` is the 1-based number
 * of the first entry the grammar refuses, when there is one. */
export function parseRows(text: string): { rows: AssertionRow[] } | { bad: number } {
  const rows: AssertionRow[] = [];
  const entries = entriesOf(text);
  for (let i = 0; i < entries.length; i++) {
    const row = rowFromEntry(entries[i]);
    if (!row) return { bad: i + 1 };
    rows.push(row);
  }
  return { rows };
}

/**
 * entryNamedBy is the index of the entry an API error is about, or -1.
 * The server names an entry two ways (internal/checkplugins): a refused
 * format quotes the whole entry ("…: \"a=body matches \\\"(\\\"\": the
 * regular expression does not compile"), the http hook quotes its name
 * ("assertion \"a\" reads the body …", "assertions names \"a\" twice").
 * Go's %q and JSON.stringify agree on every printable entry, so a quoted
 * entry is looked for first, then a quoted name — only in a message
 * about assertions, so a json_values refusal never lands on a row.
 */
export function entryNamedBy(entries: readonly string[], message: string | null | undefined): number {
  if (!message || !message.includes('assertion')) return -1;
  const whole = entries.findIndex((e) => message.includes(JSON.stringify(e)));
  if (whole >= 0) return whole;
  return entries.findIndex((e) => {
    const eq = e.indexOf('=');
    return eq > 0 && message.includes(JSON.stringify(e.slice(0, eq)));
  });
}
