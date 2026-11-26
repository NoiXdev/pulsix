import type { Trigger } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { describeSettle } from './settle.ts';

/** useSettleSummary is a trigger's settle delay in words (describeSettle). */
export function useSettleSummary(trigger: Trigger): string {
  return describeSettle(trigger.settle_seconds, trigger.max_wait_seconds, useT().triggers.settle);
}
