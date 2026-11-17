import { useState, type ReactNode } from 'react';
import type { RowAction } from '../../ui/data-list/index.ts';
import { useToast } from '../../ui/toastContext.ts';
import { backupDownloadUrl, useDeleteBackup } from '../../api/hooks/useBackups.ts';
import type { BackupEntry } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { useConfirmedDelete } from '../lists/useConfirmedDelete.ts';
import { RestoreDialog } from './RestoreDialog.tsx';
import type { RestoreDestination } from './restoreCommands.ts';

/**
 * useBackupListActions is the backup list's row menu. A stored file
 * (succeeded, or damaged — its header cannot be read) can be downloaded, as
 * a real link so the browser's "Save link as…" works, and deleted; only a
 * readable one can be restored, which opens the dialog with the command-line
 * restore. An attempt that left no file has no menu. `dialogs` holds the
 * restore dialog.
 */
export function useBackupListActions(destination: RestoreDestination): { rowActions: (item: BackupEntry) => RowAction[]; dialogs: ReactNode } {
  const t = useT();
  const toast = useToast();
  const remove = useDeleteBackup();
  const [restoring, setRestoring] = useState<string | null>(null);
  const del = useConfirmedDelete<BackupEntry>({
    rowId: (b) => b.name,
    message: (b) => t.backups.list.deleteConfirm(b.name),
    failed: t.backups.list.deleteFailed,
    remove: async (name) => {
      await remove.mutateAsync(name);
    },
    onDone: () => toast.show(t.backups.list.deleted),
  });

  function rowActions(item: BackupEntry): RowAction[] {
    if (item.state !== 'succeeded' && item.state !== 'damaged') return [];
    const actions: RowAction[] = [{ kind: 'link', id: 'download', label: t.backups.list.download, href: backupDownloadUrl(item.name), download: item.name }];
    if (item.state === 'succeeded') actions.push({ id: 'restore', label: t.backups.list.restore, onSelect: () => setRestoring(item.name) });
    actions.push({ id: 'delete', label: t.backups.list.deleteItem, destructive: true, disabled: del.pending.has(item.name), onSelect: () => void del.run(item) });
    return actions;
  }

  return {
    rowActions,
    dialogs: restoring && <RestoreDialog name={restoring} destination={destination} onClose={() => setRestoring(null)} />,
  };
}
