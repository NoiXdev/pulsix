import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import { matchesText } from '../lists/textMatch.ts';
import { isExpired } from '../tokens/expiry.ts';
import type { PersonalToken } from '../../api/hooks/usePersonalTokens.ts';

export const PERSONAL_TOKEN_KINDS = ['api', 'assistant'] as const;

/**
 * The person's own token list's filters in the URL: a search over the name,
 * the kind (API or assistant), an organization the token is granted in
 * (an id) and the expired chip.
 */
export const PERSONAL_TOKENS_FILTERS = {
  q: param.string(),
  kind: param.enum(PERSONAL_TOKEN_KINDS),
  org: param.string(),
  expired: param.flag(),
};

export type PersonalTokensSearch = ListSearch & FilterSearch<typeof PERSONAL_TOKENS_FILTERS>;

export function validatePersonalTokensSearch(search: Record<string, unknown>): PersonalTokensSearch {
  return { ...validateListSearch(search), ...validateFilters(PERSONAL_TOKENS_FILTERS, search) };
}

/** The tokens a list's filters leave, in their order. */
export function filterPersonalTokens(rows: readonly PersonalToken[], f: { q?: string; kind?: string; org?: string; expired?: true }): PersonalToken[] {
  return rows.filter((r) => {
    if (!matchesText(f.q, r.name)) return false;
    if (f.kind && r.kind !== f.kind) return false;
    if (f.org && !(r.grants ?? []).some((g) => g.org_id === f.org)) return false;
    if (f.expired && !isExpired(r.expires_at)) return false;
    return true;
  });
}
