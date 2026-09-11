import { childPath, indexPath } from './paths.ts';
import type { SchemaField, Vars } from './types.ts';

/** A write that keeps the stored secret found at `from` when the form was
 * loaded (internal/inventory/schema.KeepFrom). */
export interface KeepMarker {
  $secret: 'keep';
  from: string;
}

export function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

/** isSecretSet recognises the mask a read returns for a stored secret. */
export function isSecretSet(v: unknown): boolean {
  return isRecord(v) && v.$secret === 'set';
}

export function isKeepMarker(v: unknown): v is KeepMarker {
  return isRecord(v) && v.$secret === 'keep' && typeof v.from === 'string';
}

export function keepMarker(from: string): KeepMarker {
  return { $secret: 'keep', from };
}

/** toKeepMarkers prepares loaded values for editing: every masked secret
 * of a declared secret field becomes a keep marker naming its original
 * path, so saving keeps it and a moved repeater entry keeps its own. */
export function toKeepMarkers(fields: readonly SchemaField[], vars: Vars, prefix = ''): Vars {
  const out: Vars = { ...vars };
  for (const f of fields) {
    const v = vars[f.name];
    const path = childPath(prefix, f.name);
    if (f.type === 'secret' && isSecretSet(v)) {
      out[f.name] = keepMarker(path);
    } else if (f.type === 'object' && isRecord(v)) {
      out[f.name] = toKeepMarkers(f.fields ?? [], v, path);
    } else if (f.type === 'repeater' && Array.isArray(v)) {
      out[f.name] = v.map((item, i) => (isRecord(item) ? toKeepMarkers(f.fields ?? [], item, indexPath(path, i)) : item));
    }
  }
  return out;
}

/** declaredOnly keeps only the top-level keys fields declare, so saving a
 * form drops values of removed fields (spec §6: never silently, but on the
 * next save). */
export function declaredOnly(fields: readonly SchemaField[], vars: Vars): Vars {
  const names = new Set(fields.map((f) => f.name));
  return Object.fromEntries(Object.entries(vars).filter(([k]) => names.has(k)));
}
