import type { FieldOrigin, SchemaField } from './types.ts';

/**
 * Provenance rules of a top-level field (spec 7.75 goals 2 and 5), the SPA's
 * side of internal/inventory/schema's origin.go: absent `active` is active,
 * absent `origin` is manual, and a source field turns adjusted once a person
 * changes what it says.
 */

export function isActive(f: SchemaField): boolean {
  return f.active !== false;
}

export function originOf(f: SchemaField): FieldOrigin {
  return f.origin ?? 'manual';
}

export function activeFields(fields: readonly SchemaField[]): SchemaField[] {
  return fields.filter(isActive);
}

/** content is what a field says, without where it came from or whether it
 * is shown: what "the source's version" of a field is made of. */
export function content(f: SchemaField): SchemaField {
  const rest = { ...f };
  delete rest.active;
  delete rest.origin;
  delete rest.source;
  delete rest.set_in_playbook;
  delete rest.source_field;
  return rest;
}

/** canonical is a JSON text that ignores attribute order and absent
 * (undefined) attributes, so two equal fields always compare equal. */
function canonical(v: unknown): string {
  return JSON.stringify(v, (_key, value: unknown) => {
    if (value === null || typeof value !== 'object' || Array.isArray(value)) return value;
    const obj = value as Record<string, unknown>;
    return Object.fromEntries(
      Object.keys(obj)
        .filter((k) => obj[k] !== undefined)
        .sort()
        .map((k) => [k, obj[k]]),
    );
  });
}

export function sameValue(a: unknown, b: unknown): boolean {
  return canonical(a) === canonical(b);
}

export function sameContent(a: SchemaField, b: SchemaField): boolean {
  return sameValue(content(a), content(b));
}

/** editLocked is `next` as a person's edit of `prev`: a source field whose
 * content changed becomes adjusted and records its source version (prev's
 * content). Switching it active or inactive is not an edit of its content.
 * An adjusted field edited back to its source version is the source's
 * again, so a reverted edit ends at source whether it was made inline (one
 * step at a time) or in the field dialog (against the field it opened on).
 * Every other origin stays what it was. */
export function editLocked(prev: SchemaField, next: SchemaField): SchemaField {
  const src = prev.source_field;
  if (originOf(prev) === 'adjusted' && src && sameContent(next, { ...content(src), name: next.name })) {
    return resetToSource({ ...next, source_field: src });
  }
  if (originOf(prev) !== 'source' || sameContent(prev, next)) return next;
  return { ...next, origin: 'adjusted', source_field: content(prev) };
}

/** resetToSource gives an adjusted field the source's version back: it is
 * locked to the source again, and keeps its activation and where it came
 * from. A field without a recorded source version is returned as is. */
export function resetToSource(f: SchemaField): SchemaField {
  if (!f.source_field) return f;
  const next: SchemaField = { ...content(f.source_field), name: f.name };
  if (f.active !== undefined) next.active = f.active;
  next.origin = 'source';
  if (f.source) next.source = f.source;
  if (f.set_in_playbook) next.set_in_playbook = true;
  return next;
}

/** keepAsManual turns a field (typically a gone one) into a manual field: no
 * origin, no source, no source version, nothing the playbook sets. Only its
 * activation stays. */
export function keepAsManual(f: SchemaField): SchemaField {
  const next = content(f);
  if (f.active !== undefined) next.active = f.active;
  return next;
}

/** sourceDifferences lists the attributes (besides the name) in which a
 * field says something else than its recorded source version, each with
 * what the source says, sorted by attribute; none without a source version. */
export function sourceDifferences(f: SchemaField): [string, unknown][] {
  if (!f.source_field) return [];
  const mine = new Map(Object.entries(content(f)));
  const theirs = new Map(Object.entries(content(f.source_field)));
  const keys = [...new Set([...mine.keys(), ...theirs.keys()])].filter((k) => k !== 'name').sort();
  return keys.filter((k) => !sameValue(mine.get(k), theirs.get(k))).map((k) => [k, theirs.get(k)]);
}

/**
 * What a field's source fixes below it, for the names of its sub-fields:
 * every one of them ('all': a field that is still the source's), only the
 * sub-fields the source declared (a list: an adjusted field, whose
 * sub-fields added by a person stay free), or none (null). Renaming a
 * sub-key of a role's argument spec breaks the playbook's contract.
 */
export type SubLock = 'all' | readonly SchemaField[] | null;

export function subLockOf(f: SchemaField): SubLock {
  const origin = originOf(f);
  if (origin === 'source') return 'all';
  if (origin === 'adjusted') return f.source_field ? (f.source_field.fields ?? []) : 'all';
  return null;
}

/** subNameLocked says whether the name of `sub` is the source's. */
export function subNameLocked(lock: SubLock, sub: SchemaField): boolean {
  return lock === 'all' || (lock !== null && lock.some((s) => s.name === sub.name));
}

/** subLockBelow is the lock for the sub-fields of `sub`, one level down. */
export function subLockBelow(lock: SubLock, sub: SchemaField): SubLock {
  if (lock === 'all') return 'all';
  const declared = lock?.find((s) => s.name === sub.name);
  return declared ? (declared.fields ?? []) : null;
}
