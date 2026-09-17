import { childPath, indexPath } from '../inventory/schema/paths.ts';
import { declaredOnly, isKeepMarker, isRecord, isSecretSet, type KeepMarker } from '../inventory/schema/secrets.ts';
import type { SchemaField, Vars } from '../inventory/schema/types.ts';

/**
 * The masked-secret roundtrip contract: a GET returns {"$secret": "set"}
 * for a stored secret; the manager seals secret extra vars at rest and
 * resolves keep markers ({"$secret": "keep", "from": <path>}) and the
 * set-marker to the stored value on a PUT. The dialog therefore sends an
 * unchanged secret as a keep marker — omitting it would drop the stored
 * secret, because a PUT replaces all extra vars — and a replaced secret as
 * new plaintext. A stored secret under a field the template's schema no
 * longer declares is stripped.
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
 * unchanged declared secret sent as a keep marker naming its own path (a
 * raw set-marker included), everything else passed through; without a
 * schema, set-markers are stripped (no field is secret). */
export function varsForPut(fields: readonly SchemaField[] | undefined, vars: Vars): Vars {
  if (!fields || fields.length === 0) return stripSetMarkers(vars);
  return keepUnchangedSecrets(declaredOnly(fields, vars), fields);
}

function keepUnchangedSecrets(vars: Vars, fields: readonly SchemaField[], prefix = ''): Vars {
  const out: Vars = {};
  for (const [k, v] of Object.entries(vars)) {
    const f = fields.find((x) => x.name === k);
    const path = childPath(prefix, k);
    if (isKeepMarker(v)) {
      out[k] = v; // names the stored value's original path (entries may have moved)
      continue;
    }
    if (isSecretSet(v)) {
      out[k] = { $secret: 'keep', from: path } as KeepMarker;
      continue;
    }
    if (f?.type === 'object' && isRecord(v)) {
      out[k] = keepUnchangedSecrets(v, f.fields ?? [], path);
      continue;
    }
    if (f?.type === 'repeater' && Array.isArray(v)) {
      out[k] = v.map((item, i) => (isRecord(item) ? keepUnchangedSecrets(item, f.fields ?? [], indexPath(path, i)) : item));
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
