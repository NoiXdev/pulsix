import type { Tone } from '../../ui/badge.tsx';
import type { Messages } from '../../i18n/types.ts';

/** OUTCOME_TONE colours a schedule run's outcome with the state colours. */
export const OUTCOME_TONE: Record<string, Tone> = {
  triggered: 'ok',
  failed: 'crit',
  skipped_overlap: 'warn',
  skipped_maintenance: 'maintenance',
  missed: 'warn',
};

/** outcomeLabel names a schedule run's outcome. It is the same word the
 * calendar uses for an occurrence (`calendar.state`); an outcome the
 * catalogue does not know (a newer server) is shown as it is. */
export function outcomeLabel(outcome: string, t: Messages): string {
  const labels: Record<string, string> = t.calendar.state;
  return Object.hasOwn(labels, outcome) ? labels[outcome] : outcome;
}
