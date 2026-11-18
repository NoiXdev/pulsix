import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import { matchesText } from '../lists/textMatch.ts';
import type { Agent } from '../../api/types.ts';

/**
 * The agents list's filters in the URL: a search over the host and agent
 * name, the status and `outdated` (the manager's own verdict that the
 * agent's build needs an update). An agent has no zone of its own, so
 * there is no zone filter. The pending tokens below the list keep their
 * own expired chip in component state, like their sort and page.
 */
export const AGENTS_FILTERS = {
  q: param.string(),
  status: param.enum(['online', 'offline']),
  outdated: param.flag(),
};

export type AgentsSearch = ListSearch & FilterSearch<typeof AGENTS_FILTERS>;

export function validateAgentsSearch(search: Record<string, unknown>): AgentsSearch {
  return { ...validateListSearch(search), ...validateFilters(AGENTS_FILTERS, search) };
}

/** The agents a list's filters leave, in their order. */
export function filterAgents(rows: readonly Agent[], f: { q?: string; status?: string; outdated?: true }): Agent[] {
  return rows.filter((a) => {
    if (!matchesText(f.q, a.host_name, a.agent_name)) return false;
    if (f.status && a.status !== f.status) return false;
    if (f.outdated && a.version_status !== 'update_required') return false;
    return true;
  });
}
