/** One place that overrides a template variable: a schedule or a webhook
 * trigger of the template (spec 7.75 goal 5, "Used in"). */
export interface UsageEntry {
  kind: 'schedule' | 'webhook';
  id: string;
  name: string;
}

/** The places that override each variable, by field name. */
export type FieldUsage = Record<string, readonly UsageEntry[]>;
