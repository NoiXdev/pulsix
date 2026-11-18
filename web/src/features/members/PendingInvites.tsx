import { useState } from 'react';
import { DataList, type RowAction } from '../../ui/data-list/index.ts';
import { FilterChip, ListToolbar, ResetFilters, ResultCount } from '../../ui/list-toolbar.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { usePendingInvites, useRevokeInvite } from '../../api/hooks/useMembers.ts';
import type { PendingInvite } from '../../api/types.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { useConfirmedDelete } from '../lists/useConfirmedDelete.ts';
import { useListView } from '../lists/useListView.ts';
import { isExpired } from '../tokens/expiry.ts';
import { inviteColumns } from './inviteColumns.tsx';

/**
 * PendingInvites lists the organization's open invitations under the
 * member table (list id `invites`), so an invitation sent to a wrong address
 * can be withdrawn instead of waited out (backlog 7.29). It is the page's
 * second list: its sort and page live in the component, not in the URL. It
 * renders nothing while there are none — or when the list cannot be loaded,
 * which must not take the member table down with it.
 */
export function PendingInvites({ org }: { org: string }) {
  const toast = useToast();
  const t = useT();
  const fmt = useFormat();
  const invites = usePendingInvites(org);
  const revoke = useRevokeInvite(org);
  const columns = inviteColumns(t, fmt);
  // The one filter lives in the component, like the list's sort and page.
  const [expired, setExpired] = useState(false);
  const view = useListView('invites', columns, { mode: 'client', urlState: false, rowKey: (i) => i.id, resetKey: String(expired) });
  const del = useConfirmedDelete<PendingInvite>({
    message: (i) => t.members.pending.confirmRevoke(i.email),
    failed: t.members.pending.revokeFailed,
    confirmLabel: t.ui.confirm.confirm,
    remove: async (id) => {
      await revoke.mutateAsync(id);
    },
    onDone: (i) => toast.show(t.members.pending.revoked(i.email)),
  });

  const all = invites.data?.invites ?? [];
  const shown = expired ? all.filter((i) => isExpired(i.expires_at)) : all;
  const { rows, total } = view.view(view.ready ? shown : undefined);
  if (all.length === 0) return null;

  const rowActions = (i: PendingInvite): RowAction[] => [
    { id: 'revoke', label: t.members.pending.revokeItem, destructive: true, onSelect: () => void del.run(i), disabled: del.pending.has(i.id) },
  ];

  return (
    <section aria-label={t.members.pending.title} className="flex flex-col gap-2">
      <h2 className="text-sm font-semibold">{t.members.pending.title}</h2>
      <DataList
        label={t.members.pending.label}
        columns={columns}
        rows={rows}
        rowKey={(i) => i.id}
        rowActions={rowActions}
        rowLabel={(i) => `${i.email} (${i.role})`}
        loading={!view.ready}
        empty={expired ? t.members.pending.noMatch : null}
        toolbar={
          <ListToolbar count={<ResultCount active={expired} shown={shown.length} total={all.length} noun={t.members.pending.count} />}>
            <FilterChip pressed={expired} tone="crit" onToggle={() => setExpired((v) => !v)}>
              {t.members.pending.expiredFilter}
            </FilterChip>
            {expired && <ResetFilters onReset={() => setExpired(false)} />}
          </ListToolbar>
        }
        {...view.listProps(total, t.members.pending.count)}
      />
    </section>
  );
}
