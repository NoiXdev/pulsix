import { scopeOf } from '../../variables/paths.ts';
import type { FieldScope, FieldType, SchemaField } from '../../variables/types.ts';

/* The edits the Manage fields dialog makes to one field, pure: each returns
 * a new field and leaves no key behind that holds `undefined`, so a draft
 * compares and serialises like the schema it came from. */

function without(f: SchemaField, ...keys: (keyof SchemaField)[]): SchemaField {
  const next = { ...f };
  for (const k of keys) delete next[k];
  return next;
}

/** withScope sets where a field's value is set. A host-scoped field is asked
 * per host, so "hosts may override" no longer applies to it. */
export function withScope(f: SchemaField, scope: FieldScope): SchemaField {
  return scope === 'host' ? { ...without(f, 'overridable'), scope: 'host' } : without(f, 'scope');
}

export function toggledScope(f: SchemaField): SchemaField {
  return withScope(f, scopeOf(f) === 'host' ? 'group' : 'host');
}

export function withRequired(f: SchemaField, on: boolean): SchemaField {
  return on ? { ...f, required: true } : without(f, 'required');
}

/** withActive switches a field on or off; absent `active` means active. */
export function withActive(f: SchemaField, on: boolean): SchemaField {
  return on ? without(f, 'active') : { ...f, active: false };
}

export function withDefault(f: SchemaField, value: unknown): SchemaField {
  return value === undefined ? without(f, 'default') : { ...f, default: value };
}

export function withText(f: SchemaField, key: 'label' | 'description' | 'pattern', value: string): SchemaField {
  return value === '' ? without(f, key) : { ...f, [key]: value };
}

/** withOption adds an enum option; a blank one or one already there
 * changes nothing. */
export function withOption(f: SchemaField, option: string): SchemaField {
  const o = option.trim();
  const options = f.options ?? [];
  if (o === '' || options.includes(o)) return f;
  return { ...f, options: [...options, o] };
}

/** withoutOption removes an enum option, and the default with it when it
 * was that option. */
export function withoutOption(f: SchemaField, option: string): SchemaField {
  const next = { ...f, options: (f.options ?? []).filter((o) => o !== option) };
  return f.default === option ? without(next, 'default') : next;
}

/** toggledDefault makes an option the default, or clears the default when
 * it already was. */
export function toggledDefault(f: SchemaField, option: string): SchemaField {
  return withDefault(f, f.default === option ? undefined : option);
}

/** hasDefault: the server refuses a default for a secret, a repeater and
 * an object (internal/inventory/schema checkDefault). */
export function hasDefault(type: FieldType): boolean {
  return type !== 'secret' && type !== 'repeater' && type !== 'object';
}

/** parseList reads a list default typed as comma separated text, each item
 * as the list's item type. An item that is not one is kept as typed, so the
 * server names the problem instead of the value vanishing. */
export function parseList(text: string, item: FieldType): unknown[] | undefined {
  const parts = text
    .split(',')
    .map((p) => p.trim())
    .filter((p) => p !== '');
  if (parts.length === 0) return undefined;
  return parts.map((p) => {
    if (item === 'int' || item === 'float') {
      const n = Number(p);
      return Number.isFinite(n) ? n : p;
    }
    if (item === 'bool') return p === 'true' ? true : p === 'false' ? false : p;
    return p;
  });
}

export function formatList(value: unknown): string {
  return Array.isArray(value) ? value.map((v) => (typeof v === 'string' ? v : JSON.stringify(v))).join(', ') : '';
}
