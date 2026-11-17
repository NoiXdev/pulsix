import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import { Pill } from '../../ui/badge.tsx';
import type { AdminOrg } from '../../api/types.ts';
import type { Format } from '../../i18n/format.ts';
import type { Messages } from '../../i18n/types.ts';

/**
 * organizationColumns is every column the instance's organization list
 * offers (list id `organizations`), sorted in the browser. A member's row
 * links to the organization's hosts page, which names it; a row the admin is
 * not a member of has no link, so its name cell says why in text a screen
 * reader gets too, not only in a title.
 */
export function organizationColumns(t: Messages, fmt: Format): DataListColumn<AdminOrg>[] {
  const o = t.instance.orgs;
  return [
    {
      id: 'name',
      label: o.columns.organization,
      width: 'minmax(0, 1.2fr)',
      sortKey: 'name',
      sortValue: (r) => r.name,
      required: true,
      defaultVisible: true,
      render: (r) =>
        r.actor_is_member ? (
          <span className="block truncate font-semibold">{r.name}</span>
        ) : (
          <span className="flex min-w-0 items-center gap-2" title={o.notMemberTitle}>
            <span className="truncate font-semibold">{r.name}</span>
            <Pill tone="unknown">{o.notMember}</Pill>
            <span className="sr-only">{o.notMemberTitle}</span>
          </span>
        ),
    },
    {
      id: 'slug',
      label: o.columns.slug,
      width: 'minmax(0, 0.8fr)',
      sortKey: 'slug',
      sortValue: (r) => r.slug,
      defaultVisible: true,
      render: (r) => <span className={`block truncate font-mono text-muted-foreground ${CELL_MINOR}`}>{r.slug}</span>,
    },
    {
      id: 'members',
      label: o.columns.members,
      width: '100px',
      sortKey: 'members',
      sortValue: (r) => r.member_count,
      defaultVisible: true,
      render: (r) => <span className="font-mono">{r.member_count}</span>,
    },
    {
      id: 'created',
      label: o.columns.created,
      width: '120px',
      sortKey: 'created',
      sortValue: (r) => r.created_at,
      defaultVisible: true,
      render: (r) => <span className={`text-muted-foreground ${CELL_MINOR}`}>{fmt.date(r.created_at)}</span>,
    },
  ];
}
