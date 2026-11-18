import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import { matchesText } from '../lists/textMatch.ts';
import type { InventoryGroupSummary } from '../../api/types.ts';
import { ancestorsOf, descendantIds } from './tree.ts';

/** The values filter: whether a group's own values satisfy its schema. */
export const GROUP_VALUES = ['complete', 'incomplete'] as const;

/**
 * The groups list's filters in the URL: a search over the name and the
 * description, `jump` (the group sets a jump host of its own, as the Jump
 * host column shows), `parent` (a group id: the groups anywhere below it)
 * and the values' completeness.
 */
export const GROUPS_FILTERS = {
  q: param.string(),
  jump: param.flag(),
  parent: param.string(),
  values: param.enum(GROUP_VALUES),
};

export type GroupsSearch = ListSearch & FilterSearch<typeof GROUPS_FILTERS>;

export function validateGroupsSearch(search: Record<string, unknown>): GroupsSearch {
  return { ...validateListSearch(search), ...validateFilters(GROUPS_FILTERS, search) };
}

/** The groups a list's filters leave, in their order. `all` is the whole
 * tree, which `parent` is read against. */
export function filterGroups<G extends InventoryGroupSummary>(
  rows: readonly G[],
  all: readonly G[],
  f: { q?: string; jump?: true; parent?: string; values?: (typeof GROUP_VALUES)[number] },
): G[] {
  const below = f.parent ? descendantIds(all, f.parent) : undefined;
  return rows.filter((g) => {
    if (!matchesText(f.q, g.name, g.description)) return false;
    if (f.jump && !g.bastion_id) return false;
    if (below && !below.has(g.id)) return false;
    if (f.values && g.complete !== (f.values === 'complete')) return false;
    return true;
  });
}

/** groupPath names the groups above id, from the top-level one down to its
 * parent ([] for a top-level group). */
export function groupPath(all: readonly InventoryGroupSummary[], id: string): string[] {
  return ancestorsOf(all, id).map((g) => g.name);
}
