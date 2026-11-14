import type { Messages } from '../../i18n/types.ts';

/** The rules of internal/orgsecrets, repeated so a mistake is named at its
 * field before the round trip; the server stays the authority. */
const NAME_RE = /^[A-Za-z0-9_.-]{1,64}$/;
export const MAX_FIELDS = 16;
const MAX_VALUE_BYTES = 4096;

/** One row of the dialog: a field's name and its value as typed. */
export interface FieldRow {
  name: string;
  value: string;
}

export interface RowErrors {
  name?: string;
  value?: string;
}

export interface SecretErrors {
  name?: string;
  /** One entry per row, empty where the row is fine. */
  rows: RowErrors[];
  /** About the rows as a whole (their count). */
  form?: string;
}

const encoder = new TextEncoder();

function valueError(t: Messages, value: string): string | undefined {
  if (value === '') return t.validation.required;
  if (encoder.encode(value).length > MAX_VALUE_BYTES) return t.secrets.errors.valueTooLong;
  if (value.includes('\u0000')) return t.secrets.errors.valueNul;
  return undefined;
}

/**
 * validateSecret returns what is wrong with a secret as the dialog holds
 * it, or null when it may be sent. `taken` lists the names already in use
 * while creating (a create with one of them would silently replace it);
 * replacing passes null.
 */
export function validateSecret(t: Messages, name: string, rows: readonly FieldRow[], taken: readonly string[] | null): SecretErrors | null {
  const e = t.secrets.errors;
  const errors: SecretErrors = { rows: [] };
  let bad = false;
  if (!NAME_RE.test(name)) errors.name = e.name;
  else if (taken?.includes(name)) errors.name = e.nameTaken;
  if (errors.name) bad = true;
  if (rows.length < 1 || rows.length > MAX_FIELDS) {
    errors.form = e.fieldCount;
    bad = true;
  }
  const seen = new Set<string>();
  errors.rows = rows.map((row) => {
    const r: RowErrors = {};
    if (!NAME_RE.test(row.name)) r.name = e.fieldName;
    else if (seen.has(row.name)) r.name = e.fieldRepeated;
    seen.add(row.name);
    const v = valueError(t, row.value);
    if (v) r.value = v;
    if (r.name || r.value) bad = true;
    return r;
  });
  return bad ? errors : null;
}

/** fieldsOf is the PUT body's fields object. Values go as typed: a
 * community or password may well begin or end with a space. */
export function fieldsOf(rows: readonly FieldRow[]): Record<string, string> {
  return Object.fromEntries(rows.map((r) => [r.name, r.value]));
}
