import { useEffect, useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { Card, GridHeader } from '../../ui/table.tsx';
import { Input } from '../../ui/input.tsx';
import { ListRow, ListRowName } from '../../ui/list-row.tsx';
import { LoadMoreButton } from '../../ui/LoadMoreButton.tsx';
import { Pill } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useAdminUsers } from '../../api/hooks/useAdminUsers.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { accountStateLabel, accountStateTone } from './accountState.ts';

const COLUMNS = 'minmax(0, 1.6fr) 190px 110px 150px';

/** How long typing must pause before the search goes out. */
const SEARCH_DEBOUNCE_MS = 250;

/**
 * UsersPage lists every account on the instance for its administrators:
 * searchable by name or e-mail (once typing pauses), paged by cursor. A row opens the account
 * page. Reachable with Principal.InstanceAdmin only: GET /admin/users
 * admits no other caller, so anyone else who reaches the URL sees the
 * access-denied page. It lives under an organization's Settings for
 * navigation only — the accounts are instance-wide.
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
  const users = useAdminUsers(q);

  const guard = useQueryGuard(users);
  if (guard) return guard;

  const list = users.data?.pages.flatMap((p) => p.users ?? []) ?? [];

  return (
    <>
      <h1 className="text-xl font-semibold">{t.settings.tabs.users}</h1>
      <Input
        type="search"
        aria-label={t.users.searchLabel}
        placeholder={t.users.searchPlaceholder}
        value={input}
        onChange={(e) => setInput(e.target.value)}
        className="max-w-sm"
      />
      {!users.data ? (
        <QueryLoading />
      ) : (
        <Card>
          <GridHeader columns={COLUMNS}>
            <span>{t.users.columns.account}</span>
            <span>{t.users.columns.state}</span>
            <span>{t.users.columns.orgs}</span>
            <span>{t.users.columns.lastSignIn}</span>
          </GridHeader>
          {list.map((u) => (
            <ListRow
              key={u.id}
              to="/o/$org/settings/users/$userId"
              params={{ org, userId: u.id }}
              columns={COLUMNS}
              label={u.display_name}
            >
              <div className="flex min-w-0 flex-col">
                <span className="flex min-w-0 items-center gap-2">
                  <ListRowName className="truncate text-[13px] font-semibold">{u.display_name}</ListRowName>
                  {u.instance_admin && <Pill tone="accent">{t.users.instanceAdmin}</Pill>}
                </span>
                <span className="truncate text-xs text-muted-foreground">{u.email}</span>
              </div>
              <span>
                <Pill tone={accountStateTone(u.state)}>{accountStateLabel(t, u.state)}</Pill>
              </span>
              <span className="font-mono text-xs">{u.org_count}</span>
              <span className="text-xs text-muted-foreground">
                {u.last_sign_in_at ? fmt.dateTime(u.last_sign_in_at) : t.users.never}
              </span>
            </ListRow>
          ))}
          {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.users.empty}</p>}
        </Card>
      )}
      {users.hasNextPage && (
        <LoadMoreButton onClick={() => void users.fetchNextPage()} loading={users.isFetchingNextPage} />
      )}
    </>
  );
}
