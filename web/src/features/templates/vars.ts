import { childPath, indexPath } from '../inventory/schema/paths.ts';
import { declaredOnly, isKeepMarker, isRecord, isSecretSet, type KeepMarker } from '../inventory/schema/secrets.ts';
import type { SchemaField, Vars } from '../inventory/schema/types.ts';

/**
 * The masked-secret roundtrip contract (Task 7): a GET returns
 * {"$secret": "set"} for a stored secret; templates have no keep-marker
 * resolve path, so a PUT that echoed the marker back would be refused
 * ("must be a string") — and the manager has no way to know which stored
 * value it means anyway. The dialog therefore treats a set-marker as
 * "unchanged" and the save OMITS the key (both branches unit-tested); a
 * replaced secret is sent as new plaintext, which works for required and
 * optional fields alike. A stored secret under a field the template's
 * schema no longer declares is stripped the same way.
 */

/** SecretInputState is what the dialog shows for a secret-typed field's
 * current value: the server's set-marker, or a replacement the user typed. */
export type SecretFieldState = { marker: KeepMarker | null } | { plaintext: string | null };

export function secretFields(schema: readonly SchemaField[] | undefined): string[] {
  return (schema ?? []).filter((f) => f.type === 'secret').map((f) => f.name);
}

/** toEditableVars prepares loaded extra vars for the dialog: declared
 * secret fields showing the set-marker become keep markers (SchemaForm
 * renders those display-only with a Replace action), and only declared
 * keys survive (the server validates strictly at every depth). */
export function toEditableVars(fields: readonly SchemaField[], vars: Vars): Vars {
  return toKeepMarkers(fields, declaredOnly(fields, vars));
}

function toKeepMarkers(fields: readonly SchemaField[], vars: Vars, prefix = ''): Vars {
  const out: Vars = { ...vars };
  for (const f of fields) {
    const v = vars[f.name];
    const path = childPath(prefix, f.name);
    if (f.type === 'secret' && isSecretSet(v)) {
      out[f.name] = { $secret: 'keep', from: path } as KeepMarker;
    } else if (f.type === 'object' && isRecord(v)) {
      out[f.name] = toKeepMarkers(f.fields ?? [], v, path);
    } else if (f.type === 'repeater' && Array.isArray(v)) {
      out[f.name] = v.map((item, i) => (isRecord(item) ? toKeepMarkers(f.fields ?? [], item, indexPath(path, i)) : item));
    }
  }
  return out;
}

/** varsForPut is the PUT's extra_vars: the form's values with every
 * unchanged secret (a keep marker, or — without a schema — the raw
 * set-marker at any depth) omitted, everything else passed through. */
export function varsForPut(fields: readonly SchemaField[] | undefined, vars: Vars): Vars {
  if (!fields || fields.length === 0) return stripSetMarkers(vars);
  return stripKeepMarkers(declaredOnly(fields, vars), fields);
}

function stripKeepMarkers(vars: Vars, fields: readonly SchemaField[]): Vars {
  const out: Vars = {};
  for (const [k, v] of Object.entries(vars)) {
    const f = fields.find((x) => x.name === k);
    if (isKeepMarker(v) || isSecretSet(v)) continue; // unchanged: omitted
    if (f?.type === 'object' && isRecord(v)) {
      out[k] = stripKeepMarkers(v, f.fields ?? []);
      continue;
    }
    if (f?.type === 'repeater' && Array.isArray(v)) {
      out[k] = v.map((item) => (isRecord(item) ? stripKeepMarkers(item, f.fields ?? []) : item));
      continue;
    }
    out[k] = v;
  }
  return out;
}

function stripSetMarkers(vars: Vars): Vars {
  const out: Vars = {};
  for (const [k, v] of Object.entries(vars)) {
    if (isSecretSet(v)) continue;
    if (isRecord(v)) {
      out[k] = stripSetMarkers(v);
      continue;
    }
    if (Array.isArray(v)) {
      out[k] = v.map((item) => (isRecord(item) ? stripSetMarkers(item) : item));
      continue;
    }
    out[k] = v;
  }
  return out;
}
