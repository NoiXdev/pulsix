import type { Tone } from '../../ui/badge.tsx';
import type { Messages } from '../../i18n/types.ts';

/**
 * The words the synthetic checks report that the GUI translates (backlog
 * 3.13b): a failure reason (`<plugin>.failure`), an assertion's result
 * (`http.assertion`) and the two words its value seen
 * (`http.assertion_actual`) can be. Shared by the host page and the
 * alerts, which both show them. A word this build does not know — a newer
 * server's — is shown as it came.
 */

/** failureText is a `<plugin>.failure` label in words. */
export function failureText(t: Messages, label: string): string {
  const texts: Readonly<Record<string, string>> = t.checks.synthetic.failures;
  return Object.hasOwn(texts, label) ? texts[label] : label;
}

/** isFailureLabel: a label of the failure vocabulary. */
export function isFailureLabel(t: Messages, label: string): boolean {
  return Object.hasOwn(t.checks.synthetic.failures, label);
}

const RESULT_TONE: Readonly<Record<string, Tone>> = { pass: 'ok', warn: 'warn', crit: 'crit' };

/** assertionResult is an `http.assertion` label in words, with its
 * tone. */
export function assertionResult(t: Messages, label: string): { label: string; tone: Tone } {
  const texts: Readonly<Record<string, string>> = t.checks.assertionResults;
  return { label: Object.hasOwn(texts, label) ? texts[label] : label, tone: RESULT_TONE[label] ?? 'unknown' };
}

/** assertionActualText is an `http.assertion_actual` label as shown: a
 * number or a quoted text as it came, `absent` and `(hidden)` in
 * words. */
export function assertionActualText(t: Messages, label: string): string {
  if (label === 'absent') return t.checks.assertionActual.absent;
  if (label === '(hidden)') return t.checks.assertionActual.hidden;
  return label;
}

/** discreteText is a discrete reading of a check as the host page shows
 * it: the words above for the metrics that carry them, the label itself
 * for every other. */
export function discreteText(t: Messages, plugin: string, metric: string, label: string): string {
  if (metric === `${plugin}.failure`) return failureText(t, label);
  if (metric === 'http.assertion') return assertionResult(t, label).label;
  if (metric === 'http.assertion_actual') return assertionActualText(t, label);
  return label;
}
