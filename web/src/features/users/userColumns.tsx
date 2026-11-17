import type { AdminUser } from '../../api/types.ts';
import type { Format } from '../../i18n/format.ts';
import type { Messages } from '../../i18n/types.ts';
import { Pill } from '../../ui/badge.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import { accountStateLabel, accountStateTone } from './accountState.ts';

/**
 * userColumns is every column the instance admin's Users list offers (list
 * id `users`). The server sorts by e-mail (the default), name and last
 * sign-in (accounts that never signed in last); the derived state and the
 * organization count are not sortable.
 */
export function userColumns(t: Messages, fmt: Format): DataListColumn<AdminUser>[] {
  const c = t.users.columns;
  return [
    {
      id: 'email',
      label: c.email,
      width: 'minmax(0, 1.4fr)',
      sortKey: 'email',
      sortValue: (u) => u.email.toLowerCase(),
      required: true,
      defaultVisible: true,
      render: (u) => <span className="block truncate font-semibold">{u.email}</span>,
    },
    {
      id: 'name',
      label: c.name,
      width: 'minmax(0, 1fr)',
      sortKey: 'name',
      sortValue: (u) => u.display_name.toLowerCase(),
      defaultVisible: true,
      render: (u) => (
        <span className="flex min-w-0 items-center gap-2">
          <span className="truncate">{u.display_name}</span>
          {u.instance_admin && <Pill tone="accent">{t.users.instanceAdmin}</Pill>}
        </span>
      ),
    },
    {
      id: 'state',
      label: c.state,
      width: '190px',
      defaultVisible: true,
      render: (u) => (
        <span>
          <Pill tone={accountStateTone(u.state)}>{accountStateLabel(t, u.state)}</Pill>
        </span>
      ),
    },
    {
      id: 'orgs',
      label: c.orgs,
      width: '110px',
      defaultVisible: true,
      render: (u) => <span className="font-mono">{u.org_count}</span>,
    },
    {
      id: 'last_sign_in',
      label: c.lastSignIn,
      width: '150px',
      sortKey: 'last_sign_in',
      sortValue: (u) => u.last_sign_in_at,
      defaultVisible: true,
      render: (u) => (
        <span className={`text-muted-foreground ${CELL_MINOR}`}>{u.last_sign_in_at ? fmt.dateTime(u.last_sign_in_at) : t.users.never}</span>
      ),
    },
  ];
}
