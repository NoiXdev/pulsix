import { isActive } from '../../variables/origin.ts';
import type { SchemaField, Vars } from '../../variables/types.ts';

/** keptNames lists the stored values a run does not receive: those under
 * an inactive field or a name the schema no longer declares (the server
 * keeps them until a person discards them, plan 7.75 decision 10), in the
 * order they were stored. */
export function keptNames(fields: readonly SchemaField[], vars: Vars): string[] {
  const byName = new Map(fields.map((f) => [f.name, f]));
  return Object.keys(vars).filter((name) => {
    const f = byName.get(name);
    return !f || !isActive(f);
  });
}
