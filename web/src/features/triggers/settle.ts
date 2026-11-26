import type { Messages } from '../../i18n/types.ts';

/**
 * A trigger's settle delay (backlog 2.137): 0 is off (every call starts a
 * run), else 5–3600 seconds; the maximum wait, only with a delay, runs
 * from the delay to 86400 seconds and defaults to ten times the delay.
 * The bounds mirror the manager's (internal/triggers checkSettle).
 */
export const SETTLE_MIN = 5;
export const SETTLE_MAX = 3600;
export const MAX_WAIT_CAP = 86400;
export const MAX_WAIT_FACTOR = 10;

function whole(text: string): number | null {
  const s = text.trim();
  if (s === '') return 0;
  if (!/^\d+$/.test(s)) return null;
  return Number(s);
}

/** settleProblem names the field the server would refuse, or null. An
 * empty settle delay is off; an empty maximum wait is the default. */
export function settleProblem(settle: string, maxWait: string): 'settle' | 'maxWait' | null {
  const s = whole(settle);
  if (s === null || (s !== 0 && (s < SETTLE_MIN || s > SETTLE_MAX))) return 'settle';
  if (s === 0 || maxWait.trim() === '') return null;
  const m = whole(maxWait);
  if (m === null || m < s || m > MAX_WAIT_CAP) return 'maxWait';
  return null;
}

/** settleBody is what the form sends: the delay always (so an edit that
 * turns it off does), and with a delay the maximum wait, 0 for the
 * default. */
export function settleBody(settle: string, maxWait: string): { settle_seconds: number; max_wait_seconds?: number } {
  const s = whole(settle) ?? 0;
  if (s === 0) return { settle_seconds: 0 };
  return { settle_seconds: s, max_wait_seconds: whole(maxWait) ?? 0 };
}

/** duration reads seconds in the largest whole unit. */
export function duration(seconds: number): string {
  if (seconds >= 3600 && seconds % 3600 === 0) return `${seconds / 3600} h`;
  if (seconds >= 60 && seconds % 60 === 0) return `${seconds / 60} min`;
  return `${seconds} s`;
}

/** describeSettle is a settle delay in words: off, or the quiet moment it
 * waits for and the latest it runs (by default ten times the wait). */
export function describeSettle(settle: number | undefined, maxWait: number | undefined, m: Messages['triggers']['settle']): string {
  const s = settle ?? 0;
  if (s <= 0) return m.off;
  return m.summary(duration(s), duration(maxWait ?? MAX_WAIT_FACTOR * s));
}
