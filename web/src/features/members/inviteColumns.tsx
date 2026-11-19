import { Pill, RoleBadge } from '../../ui/badge.tsx';
import { Button } from '../../ui/button.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { PendingInvite } from '../../api/types.ts';
import type { Format } from '../../i18n/format.ts';
import type { Messages } from '../../i18n/types.ts';
import { roleRank } from './roles.ts';

/** What the expiry cell offers on an expired invitation; absent, it only
 * marks it (a reader who may not manage members). */
export interface InviteResend {
  onResend: (invite: PendingInvite) => void;
  /** The invitations whose resend is in flight. */
  pending: ReadonlySet<string>;
}

/**
 * inviteColumns is every column the open invitations list offers (list id
 * `invites`), sorted in the browser. An invitation opens nothing, so the
 * e-mail address is plain text. An expired invitation is marked in its
 * expiry cell, beside a Resend button when `resend` is given (backlog
 * 3.125).
 */
export function inviteColumns(t: Messages, fmt: Format, resend?: InviteResend): DataListColumn<PendingInvite>[] {
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
      width: 'minmax(220px, 1fr)',
      sortKey: 'expires',
      sortValue: (i) => i.expires_at,
      defaultVisible: true,
      render: (i) => (
        <span className="flex flex-wrap items-center gap-2">
          <span className={`text-muted-foreground ${CELL_MINOR}`}>{fmt.date(i.expires_at)}</span>
          {i.expired && <Pill tone="crit">{t.members.pending.expired}</Pill>}
          {i.expired && resend && (
            <Button
              type="button"
              className="relative z-10 h-7 px-2 text-[12px]"
              aria-label={t.members.pending.resendLabel(i.email)}
              disabled={resend.pending.has(i.id)}
              onClick={() => resend.onResend(i)}
            >
              {t.members.pending.resend}
            </Button>
          )}
        </span>
      ),
    },
  ];
}
