import { useNavigate } from '@tanstack/react-router';
import type { RowAction } from '../../../ui/data-list/index.ts';
import { useToast } from '../../../ui/toastContext.ts';
import { useT } from '../../../i18n/useT.ts';
import { useConfirmedDelete, type ConfirmedDeleteOptions } from '../../lists/useConfirmedDelete.ts';

type EditPath = '/o/$org/settings/shared/repos/$id' | '/o/$org/settings/shared/templates/$id' | '/o/$org/settings/shared/checks/$id';

/**
 * useSharedRowActions is the row menu every shared-resource list shares:
 * Open (the definition's form page) and Delete…, which asks, deletes and
 * says so in a toast. A refused delete shows the server's own message — a
 * definition a schedule, trigger or playbook still references refuses with
 * 409 (spec decision 5), and mapSharedErr composes that sentence server-side,
 * naming the organization and what references it, so an instance admin's own
 * screen can show it verbatim. `remove` is the kind's delete mutation; the
 * two messages say what this kind calls the thing.
 */
export function useSharedRowActions<T extends { id: string }>({
  org,
  to,
  remove,
  confirmMessage,
  deletedMessage,
}: {
  org: string;
  to: EditPath;
  remove: (id: string) => Promise<unknown>;
  confirmMessage: (definition: T) => string;
  deletedMessage: (definition: T) => string;
}): (definition: T) => RowAction[] {
  const t = useT();
  const toast = useToast();
  const navigate = useNavigate();
  // T extends { id: string }, so the conditional type's `rowId` stays optional.
  const del = useConfirmedDelete<T>({
    message: confirmMessage,
    failed: t.instance.shared.deleteFailed,
    remove,
    onDone: (d) => toast.show(deletedMessage(d)),
  } as ConfirmedDeleteOptions<T>);
  return (d) => [
    { id: 'open', label: t.instance.shared.open, onSelect: () => void navigate({ to, params: { org, id: d.id } }) },
    { id: 'delete', label: t.instance.shared.deleteItem, destructive: true, disabled: del.pending.has(d.id), onSelect: () => void del.run(d) },
  ];
}
