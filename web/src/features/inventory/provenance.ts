import { isRecord } from '../variables/secrets.ts';
import type { SchemaField, Vars } from '../variables/types.ts';

/** What one ancestor group renders as its vars, by name; `vars` undefined
 * when the data at hand does not say (the group is not in the preview). */
export interface AncestorVars {
  name: string;
  vars: Vars | undefined;
}

/** Where a group variable's effective value comes from. */
export type Provenance =
  /** The group sets it and so overrides the nearest ancestor that does. */
  | { kind: 'overrides'; group: string; value: unknown }
  /** The group leaves it unset; its hosts get the nearest ancestor's value. */
  | { kind: 'inherits'; group: string; value: unknown }
  /** The group leaves it unset; its schema's default is rendered for it. */
  | { kind: 'default'; value: unknown };

/**
 * ancestorVarsFrom reads each ancestor's vars from an inventory preview
 * document (`<group>.vars`, as the renderer writes them: the group's values
 * with its schema's defaults, secrets masked). A preview only holds the
 * groups above hosts it renders, so an ancestor missing from it is unknown,
 * not empty; one that is there without `vars` sets nothing.
 */
export function ancestorVarsFrom(inventory: Readonly<Record<string, unknown>> | undefined, ancestors: readonly { name: string }[]): AncestorVars[] {
  return ancestors.map(({ name }) => {
    const entry = inventory && Object.hasOwn(inventory, name) ? inventory[name] : undefined;
    if (!isRecord(entry)) return { name, vars: undefined };
    return { name, vars: isRecord(entry.vars) ? entry.vars : {} };
  });
}

/**
 * provenanceOf says where the effective value of a group's top-level field
 * comes from, the way Ansible merges group vars: a deeper group's vars
 * override its ancestors', and the renderer writes a schema default into
 * the group's own vars. So a value the group sets overrides the nearest
 * ancestor setting the same key; an unset field with a default renders that
 * default; an unset one without inherits from the nearest ancestor that
 * sets it. `ancestors` run from the root down to the parent. Whenever an
 * ancestor nearer than the answer is unknown the answer could be wrong, and
 * there is none (null) — as there is when nothing applies.
 */
export function provenanceOf(field: SchemaField, own: unknown, ancestors: readonly AncestorVars[]): Provenance | null {
  // A null default is none: schema.Resolve writes no nil default.
  if (own === undefined && field.default !== undefined && field.default !== null) return { kind: 'default', value: field.default };
  for (let i = ancestors.length - 1; i >= 0; i--) {
    const { name, vars } = ancestors[i];
    if (vars === undefined) return null;
    if (Object.hasOwn(vars, field.name) && vars[field.name] !== undefined && vars[field.name] !== null) {
      return { kind: own === undefined ? 'inherits' : 'overrides', group: name, value: vars[field.name] };
    }
  }
  return null;
}

const MAX = 48;

/** The key of the plain-text wrapper the renderer puts around every string
 * an assistant wrote (decision D6: Ansible's unsafe marker). */
const PLAIN = '__ansible_unsafe';

/** unwrapPlain returns v with every plain-text wrapper replaced by the
 * string it holds, and whether there was one. */
export function unwrapPlain(v: unknown): { value: unknown; plain: boolean } {
  if (isRecord(v)) {
    const keys = Object.keys(v);
    if (keys.length === 1 && keys[0] === PLAIN && typeof v[PLAIN] === 'string') return { value: v[PLAIN], plain: true };
    let plain = false;
    const out: Record<string, unknown> = {};
    for (const k of keys) {
      const u = unwrapPlain(v[k]);
      out[k] = u.value;
      plain ||= u.plain;
    }
    return { value: out, plain };
  }
  if (Array.isArray(v)) {
    let plain = false;
    const out = v.map((e) => {
      const u = unwrapPlain(e);
      plain ||= u.plain;
      return u.value;
    });
    return { value: out, plain };
  }
  return { value: v, plain: false };
}

/** formatValue is a value as a provenance line quotes it: a string as is,
 * anything else as JSON, cut to one short line. A value an assistant wrote
 * reads unwrapped, followed by `plainLabel` in parentheses when given. */
export function formatValue(v: unknown, plainLabel?: string): string {
  const { value, plain } = unwrapPlain(v);
  const text = typeof value === 'string' ? value : (JSON.stringify(value) ?? String(value));
  const cut = text.length > MAX ? `${text.slice(0, MAX - 1)}…` : text;
  return plain && plainLabel ? `${cut} (${plainLabel})` : cut;
}
