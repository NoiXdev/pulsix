import { useState, type ReactNode } from 'react';
import { useRevokeToken } from '../../api/hooks/useTokens.ts';
import type { components } from '../../api/schema.d.ts';
import { useT } from '../../i18n/useT.ts';
import type { RowAction } from '../../ui/data-list/index.ts';
import { useToast } from '../../ui/toastContext.ts';
import { useConfirmedDelete } from '../lists/useConfirmedDelete.ts';
import { EditTokenDialog } from './EditTokenDialog.tsx';
import { isExpired } from './expiry.ts';

type Token = components['schemas']['TokenDTO'];

/**
 * useTokenListActions is the organization token list's row menu: Edit (not
 * for an expired token, which cannot be changed) and Revoke…, which asks
 * the same question as before. `dialogs` holds the edit dialog.
 */
export function useTokenListActions(org: string): { rowActions: (token: Token) => RowAction[]; dialogs: ReactNode } {
  const t = useT();
  const toast = useToast();
  const revoke = useRevokeToken(org);
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

  function rowActions(tok: Token): RowAction[] {
    return [
      { id: 'edit', label: t.tokens.editItem, onSelect: () => setEditing(tok), disabled: isExpired(tok.expires_at) },
      { id: 'revoke', label: t.tokens.revokeItem, destructive: true, onSelect: () => void del.run(tok), disabled: del.pending.has(tok.id) },
    ];
  }

  return {
    rowActions,
    dialogs: editing && <EditTokenDialog key={editing.id} org={org} token={editing} open onClose={() => setEditing(null)} />,
  };
}
