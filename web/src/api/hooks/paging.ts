import type { PageSize } from '../types.ts';

/**
 * The page a server-paged list asks for (the `page`, `per_page`, `sort`
 * and `order` query parameters of hosts, runs, alerts, users and the
 * activity feeds). `sort` and `order` are absent for the list's default
 * order. features/lists/useListView builds it; a list's page hook spreads
 * it into the request's query.
 */
export interface ServerPageQuery {
  page: number;
  per_page: PageSize;
  sort?: string;
  order?: 'asc' | 'desc';
}
