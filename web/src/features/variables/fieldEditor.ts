import type { Messages } from '../../i18n/types.ts';
import type { EditorVariant, FieldType, Problem, SchemaField } from './types.ts';

export const FIELD_TYPES: readonly FieldType[] = ['string', 'int', 'float', 'bool', 'enum', 'secret', 'list', 'repeater', 'object'];
export const SCALAR_TYPES: readonly FieldType[] = ['string', 'int', 'float', 'bool', 'enum'];

/** typesFor is the field types a schema may use. A shared job-template
 * definition withholds "secret": the server refuses a secret-typed field
 * there by construction (a shared definition's extra vars are written into
 * every organization's own column unsealed), and offering a type that is
 * always refused is worse than not offering it (backlog 2.101). */
export function typesFor(variant: EditorVariant): readonly FieldType[] {
  return variant === 'shared' ? FIELD_TYPES.filter((t) => t !== 'secret') : FIELD_TYPES;
}

/** formatValue shows a schema attribute's value in one line: a default in
 * the field list, or what the source says in the field dialog. */
export function formatValue(v: unknown): string {
  if (v === undefined || v === null || v === '') return '—';
  if (typeof v === 'string') return v;
  if (Array.isArray(v) && v.every((x) => typeof x === 'string')) return v.join(', ');
  return JSON.stringify(v);
}

/** formatAttribute is formatValue for one attribute of a field, with the
 * structured ones (sub-fields, a list's item) shown by what matters: the
 * sub-fields' names, the item's type (with its options for an enum). */
export function formatAttribute(key: string, v: unknown): string {
  if (key === 'fields' && Array.isArray(v)) return formatValue((v as SchemaField[]).map((f) => f.name));
  if (key === 'item' && v && typeof v === 'object') {
    const item = v as SchemaField;
    return item.type === 'enum' && item.options?.length ? `enum (${item.options.join(', ')})` : formatValue(item.type);
  }
  return formatValue(v);
}

export function typeLabel(f: SchemaField, t: Messages): string {
  if (f.type === 'list') return `list<${f.item?.type ?? 'string'}>`;
  if (f.type === 'object') return t.variables.builder.subSchema;
  return f.type;
}

export function newField(existing: readonly SchemaField[]): SchemaField {
  const names = new Set(existing.map((f) => f.name));
  let n = existing.length + 1;
  while (names.has(`field_${n}`)) n++;
  return { name: `field_${n}`, type: 'string' };
}

const COLLECTIONS = new Set<FieldType>(['list', 'repeater']);
const WITH_SUBFIELDS = new Set<FieldType>(['repeater', 'object']);

/** changeType switches a field's type, keeping what still means the same
 * thing (name, label, description, scope, required, overridable, item
 * counts between collections, sub-fields between repeater and object, and
 * its provenance) and giving the new type the structure the server
 * requires. */
export function changeType(f: SchemaField, type: FieldType): SchemaField {
  const next: SchemaField = { name: f.name, type };
  if (f.label) next.label = f.label;
  if (f.description) next.description = f.description;
  if (f.scope) next.scope = f.scope;
  if (f.required) next.required = true;
  if (f.overridable) next.overridable = true;
  if (type === 'enum') next.options = f.options?.length ? f.options : ['option_1'];
  if (type === 'list') next.item = f.item ?? { name: '', type: 'string' };
  if (WITH_SUBFIELDS.has(type)) next.fields = f.fields?.length ? f.fields : [{ name: 'value', type: 'string' }];
  if (COLLECTIONS.has(type) && COLLECTIONS.has(f.type)) {
    if (f.min_items !== undefined) next.min_items = f.min_items;
    if (f.max_items !== undefined) next.max_items = f.max_items;
  }
  // Provenance is about the field, not its type: it stays as it is (a type
  // change of a source field makes it adjusted when it is saved).
  if (f.active !== undefined) next.active = f.active;
  if (f.origin) next.origin = f.origin;
  if (f.source) next.source = f.source;
  if (f.set_in_playbook) next.set_in_playbook = true;
  if (f.source_field) next.source_field = f.source_field;
  return next;
}

export function moveField(fields: readonly SchemaField[], from: number, to: number): SchemaField[] {
  const next = [...fields];
  next.splice(to, 0, next.splice(from, 1)[0]);
  return next;
}

export function replaceField(fields: readonly SchemaField[], index: number, field: SchemaField): SchemaField[] {
  return fields.map((f, i) => (i === index ? field : f));
}

export function removeField(fields: readonly SchemaField[], index: number): SchemaField[] {
  return fields.filter((_, i) => i !== index);
}

/** fieldPath is the server's path of field index inside the list at prefix. */
export function fieldPath(prefix: string, index: number): string {
  return `${prefix ? `${prefix}.` : ''}fields[${index}]`;
}

export function hasProblemUnder(problems: readonly Problem[] | undefined, path: string): boolean {
  return (problems ?? []).some((p) => p.path === path || p.path.startsWith(`${path}.`) || p.path.startsWith(`${path}[`));
}
