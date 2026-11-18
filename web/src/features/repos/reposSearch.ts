import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import { matchesText } from '../lists/textMatch.ts';
import type { Repo } from '../../api/types.ts';

/** What the last sync said: it failed, it succeeded, or it never ran. */
export const SYNC_STATES = ['ok', 'failed', 'never'] as const;

/**
 * The repositories list's filters in the URL: a search over the name, the
 * URL and the branch, the last sync's outcome and the credential the
 * repository is cloned with.
 */
export const REPOS_FILTERS = {
  q: param.string(),
  sync: param.enum(SYNC_STATES),
  credential: param.string(),
};

export type ReposSearch = ListSearch & FilterSearch<typeof REPOS_FILTERS>;

export function validateReposSearch(search: Record<string, unknown>): ReposSearch {
  return { ...validateListSearch(search), ...validateFilters(REPOS_FILTERS, search) };
}

/** A repository's last sync as the sync filter reads it (SyncState's own order). */
export function syncStateOf(r: Pick<Repo, 'last_sync' | 'last_sync_error'>): (typeof SYNC_STATES)[number] {
  if (r.last_sync_error) return 'failed';
  return r.last_sync ? 'ok' : 'never';
}

/** The repositories a list's filters leave, in their order. */
export function filterRepos(rows: readonly Repo[], f: { q?: string; sync?: string; credential?: string }): Repo[] {
  return rows.filter((r) => {
    if (!matchesText(f.q, r.name, r.url, r.branch)) return false;
    if (f.sync && syncStateOf(r) !== f.sync) return false;
    if (f.credential && r.credential_id !== f.credential) return false;
    return true;
  });
}
