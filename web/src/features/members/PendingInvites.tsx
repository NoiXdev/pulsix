import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { RoleBadge } from '../../ui/badge.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { usePendingInvites, useRevokeInvite } from '../../api/hooks/useMembers.ts';

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
  const invites = usePendingInvites(org);
  const revoke = useRevokeInvite(org);

  const rows = invites.data?.invites ?? [];
  if (rows.length === 0) return null;

  return (
    <section aria-label="Open invitations" className="flex flex-col gap-2">
      <h2 className="text-sm font-semibold">Open invitations</h2>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>E-mail address</span>
          <span>Role</span>
          <span>Invited by</span>
          <span>Expires</span>
          <span />
        </GridHeader>
        {rows.map((inv) => (
          <GridRow key={inv.id} columns={COLUMNS}>
            <span className="truncate text-xs">{inv.email}</span>
            <RoleBadge role={inv.role} />
            <span className="truncate text-[11px] text-muted-foreground">{inv.invited_by_name}</span>
            <span className="text-[11px] text-muted-foreground">{new Date(inv.expires_at).toLocaleDateString()}</span>
            <button
              type="button"
              className="text-[11px] text-destructive"
              aria-label={`Revoke invitation to ${inv.email}`}
              onClick={async () => {
                if (!confirm(`Revoke the invitation to ${inv.email}? Its link stops working.`)) return;
                try {
                  await revoke.mutateAsync(inv.id);
                  toast.show(`Invitation to ${inv.email} revoked`);
                } catch (err) {
                  toast.show(err instanceof Error ? err.message : 'failed to revoke invitation', 'destructive');
                }
              }}
            >
              revoke
            </button>
          </GridRow>
        ))}
      </Card>
    </section>
  );
}
