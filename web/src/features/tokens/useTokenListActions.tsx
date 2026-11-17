import { useState, type ReactNode } from 'react';
import { useRevokeToken } from '../../api/hooks/useTokens.ts';
import { useRevokePersonalTokenGrant, useUnblockPersonalTokenOwner, type PersonalTokenEntry } from '../../api/hooks/usePersonalTokens.ts';
import type { components } from '../../api/schema.d.ts';
import { useT } from '../../i18n/useT.ts';
import type { RowAction } from '../../ui/data-list/index.ts';
import { useConfirm } from '../../ui/confirmContext.ts';
import { useToast } from '../../ui/toastContext.ts';
import { useConfirmedDelete } from '../lists/useConfirmedDelete.ts';
import { EditTokenDialog } from './EditTokenDialog.tsx';
import { isExpired } from './expiry.ts';
import type { TokenRow } from './tokenColumns.tsx';

type Token = components['schemas']['TokenDTO'];

/**
 * useUnblock lifts the block on a personal entry's owner: confirm, request,
 * a toast either way. `pending` holds the entries whose request runs.
 */
function useUnblock(org: string) {
  const t = useT();
  const p = t.tokens.personal;
  const toast = useToast();
  const ask = useConfirm();
  const unblock = useUnblockPersonalTokenOwner(org);
  const [pending, setPending] = useState<ReadonlySet<string>>(new Set());
  async function run(e: PersonalTokenEntry) {
    if (!(await ask({ message: p.unblockConfirm(e.owner_name), confirmLabel: t.ui.confirm.confirm }))) return;
    setPending((s) => new Set(s).add(e.token_id));
    try {
      await unblock.mutateAsync(e.token_id);
      toast.show(p.unblocked(e.owner_name));
    } catch (err) {
      toast.show(err instanceof Error ? err.message : p.unblockFailed, 'destructive');
    } finally {
      setPending((s) => {
        const next = new Set(s);
        next.delete(e.token_id);
        return next;
      });
    }
  }
  return { run, pending };
}

/**
 * useTokenListActions is the organization token list's row menu. An
 * organization token: Edit (not for an expired token, which cannot be
 * changed) and Revoke…, which asks the same question as before. A member's
 * personal token: "Revoke for this organization…", which blocks its owner
 * here; once the owner is blocked here, Unblock…, which lifts it (beside
 * Revoke on an entry that is blocked only through its owner).
 * `dialogs` holds the edit dialog.
 */
export function useTokenListActions(org: string): { rowActions: (row: TokenRow) => RowAction[]; dialogs: ReactNode } {
  const t = useT();
  const p = t.tokens.personal;
  const toast = useToast();
  const revoke = useRevokeToken(org);
  const revokeGrant = useRevokePersonalTokenGrant(org);
  const unblock = useUnblock(org);
  const [editing, setEditing] = useState<Token | null>(null);
  const del = useConfirmedDelete<Token>({
    message: (tok) => t.tokens.revokeConfirm(tok.name),
    failed: t.tokens.revokeFailed,
    confirmLabel: t.ui.confirm.confirm,
    remove: async (id) => {
      await revoke.mutateAsync(id);
    },
    onDone: (tok) => toast.show(t.tokens.revoked(tok.name)),
  });
  const block = useConfirmedDelete<PersonalTokenEntry>({
    message: (e) => p.revokeConfirm(e.name, e.owner_name),
    failed: p.revokeFailed,
    confirmLabel: t.ui.confirm.confirm,
    rowId: (e) => e.token_id,
    remove: (id) => revokeGrant.mutateAsync(id),
    onDone: (e) => toast.show(p.revoked(e.name)),
  });

  function rowActions(row: TokenRow): RowAction[] {
    if (row.type === 'personal') {
      const busy = block.pending.has(row.token_id) || unblock.pending.has(row.token_id);
      const unblockItem: RowAction = { id: 'unblock', label: p.unblockItem, onSelect: () => void unblock.run(row), disabled: busy };
      const revokeItem: RowAction = { id: 'revoke-grant', label: p.revokeItem, destructive: true, onSelect: () => void block.run(row), disabled: busy };
      // An entry blocked only through its owner can still be revoked itself,
      // so it stays out once the person's block is lifted.
      if (row.blocked) return [unblockItem];
      return row.owner_blocked ? [unblockItem, revokeItem] : [revokeItem];
    }
    return [
      { id: 'edit', label: t.tokens.editItem, onSelect: () => setEditing(row), disabled: isExpired(row.expires_at) },
      { id: 'revoke', label: t.tokens.revokeItem, destructive: true, onSelect: () => void del.run(row), disabled: del.pending.has(row.id) },
    ];
  }

  return {
    rowActions,
    dialogs: editing && <EditTokenDialog key={editing.id} org={org} token={editing} open onClose={() => setEditing(null)} />,
  };
}
