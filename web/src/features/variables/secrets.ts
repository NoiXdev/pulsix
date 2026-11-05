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

/** declaredOnly keeps only the keys fields declare, at every depth, so
 * saving a form drops values of fields a schema edit removed (spec §6:
 * never silently, but on the next save) — matching the server, which
 * validates strictly at every depth (validateObject's "unknown field"). A
 * removed sub-field of an object or of a repeater entry is pruned the same
 * way the top-level orphan is; a non-object repeater entry or a list's
 * items are left as they are (the server reports those on its own). Never
 * mutates its input. */
export function declaredOnly(fields: readonly SchemaField[], vars: Vars): Vars {
  const byName = new Map(fields.map((f) => [f.name, f]));
  const out: Vars = {};
  for (const [k, v] of Object.entries(vars)) {
    const f = byName.get(k);
    if (!f) continue;
    if (f.type === 'object' && isRecord(v)) {
      out[k] = declaredOnly(f.fields ?? [], v);
    } else if (f.type === 'repeater' && Array.isArray(v)) {
      out[k] = v.map((item) => (isRecord(item) ? declaredOnly(f.fields ?? [], item) : item));
    } else {
      out[k] = v;
    }
  }
  return out;
}
