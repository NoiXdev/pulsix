import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { RoleBadge } from '../../ui/badge.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { usePendingInvites, useRevokeInvite } from '../../api/hooks/useMembers.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';

const COLUMNS = 'minmax(0, 1fr) 110px minmax(0, 160px) 110px 70px';

/**
 * PendingInvites lists the organization's open invitations under the
 * member table, so an invitation sent to a wrong address can be withdrawn
 * instead of waited out (backlog 7.29). It renders nothing while there are
 * none — or when the list cannot be loaded, which must not take the member
 * table down with it.
 */
export function PendingInvites({ org }: { org: string }) {
  const toast = useToast();
  const t = useT();
  const fmt = useFormat();
  const invites = usePendingInvites(org);
  const revoke = useRevokeInvite(org);

  const rows = invites.data?.invites ?? [];
  if (rows.length === 0) return null;

  return (
    <section aria-label={t.members.pending.title} className="flex flex-col gap-2">
      <h2 className="text-sm font-semibold">{t.members.pending.title}</h2>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>{t.members.inviteDialog.emailLabel}</span>
          <span>{t.members.columns.role}</span>
          <span>{t.members.pending.invitedBy}</span>
          <span>{t.members.pending.expires}</span>
          <span />
        </GridHeader>
        {rows.map((inv) => (
          <GridRow key={inv.id} columns={COLUMNS}>
            <span className="truncate text-xs">{inv.email}</span>
            <RoleBadge role={inv.role} />
            <span className="truncate text-[11px] text-muted-foreground">{inv.invited_by_name}</span>
            <span className="text-[11px] text-muted-foreground">{fmt.date(inv.expires_at)}</span>
            <button
              type="button"
              className="text-[11px] text-destructive"
              aria-label={t.members.pending.revokeAria(inv.email)}
              onClick={async () => {
                if (!confirm(t.members.pending.confirmRevoke(inv.email))) return;
                try {
                  await revoke.mutateAsync(inv.id);
                  toast.show(t.members.pending.revoked(inv.email));
                } catch (err) {
                  toast.show(err instanceof Error ? err.message : t.members.pending.revokeFailed, 'destructive');
                }
              }}
            >
              {t.members.pending.revoke}
            </button>
          </GridRow>
        ))}
      </Card>
    </section>
  );
}
