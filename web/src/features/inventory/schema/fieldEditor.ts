import type { FieldType, Problem, SchemaField } from './types.ts';

export const FIELD_TYPES: readonly FieldType[] = ['string', 'int', 'float', 'bool', 'enum', 'secret', 'list', 'repeater', 'object'];
export const SCALAR_TYPES: readonly FieldType[] = ['string', 'int', 'float', 'bool', 'enum'];

export function typeLabel(f: SchemaField): string {
  if (f.type === 'list') return `list<${f.item?.type ?? 'string'}>`;
  if (f.type === 'object') return 'sub-schema';
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
 * counts between collections, sub-fields between repeater and object) and
 * giving the new type the structure the server requires. */
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
