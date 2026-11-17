import { useEffect, useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { DataList } from '../../ui/data-list/index.ts';
import { Input } from '../../ui/input.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useAdminUsersPage } from '../../api/hooks/useAdminUsers.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { useListView } from '../lists/useListView.ts';
import { userColumns } from './userColumns.tsx';

/** How long typing must pause before the search goes out. */
const SEARCH_DEBOUNCE_MS = 250;

/**
 * UsersPage lists every account on the instance for its administrators
 * (list id `users`): searchable by name or e-mail (once typing pauses; a
 * new search starts again at page 1), sorted and paged by the server. A row
 * opens the account page. Reachable with Principal.InstanceAdmin only: GET
 * /admin/users admits no other caller, so anyone else who reaches the URL
 * sees the access-denied page. It lives under an organization's Settings
 * for navigation only — the accounts are instance-wide.
 */
export function UsersPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const fmt = useFormat();
  const [input, setInput] = useState('');
  const [q, setQ] = useState('');
  useEffect(() => {
    const timer = setTimeout(() => setQ(input.trim()), SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [input]);

  const columns = userColumns(t, fmt);
  const view = useListView('users', columns, { mode: 'server', resetKey: JSON.stringify({ q }) });
  const users = useAdminUsersPage(q, view.serverQuery, { enabled: view.ready });

  const guard = useQueryGuard(users);
  if (guard) return guard;

  return (
    <>
      <h1 className="text-xl font-semibold">{t.settings.tabs.users}</h1>
      <DataList
        label={t.users.label}
        columns={columns}
        rows={users.data?.users ?? []}
        rowKey={(u) => u.id}
        rowLink={(u) => ({ to: '/o/$org/settings/users/$userId', params: { org, userId: u.id }, label: u.email })}
        loading={users.isPending || users.isPlaceholderData}
        empty={t.users.empty}
        toolbar={
          <Input
            type="search"
            aria-label={t.users.searchLabel}
            placeholder={t.users.searchPlaceholder}
            value={input}
            onChange={(e) => setInput(e.target.value)}
            className="max-w-sm"
          />
        }
        {...view.listProps(users.isPlaceholderData ? undefined : users.data?.total, t.users.count)}
      />
    </>
  );
}
