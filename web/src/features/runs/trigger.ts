/** TRIGGER_LABEL names what started a job (jobs.trigger). */
export const TRIGGER_LABEL: Record<string, string> = { manual: 'Manual', schedule: 'Schedule', webhook: 'Webhook', system: 'System' };

export function triggerLabel(trigger: string): string {
  return TRIGGER_LABEL[trigger] ?? trigger;
}
