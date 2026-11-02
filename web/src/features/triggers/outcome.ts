import type { Messages } from '../../i18n/types.ts';

/** outcomeLabel names what became of an incoming webhook call, or shows
 * the value itself for one the catalogue does not know (a newer server). */
export function outcomeLabel(outcome: string, t: Messages): string {
  const labels: Record<string, string> = t.triggers.outcome;
  return Object.hasOwn(labels, outcome) ? labels[outcome] : outcome;
}
