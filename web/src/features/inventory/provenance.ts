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
  if (own === undefined && field.default !== undefined) return { kind: 'default', value: field.default };
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

/** formatValue is a value as a provenance line quotes it: a string as is,
 * anything else as JSON, cut to one short line. */
export function formatValue(v: unknown): string {
  const text = typeof v === 'string' ? v : (JSON.stringify(v) ?? String(v));
  return text.length > MAX ? `${text.slice(0, MAX - 1)}…` : text;
}
