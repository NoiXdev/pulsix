import type { Rule } from '../../api/types.ts';

/**
 * orderByMetric is the rules list's unsorted order: rules on one metric
 * together, each metric where it first appears. Two rules on the same
 * metric (an org-wide warning and a host-scoped override, say) then sit
 * next to each other, so which one wins is visible without opening either.
 */
export function orderByMetric(rules: readonly Rule[]): Rule[] {
  const map = new Map<string, Rule[]>();
  for (const r of rules) {
    const bucket = map.get(r.metric) ?? [];
    bucket.push(r);
    map.set(r.metric, bucket);
  }
  return [...map.values()].flat();
}
