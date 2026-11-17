import type { RowAction } from '../../ui/data-list/index.ts';
import type { Bastion } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { bastionKeyState } from './bastionKeyState.ts';
import { useBastionCommands } from './useBastionCommands.ts';

/**
 * useBastionListActions is the jump hosts list's row menu, for a writer
 * only (a reader gets no menu): Edit, Delete, and Pin host key (a key not
 * pinned yet, enabled once a connection test observed one) or Unpin (a
 * pinned key). A `changed` key offers Pin: the operator first pins what the
 * test saw.
 */
export function useBastionListActions(org: string, canWrite: boolean, edit: (b: Bastion) => void): (b: Bastion) => RowAction[] {
  const t = useT();
  const commands = useBastionCommands(org);
  const m = t.credentials.jumpHosts;
  return (b) => {
    if (!canWrite) return [];
    const items: RowAction[] = [
      { id: 'edit', label: t.common.edit, onSelect: () => edit(b) },
      { id: 'delete', label: t.common.delete, destructive: true, onSelect: () => void commands.remove(b) },
    ];
    const state = bastionKeyState(b);
    if (state && state !== 'pinned') {
      items.push({ id: 'pin', label: m.pinItem, onSelect: () => void commands.pin(b), disabled: commands.keyPending.has(b.id) || !b.observed_key });
    }
    if (state === 'pinned') {
      items.push({ id: 'unpin', label: m.unpinItem, onSelect: () => void commands.unpin(b), disabled: commands.keyPending.has(b.id) });
    }
    return items;
  };
}
