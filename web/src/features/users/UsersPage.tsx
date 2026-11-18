import { useParams } from '@tanstack/react-router';
import { DataList } from '../../ui/data-list/index.ts';
import { ListToolbar, ResetFilters, SearchInput } from '../../ui/list-toolbar.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useAdminUsersPage } from '../../api/hooks/useAdminUsers.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { useListFilters } from '../lists/filterSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { USERS_FILTERS } from './usersSearch.ts';
import { userColumns } from './userColumns.tsx';
import { PageHeader } from '../../ui/settings.tsx';

/**
 * UsersPage lists every account on the instance for its administrators
 * (list id `users`): searchable by name or e-mail (the search is in the URL,
 * goes out once typing pauses and starts again at page 1), sorted and paged
 * by the server. The server has no account-state filter, so there is none. A row
 * opens the account page. Reachable with Principal.InstanceAdmin only: GET
 * /admin/users admits no other caller, so anyone else who reaches the URL
 * sees the access-denied page. It lives under an organization's Settings
 * for navigation only — the accounts are instance-wide.
 */
export function UsersPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const fmt = useFormat();
  const { filters, active, setFilter, reset } = useListFilters(USERS_FILTERS);
  const q = filters.q ?? '';

  const columns = userColumns(t, fmt);
  const view = useListView('users', columns, { mode: 'server', resetKey: JSON.stringify(filters) });
  const users = useAdminUsersPage(q.trim(), view.serverQuery, { enabled: view.ready });

  const guard = useQueryGuard(users);
  if (guard) return guard;

  return (
    <>
      <PageHeader title={t.settings.tabs.users} />
      <DataList
        label={t.users.label}
        columns={columns}
        rows={users.data?.users ?? []}
        rowKey={(u) => u.id}
        rowLink={(u) => ({ to: '/o/$org/settings/users/$userId', params: { org, userId: u.id }, label: u.email })}
        loading={users.isPending || users.isPlaceholderData}
        empty={active ? t.users.noMatch : t.users.empty}
        toolbar={
          <ListToolbar>
            <SearchInput
              label={t.users.searchLabel}
              placeholder={t.users.searchPlaceholder}
              value={q}
              onChange={(v) => setFilter('q', v || undefined, { replace: true })}
            />
            {active && <ResetFilters onReset={reset} />}
          </ListToolbar>
        }
        {...view.listProps(users.isPlaceholderData ? undefined : users.data?.total, t.users.count)}
      />
    </>
  );
}
