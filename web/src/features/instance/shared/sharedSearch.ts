import { param, validateFilters, type FilterSearch } from '../../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../../lists/listSearch.ts';
import { matchesText } from '../../lists/textMatch.ts';
import type { CheckDefinitionDef, RepoDefinition, TemplateDefinition } from '../../../api/types.ts';

/** The shared repository and job template lists' filter in the URL: a search. */
export const SHARED_FILTERS = {
  q: param.string(),
};

/** The shared check list's filters: a search and the plugin a definition runs. */
export const SHARED_CHECKS_FILTERS = {
  q: param.string(),
  plugin: param.string(),
};

export type SharedSearch = ListSearch & FilterSearch<typeof SHARED_FILTERS>;
export type SharedChecksSearch = ListSearch & FilterSearch<typeof SHARED_CHECKS_FILTERS>;

export function validateSharedSearch(search: Record<string, unknown>): SharedSearch {
  return { ...validateListSearch(search), ...validateFilters(SHARED_FILTERS, search) };
}

export function validateSharedChecksSearch(search: Record<string, unknown>): SharedChecksSearch {
  return { ...validateListSearch(search), ...validateFilters(SHARED_CHECKS_FILTERS, search) };
}

export function filterSharedRepos(rows: readonly RepoDefinition[], f: { q?: string }): RepoDefinition[] {
  return rows.filter((d) => matchesText(f.q, d.name, d.url, d.branch, d.subpath));
}

export function filterSharedTemplates(rows: readonly TemplateDefinition[], f: { q?: string }, repoName: ReadonlyMap<string, string>): TemplateDefinition[] {
  return rows.filter((d) => matchesText(f.q, d.name, d.playbook_path, d.limit, repoName.get(d.shared_repo_id)));
}

export function filterSharedChecks(rows: readonly CheckDefinitionDef[], f: { q?: string; plugin?: string }): CheckDefinitionDef[] {
  return rows.filter((d) => matchesText(f.q, d.key, d.plugin) && (!f.plugin || d.plugin === f.plugin));
}
