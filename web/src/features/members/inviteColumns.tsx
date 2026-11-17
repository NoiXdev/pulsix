import { RoleBadge } from '../../ui/badge.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { PendingInvite } from '../../api/types.ts';
import type { Format } from '../../i18n/format.ts';
import type { Messages } from '../../i18n/types.ts';
import { roleRank } from './roles.ts';

/**
 * inviteColumns is every column the open invitations list offers (list id
 * `invites`), sorted in the browser. An invitation opens nothing, so the
 * e-mail address is plain text.
 */
export function inviteColumns(t: Messages, fmt: Format): DataListColumn<PendingInvite>[] {
  return [
    {
      id: 'email',
      label: t.members.inviteDialog.emailLabel,
      width: 'minmax(0, 1.2fr)',
      sortKey: 'email',
      sortValue: (i) => i.email,
      required: true,
      defaultVisible: true,
      render: (i) => <span className="block truncate font-semibold">{i.email}</span>,
    },
    {
      id: 'role',
      label: t.members.columns.role,
      width: '120px',
      sortKey: 'role',
      sortValue: (i) => roleRank(i.role),
      defaultVisible: true,
      render: (i) => (
        <span>
          <RoleBadge role={i.role} />
        </span>
      ),
    },
    {
      id: 'invited_by',
      label: t.members.pending.invitedBy,
      width: 'minmax(0, 1fr)',
      sortKey: 'invited_by',
      sortValue: (i) => i.invited_by_name,
      defaultVisible: true,
      render: (i) => <span className={`block truncate text-muted-foreground ${CELL_MINOR}`}>{i.invited_by_name}</span>,
    },
    {
      id: 'expires',
      label: t.members.pending.expires,
      width: '120px',
      sortKey: 'expires',
      sortValue: (i) => i.expires_at,
      defaultVisible: true,
      render: (i) => <span className={`text-muted-foreground ${CELL_MINOR}`}>{fmt.date(i.expires_at)}</span>,
    },
  ];
}
