import { useHostNames } from '../../api/hooks/useHosts.ts';
import { useGroups, useGroupsMembers } from '../../api/hooks/useInventory.ts';
import type { Template } from '../../api/types.ts';
import { descendantIds } from '../inventory/tree.ts';
import { limitMatcher } from './limit.ts';

export interface ScopeHost {
  id: string;
  name: string;
}

/** What a dry run's hosts can be chosen from: the template's own hosts when
 * its scope resolves here, `open` (every managed host, the server keeping
 * only the template's) when it does not, `pending` while it is read. */
export type TemplateHosts = { kind: 'listed'; hosts: ScopeHost[] } | { kind: 'open' } | { kind: 'pending' };

const NONE: readonly string[] = [];

/**
 * useTemplateHosts resolves a template's scope the way the server does
 * (internal/jobs/resolver.go), as far as the browser can: a group means
 * its members and those of every group below it; explicit hosts mean
 * those, and both together their intersection. A scope of neither (the
 * whole organization), or one whose reads failed, stays `open`. The
 * template's limit narrows the hosts it can name (limit.ts); one the browser
 * cannot read keeps them all, the server applying it.
 */
export function useTemplateHosts(org: string, template: Template | undefined): TemplateHosts {
  const groupId = template?.scope.group_id ?? '';
  const hostIds = template?.scope.host_ids ?? NONE;
  const groups = useGroups(org, { enabled: !!groupId });
  const subtree = groupId && groups.data ? [groupId, ...descendantIds(groups.data.groups ?? [], groupId)] : [];
  const members = useGroupsMembers(org, subtree);
  const named = useHostNames(org, groupId ? NONE : hostIds);

  const keep = limitMatcher(template?.limit ?? '');

  if (!template) return { kind: 'pending' };
  if (!groupId) return hostIds.length > 0 ? { kind: 'listed', hosts: sortByName(named.filter((h) => keep(h.name))) } : { kind: 'open' };
  if (groups.isError || members.isError) return { kind: 'open' };
  if (!groups.data || !members.members) return { kind: 'pending' };
  const byId = new Map<string, ScopeHost>();
  for (const m of members.members) {
    if (hostIds.length > 0 && !hostIds.includes(m.host_id)) continue;
    if (!keep(m.host_name)) continue;
    byId.set(m.host_id, { id: m.host_id, name: m.host_name });
  }
  return { kind: 'listed', hosts: sortByName([...byId.values()]) };
}

function sortByName(hosts: ScopeHost[]): ScopeHost[] {
  return [...hosts].sort((a, b) => a.name.localeCompare(b.name));
}
