import type { Tone } from '../../ui/badge.tsx';

/** OUTCOME_TONE colours a schedule run's outcome with the state colours. */
export const OUTCOME_TONE: Record<string, Tone> = {
  triggered: 'ok',
  failed: 'crit',
  skipped_overlap: 'warn',
  skipped_maintenance: 'unknown',
  missed: 'warn',
};
