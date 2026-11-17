import { useState, type ReactNode } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import type { RowAction } from '../../ui/data-list/index.ts';
import { useToast } from '../../ui/toastContext.ts';
import { useDeleteSecret } from '../../api/hooks/useSecrets.ts';
import type { OrgSecret } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { useConfirmedDelete } from '../lists/useConfirmedDelete.ts';
import { conflictChecks } from './conflictChecks.ts';
import { SecretDialog } from './SecretDialog.tsx';

/**
 * useSecretListActions is the secret list's row menu, for a caller with
 * credentials.write only: Replace and Delete. A secret a check still names
 * answers Delete with the dialog saying which checks, instead of asking for
 * a delete the server would refuse; the 409 stays for a list gone stale.
 * `dialogs` holds that dialog and the secret dialog `openNew` or Replace
 * opened.
 */
export function useSecretListActions(
  org: string,
  canWrite: boolean,
  taken: string[],
): { rowActions: (secret: OrgSecret) => RowAction[]; dialogs: ReactNode; openNew: () => void } {
  const t = useT();
  const s = t.secrets;
  const toast = useToast();
  const remove = useDeleteSecret(org);
  const [editing, setEditing] = useState<OrgSecret | 'new' | null>(null);
  const [blocked, setBlocked] = useState<OrgSecret | null>(null);
  const del = useConfirmedDelete<OrgSecret>({
    rowId: (x) => x.name,
    message: (x) => s.deleteConfirm(x.name),
    failed: s.deleteFailed,
    remove: async (name) => {
      try {
        await remove.mutateAsync(name);
      } catch (err) {
        const checks = conflictChecks(err);
        if (checks) throw new Error(s.inUse(name, checks.join(', '), checks.length), { cause: err });
        throw err;
      }
    },
    onDone: (x) => toast.show(s.deleted(x.name)),
  });

  function rowActions(secret: OrgSecret): RowAction[] {
    if (!canWrite) return [];
    return [
      { id: 'replace', label: s.replace, onSelect: () => setEditing(secret) },
      {
        id: 'delete',
        label: s.delete,
        destructive: true,
        disabled: del.pending.has(secret.name),
        onSelect: () => ((secret.used_by ?? []).length > 0 ? setBlocked(secret) : void del.run(secret)),
      },
    ];
  }

  const usedBy = blocked?.used_by ?? [];
  return {
    rowActions,
    openNew: () => setEditing('new'),
    dialogs: (
      <>
        {blocked && (
          <Dialog open onClose={() => setBlocked(null)} title={s.inUseTitle}>
            <div className="flex flex-col gap-4">
              <p className="text-sm">{s.inUseBody(blocked.name, usedBy.join(', '), usedBy.length)}</p>
              <div className="flex justify-end">
                <Button variant="primary" onClick={() => setBlocked(null)}>
                  {s.close}
                </Button>
              </div>
            </div>
          </Dialog>
        )}
        {editing && <SecretDialog org={org} secret={editing === 'new' ? undefined : editing} taken={taken} onClose={() => setEditing(null)} />}
      </>
    ),
  };
}
