import type { Messages } from '../../i18n/types.ts';

/** TRIGGERS names what can start a job (jobs.trigger), in the order a
 * picker lists them. */
export const TRIGGERS = ['manual', 'schedule', 'webhook', 'system'] as const;

/** triggerLabel names what started a job; a trigger this build does not
 * know is shown as the API sent it. */
export function triggerLabel(trigger: string, t: Messages): string {
  return (t.runs.trigger as Record<string, string>)[trigger] ?? trigger;
}
