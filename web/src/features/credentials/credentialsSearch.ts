import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import { matchesText } from '../lists/textMatch.ts';
import type { Credential } from '../../api/types.ts';

export const CREDENTIAL_KINDS = ['ssh_key', 'ssh_password', 'git_key', 'git_token', 'vault_password'] as const;

/**
 * The credentials list's filters in the URL: a search over the name and
 * the login, the kind, the two expiry chips (`expiring` is the server's
 * expires_soon, 30 days) and `unused` (nothing refers to the credential).
 */
export const CREDENTIALS_FILTERS = {
  q: param.string(),
  kind: param.enum(CREDENTIAL_KINDS),
  expiring: param.flag(),
  expired: param.flag(),
  unused: param.flag(),
};

export type CredentialsSearch = ListSearch & FilterSearch<typeof CREDENTIALS_FILTERS>;

export function validateCredentialsSearch(search: Record<string, unknown>): CredentialsSearch {
  return { ...validateListSearch(search), ...validateFilters(CREDENTIALS_FILTERS, search) };
}

/** The credentials a list's filters leave, in their order. */
export function filterCredentials(
  rows: readonly Credential[],
  f: { q?: string; kind?: string; expiring?: true; expired?: true; unused?: true },
): Credential[] {
  return rows.filter((c) => {
    if (!matchesText(f.q, c.name, c.username)) return false;
    if (f.kind && c.kind !== f.kind) return false;
    if (f.expired && !c.expired) return false;
    if (f.expiring && !(c.expires_soon && !c.expired)) return false;
    if (f.unused && Object.values(c.used_by).some((n) => (n ?? 0) > 0)) return false;
    return true;
  });
}
