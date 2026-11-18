import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import { matchesText } from '../lists/textMatch.ts';
import { roleRank } from '../members/roles.ts';
import { isExpired } from './expiry.ts';
import type { TokenRow } from './tokenColumns.tsx';

export const TOKEN_KINDS = ['api', 'assistant', 'personal'] as const;
export const TOKEN_ROLES = ['viewer', 'operator', 'admin', 'owner'] as const;
/** The owner filter's value for the organization's own tokens (a member's personal token has the member's id). */
export const ORG_OWNER = 'org';

/**
 * The organization's token list's filters in the URL: a search over the name
 * and the owner, the owner (the organization itself or the member whose
 * personal token it is), the role, the kind (an API token, an assistant
 * token or a member's personal token) and the expired chip.
 */
export const TOKENS_FILTERS = {
  q: param.string(),
  owner: param.string(),
  role: param.enum(TOKEN_ROLES),
  kind: param.enum(TOKEN_KINDS),
  expired: param.flag(),
};

export type TokensSearch = ListSearch & FilterSearch<typeof TOKENS_FILTERS>;

export function validateTokensSearch(search: Record<string, unknown>): TokensSearch {
  return { ...validateListSearch(search), ...validateFilters(TOKENS_FILTERS, search) };
}

/** The rows a list's filters leave, in their order. */
export function filterTokens(
  rows: readonly TokenRow[],
  f: { q?: string; owner?: string; role?: string; kind?: string; expired?: true },
): TokenRow[] {
  return rows.filter((r) => {
    const owner = r.type === 'personal' ? r.owner_id : ORG_OWNER;
    if (!matchesText(f.q, r.name, r.type === 'personal' ? r.owner_name : undefined, r.type === 'personal' ? r.owner_email : undefined)) return false;
    if (f.owner && owner !== f.owner) return false;
    if (f.role && roleRank(r.role) !== roleRank(f.role)) return false;
    if (f.kind && (f.kind === 'personal' ? r.type !== 'personal' : r.type === 'personal' || r.kind !== f.kind)) return false;
    if (f.expired && !isExpired(r.expires_at)) return false;
    return true;
  });
}
