export const OBJECT_TYPES = [
  'host', 'member', 'token', 'zone', 'organization', 'setup', 'totp', 'passkey', 'invite',
  // Automation (spec 2026-09-18 §8): a scheduled run is recorded against
  // its job template.
  'job_template', 'job', 'schedule', 'trigger', 'worker', 'channel', 'maintenance',
] as const;

/** known is the catalogue's label for a value the API sends, or the value
 * itself when it is one the page has no label for (a newer server). */
export function known(labels: Record<string, string>, value: string): string {
  return Object.hasOwn(labels, value) ? labels[value] : value;
}
