/** One place that overrides a template variable: a schedule or a webhook
 * trigger of the template (spec 7.75 goal 5, "Used in"). */
export interface UsageEntry {
  kind: 'schedule' | 'webhook';
  id: string;
  name: string;
}

/** The places that override each variable, by field name. */
export type FieldUsage = Record<string, readonly UsageEntry[]>;

/** The part of a schedule that usageByField reads. */
interface ScheduleLike {
  id: string;
  name: string;
  extra_vars?: Record<string, unknown> | null;
}

/** The part of a webhook trigger that usageByField reads. */
interface TriggerLike {
  id: string;
  name: string;
  mapping?: { extra_vars?: Record<string, string> | null } | null;
}

/**
 * usageByField says, for each variable name, which of a template's
 * schedules and webhook triggers override it: a schedule by the keys of its
 * extra vars, a webhook by the variables its payload mapping fills. Callers
 * pass the template's own schedules and its template_run triggers; the
 * schedules come first, each group in the order given.
 */
export function usageByField(schedules: readonly ScheduleLike[], triggers: readonly TriggerLike[]): FieldUsage {
  // No prototype: a variable may be called `constructor`.
  const usage: Record<string, UsageEntry[]> = Object.create(null);
  const add = (name: string, entry: UsageEntry) => {
    (usage[name] ??= []).push(entry);
  };
  for (const s of schedules) {
    for (const name of Object.keys(s.extra_vars ?? {})) add(name, { kind: 'schedule', id: s.id, name: s.name });
  }
  for (const t of triggers) {
    for (const name of Object.keys(t.mapping?.extra_vars ?? {})) add(name, { kind: 'webhook', id: t.id, name: t.name });
  }
  return usage;
}
