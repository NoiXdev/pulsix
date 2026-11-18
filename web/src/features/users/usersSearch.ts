import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';

/**
 * The accounts list's filter in the URL: a search over the name and e-mail,
 * which the server applies. GET /admin/users has no filter for an account's
 * state (it is derived, and neither filterable nor sortable there), so the
 * list offers none: filtering one server page by state would hide accounts
 * that sit on the next.
 */
export const USERS_FILTERS = {
  q: param.string(),
};

export type UsersSearch = ListSearch & FilterSearch<typeof USERS_FILTERS>;

export function validateUsersSearch(search: Record<string, unknown>): UsersSearch {
  return { ...validateListSearch(search), ...validateFilters(USERS_FILTERS, search) };
}
