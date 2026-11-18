import { useState } from 'react';
import { DataList } from '../../ui/data-list/index.ts';
import { ListToolbar, ResetFilters, ResultCount } from '../../ui/list-toolbar.tsx';
import type { BackupEntry } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { useFormat } from '../../i18n/format.ts';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import { useListView } from '../lists/useListView.ts';
import { backupColumns } from './backupColumns.tsx';
import type { RestoreDestination } from './restoreCommands.ts';
import { useBackupListActions } from './useBackupListActions.tsx';

const rowKey = (b: BackupEntry) => `${b.name}|${b.created_at}`;

/** The kind labels are lower case in the table ("manual (Ada)"); a picker's options start with a capital. */
const capitalised = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

/**
 * BackupList is the table of backups at the destination merged with recent
 * attempts (list id `backups`). A stored file can be downloaded and deleted,
 * and a readable one restored, from its row's menu (useBackupListActions);
 * an attempt that left no file (running, failed, interrupted) has no menu:
 * there is nothing to act on. A readable file whose header this instance
 * cannot authenticate (an older version's, another instance's, or one
 * someone else put there) is marked unverified but keeps its actions.
 *
 * One of several sections of the backups page, so its sort, page and the
 * kind and status filters live in the component, not in the URL.
 */
export function BackupList({ items, destination }: { items: BackupEntry[]; destination: RestoreDestination }) {
  const t = useT();
  const f = useFormat();
  const actions = useBackupListActions(destination);
  const columns = backupColumns(t, f);
  const [kind, setKind] = useState<string | undefined>();
  const [state, setState] = useState<string | undefined>();
  const active = !!kind || !!state;
  const view = useListView('backups', columns, { mode: 'client', urlState: false, rowKey, resetKey: `${kind}|${state}` });
  const shown = items.filter((b) => (!kind || b.kind === kind) && (!state || b.state === state));
  const { rows, total } = view.view(view.ready ? shown : undefined);
  const l = t.backups.list;

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
        empty={active ? l.noMatch : l.empty}
        toolbar={
          <ListToolbar count={<ResultCount active={active} shown={shown.length} total={items.length} noun={l.count} />}>
            <FilterSelect
              label={l.filters.kind}
              allLabel={l.filters.allKinds}
              options={[
                { value: 'scheduled', label: capitalised(l.scheduled) },
                { value: 'manual', label: capitalised(l.manual('')) },
              ]}
              value={kind}
              onChange={setKind}
            />
            <FilterSelect
              label={l.filters.state}
              allLabel={l.filters.anyState}
              options={(Object.keys(l.states) as Array<keyof typeof l.states>).map((s) => ({ value: s, label: l.states[s] }))}
              value={state}
              onChange={setState}
            />
            {active && (
              <ResetFilters
                onReset={() => {
                  setKind(undefined);
                  setState(undefined);
                }}
              />
            )}
          </ListToolbar>
        }
        {...view.listProps(total, t.backups.list.count)}
      />
      {actions.dialogs}
    </>
  );
}
