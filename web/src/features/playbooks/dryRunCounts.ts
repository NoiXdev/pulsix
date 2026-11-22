import type { JobEvent } from '../../api/types.ts';
import { ZERO_COUNTS, type RecapCounts } from '../runs/hostCounts.ts';

/** The recap's count kinds, in the order the dry run's tiles show them. */
export const COUNT_KEYS = ['ok', 'changed', 'skipped', 'failed', 'unreachable'] as const;

/** countEvents counts a run's task results by state, the live stand-in for
 * the recap's counts until the run has one. */
export function countEvents(events: readonly JobEvent[]): RecapCounts {
  const out: RecapCounts = { ...ZERO_COUNTS };
  for (const e of events) if ((COUNT_KEYS as readonly string[]).includes(e.state)) out[e.state as keyof RecapCounts]++;
  return out;
}
