import { DataList } from '../../ui/data-list/index.ts';
import type { BackupEntry } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { useFormat } from '../../i18n/format.ts';
import { useListView } from '../lists/useListView.ts';
import { backupColumns } from './backupColumns.tsx';
import type { RestoreDestination } from './restoreCommands.ts';
import { useBackupListActions } from './useBackupListActions.tsx';

const rowKey = (b: BackupEntry) => `${b.name}|${b.created_at}`;

/**
 * BackupList is the table of backups at the destination merged with recent
 * attempts (list id `backups`). A stored file can be downloaded and deleted,
 * and a readable one restored, from its row's menu (useBackupListActions);
 * an attempt that left no file (running, failed, interrupted) has no menu:
 * there is nothing to act on. A readable file whose header this instance
 * cannot authenticate (an older version's, another instance's, or one
 * someone else put there) is marked unverified but keeps its actions.
 *
 * One of several sections of the backups page, so its sort and page live in
 * the component, not in the URL.
 */
export function BackupList({ items, destination }: { items: BackupEntry[]; destination: RestoreDestination }) {
  const t = useT();
  const f = useFormat();
  const actions = useBackupListActions(destination);
  const columns = backupColumns(t, f);
  const view = useListView('backups', columns, { mode: 'client', urlState: false, rowKey });
  const { rows, total } = view.view(view.ready ? items : undefined);

  return (
    <>
      <DataList
        label={t.backups.list.label}
        columns={columns}
        rows={rows}
        rowKey={rowKey}
        rowLabel={(b) => b.name}
        rowActions={actions.rowActions}
        loading={!view.ready}
        empty={t.backups.list.empty}
        {...view.listProps(total, t.backups.list.count)}
      />
      {actions.dialogs}
    </>
  );
}
