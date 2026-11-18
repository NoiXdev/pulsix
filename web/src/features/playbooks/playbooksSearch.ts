import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import { matchesText } from '../lists/textMatch.ts';
import type { Playbook } from '../../api/types.ts';

export const PLAYBOOK_SOURCES = ['repo', 'editor'] as const;

/**
 * The playbooks list's filters in the URL: a search over the summary and the
 * path, where the playbook comes from (a repository or the editor) and the
 * repository a repo playbook lives in.
 */
export const PLAYBOOKS_FILTERS = {
  q: param.string(),
  source: param.enum(PLAYBOOK_SOURCES),
  repo: param.string(),
};

export type PlaybooksSearch = ListSearch & FilterSearch<typeof PLAYBOOKS_FILTERS>;

export function validatePlaybooksSearch(search: Record<string, unknown>): PlaybooksSearch {
  return { ...validateListSearch(search), ...validateFilters(PLAYBOOKS_FILTERS, search) };
}

/** The playbooks a list's filters leave, in their order. */
export function filterPlaybooks(rows: readonly Playbook[], f: { q?: string; source?: string; repo?: string }): Playbook[] {
  return rows.filter((p) => {
    if (!matchesText(f.q, p.summary, p.path)) return false;
    if (f.source && p.source !== f.source) return false;
    if (f.repo && p.repo_id !== f.repo) return false;
    return true;
  });
}
