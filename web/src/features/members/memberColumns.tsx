import { Pill, RoleBadge } from '../../ui/badge.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { Member } from '../../api/types.ts';
import type { Format } from '../../i18n/format.ts';
import type { Messages } from '../../i18n/types.ts';
import { roleRank } from './roles.ts';

function initials(name: string): string {
  return name
    .split(' ')
    .slice(0, 2)
    .map((p) => p[0]?.toUpperCase())
    .join('');
}

/**
 * memberColumns is every column the members list offers (list id `members`),
 * sorted in the browser. The member's name column holds the row's link to
 * the member page; the e-mail address, shown under the name before, is a
 * column of its own.
 */
export function memberColumns(t: Messages, fmt: Format): DataListColumn<Member>[] {
  const c = t.members.columns;
  return [
    {
      id: 'member',
      label: c.member,
      width: 'minmax(0, 1.2fr)',
      sortKey: 'name',
      sortValue: (m) => m.display_name,
      required: true,
      defaultVisible: true,
      render: (m) => (
        <span className="flex min-w-0 items-center gap-2.5">
          <span data-avatar aria-hidden="true" className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-brand-dim text-[11px] font-bold text-background">
            {initials(m.display_name)}
          </span>
          <span className="block truncate font-semibold">{m.display_name}</span>
        </span>
      ),
    },
    {
      id: 'email',
      label: c.email,
      width: 'minmax(0, 1.2fr)',
      sortKey: 'email',
      sortValue: (m) => m.email,
      defaultVisible: true,
      render: (m) => <span className={`block truncate text-muted-foreground ${CELL_MINOR}`}>{m.email}</span>,
    },
    {
      id: 'role',
      label: c.role,
      width: '130px',
      sortKey: 'role',
      sortValue: (m) => roleRank(m.role),
      defaultVisible: true,
      render: (m) => (
        <span>
          <RoleBadge role={m.role} />
        </span>
      ),
    },
    {
      id: 'joined',
      label: c.joined,
      width: '110px',
      sortKey: 'joined',
      sortValue: (m) => m.created_at,
      defaultVisible: true,
      render: (m) => <span className={`text-muted-foreground ${CELL_MINOR}`}>{fmt.date(m.created_at)}</span>,
    },
    {
      id: 'status',
      label: c.status,
      width: '100px',
      sortKey: 'status',
      sortValue: (m) => (m.disabled_at ? 1 : 0),
      defaultVisible: true,
      render: (m) => <span>{m.disabled_at ? <Pill tone="unknown">{t.members.disabled}</Pill> : <Pill tone="ok">{t.members.active}</Pill>}</span>,
    },
  ];
}
