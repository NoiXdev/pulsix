import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import { matchesText } from '../lists/textMatch.ts';
import type { Template } from '../../api/types.ts';

/** What a template's scope names: nothing (the whole organization), a group or explicit hosts. */
export const TEMPLATE_SCOPES = ['whole', 'group', 'hosts'] as const;

/**
 * The job templates list's filters in the URL: a search over the name and
 * the limit, the playbook it runs and its scope.
 */
export const TEMPLATES_FILTERS = {
  q: param.string(),
  playbook: param.string(),
  scope: param.enum(TEMPLATE_SCOPES),
};

/** `create` opens the new template dialog with that playbook chosen (the
 * playbook page's "Create template…"), the way `?id=` opens a schedule. */
export type TemplatesSearch = ListSearch & FilterSearch<typeof TEMPLATES_FILTERS> & { create?: string };

export function validateTemplatesSearch(search: Record<string, unknown>): TemplatesSearch {
  return {
    ...validateListSearch(search),
    ...validateFilters(TEMPLATES_FILTERS, search),
    create: typeof search.create === 'string' && search.create !== '' ? search.create : undefined,
  };
}

/** Whether a template's scope is the given kind (a group with hosts is both). */
function inScope(tpl: Template, scope: string): boolean {
  const group = !!tpl.scope.group_id;
  const hosts = (tpl.scope.host_ids ?? []).length > 0;
  if (scope === 'whole') return !group && !hosts;
  return scope === 'group' ? group : hosts;
}

/** The templates a list's filters leave, in their order. */
export function filterTemplates(rows: readonly Template[], f: { q?: string; playbook?: string; scope?: string }): Template[] {
  return rows.filter((tpl) => {
    if (!matchesText(f.q, tpl.name, tpl.limit)) return false;
    if (f.playbook && tpl.playbook_id !== f.playbook) return false;
    if (f.scope && !inScope(tpl, f.scope)) return false;
    return true;
  });
}
