import { childPath, indexPath } from '../variables/paths.ts';
import { declaredOnly, isKeepMarker, isRecord, isSecretSet, keepMarker, toKeepMarkers, type KeepMarker } from '../variables/secrets.ts';
import { activeFields } from '../variables/origin.ts';
import { toVarSchema } from '../../api/hooks/useInventory.ts';
import type { SchemaField, Vars } from '../variables/types.ts';
import type { TemplateInput, TemplateUpdateInput } from '../../api/types.ts';

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

/** toDraftVars prepares loaded values for the variables page: EVERY stored
 * value stays in the draft — those of inactive and removed fields too, so
 * switching a field active or adding a removed one back under its name
 * shows its value again (spec 7.75 owner decision 4) — with each masked
 * secret as a keep marker naming its path (a removed field's at the top
 * level). varsForPut then sends the active declared fields only. */
export function toDraftVars(fields: readonly SchemaField[], vars: Vars): Vars {
  const out = toKeepMarkers(fields, vars);
  const declared = new Set(fields.map((f) => f.name));
  for (const [k, v] of Object.entries(out)) {
    if (!declared.has(k) && isSecretSet(v)) out[k] = keepMarker(k);
  }
  return out;
}

/** varsForPut is the PUT's extra_vars: the values of the ACTIVE fields,
 * with every unchanged declared secret sent as a keep marker naming its own
 * path (a raw set-marker included). The server keeps the stored value of an
 * inactive or removed field the body omits (plan 7.75 decision 10), so
 * those are left out. Without a schema (undefined) every value passes,
 * set-markers stripped (no field is secret). */
export function varsForPut(fields: readonly SchemaField[] | undefined, vars: Vars): Vars {
  if (!fields) return stripSetMarkers(vars);
  const active = activeFields(fields);
  return keepUnchangedSecrets(declaredOnly(active, vars), active);
}

/** echoedVars is what a template save that does not edit the extra vars
 * (the template dialog) sends back for them: the values as loaded, each
 * stored secret as a keep marker and only the active fields' (the server
 * keeps the rest), or every value of a template without a schema. */
export function echoedVars(schema: unknown, vars: Vars): Vars {
  if (!schema) return varsForPut(undefined, vars);
  const fields = toVarSchema(schema).fields;
  return varsForPut(fields, toEditableVars(fields, vars));
}

function keepUnchangedSecrets(vars: Vars, fields: readonly SchemaField[], prefix = ''): Vars {
  const out: Vars = {};
  for (const [k, v] of Object.entries(vars)) {
    const f = fields.find((x) => x.name === k);
    const path = childPath(prefix, k);
    if (isKeepMarker(v)) {
      // Names the stored value's original path (entries may have moved).
      // The server resolves a marker under a secret field only: a stored
      // secret under a field of another type (a removed secret added back,
      // a field retyped) cannot be kept (the page says so at the field),
      // so neither marker is sent for it.
      if (f?.type === 'secret') out[k] = v;
      continue;
    }
    if (isSecretSet(v)) {
      if (f?.type === 'secret') out[k] = { $secret: 'keep', from: path } as KeepMarker;
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

/** TemplateBodyIdentityKey is the one TemplateBodyKey with no form
 * counterpart: `$schema` is the generated JSON-Schema self-pointer huma
 * stamps on every *InputBody, never a value the client sends — the only
 * key TEMPLATE_BODY_FIELDS below may leave out. */
type TemplateBodyIdentityKey = '$schema';

/** TemplateBodyKey is the union of TemplateCreateInputBody's and
 * TemplateUpdateInputBody's own fields. TEMPLATE_BODY_FIELDS is keyed off
 * the union, not off TemplateInput (the Create body) alone: the two
 * schemas are generated independently and are free to diverge, and a guard
 * anchored on Create alone would compile cleanly — no missing-property
 * error, since an optional field missing from an object literal is not one
 * — the day a field is added only to TemplateUpdateInputBody, leaving
 * submit()'s PUT free to silently omit it. */
type TemplateBodyKey = keyof TemplateInput | keyof TemplateUpdateInput;

/**
 * TEMPLATE_BODY_FIELDS is TemplateForm submit()'s own compile-time guard
 * against routeToInput's class of bug (alertRoute.ts's own
 * ROUTE_INPUT_FIELDS — this copies the technique, not the contents):
 * submit() builds its save body by hand from component state, so a field
 * added to TemplateCreateInputBody/TemplateUpdateInputBody that nobody
 * teaches submit() to send would silently never reach the server on the
 * next save — the exact shape that, for vault_identities, also means
 * saving an edited template wipes whatever labelled vault passwords it
 * already added on top of its repository's own set (PUT is a full
 * replace).
 *
 * `Record<K, true>` forces exhaustiveness in both directions, over BOTH
 * bodies' fields (K is TemplateBodyKey, not just keyof TemplateInput): a
 * field either body gains and this literal does not list is a
 * missing-property error, and a stale key neither body has is an
 * excess-property error — so the day a new field joins either the create
 * or the update body, this line refuses to compile until TemplateForm's
 * own submit() is updated right next to it.
 */
export const TEMPLATE_BODY_FIELDS: Record<Exclude<TemplateBodyKey, TemplateBodyIdentityKey>, true> = {
  name: true,
  playbook_id: true,
  scope: true,
  limit: true,
  extra_vars: true,
  extra_vars_schema: true,
  credential_id: true,
  vault_identities: true,
};
