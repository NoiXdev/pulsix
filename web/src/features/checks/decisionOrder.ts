import type { ItemDecision } from '../../api/types.ts';

/**
 * orderByCheck is the item decisions list's unsorted order: decisions on one
 * check together, each check where it first appears, so a host decision
 * overruling an organization one sits next to it. Within a check an
 * organization-scope decision comes before every host-scope one, reading as
 * the broad stroke the narrower rows sit on top of.
 */
export function orderByCheck(decisions: readonly ItemDecision[]): ItemDecision[] {
  const map = new Map<string, ItemDecision[]>();
  for (const d of decisions) {
    const bucket = map.get(d.check_key) ?? [];
    bucket.push(d);
    map.set(d.check_key, bucket);
  }
  for (const bucket of map.values()) {
    bucket.sort((a, b) => (a.scope === b.scope ? 0 : a.scope === 'org' ? -1 : 1));
  }
  return [...map.values()].flat();
}
