import { useToast } from '../../../ui/toastContext.ts';
import { useT } from '../../../i18n/useT.ts';

/**
 * useDeleteWithConfirm is the delete flow every shared-resource list shares:
 * ask for confirmation, delete, say so in a toast. A refused delete shows
 * the server's own message instead of a generic one — a definition a
 * schedule, trigger or playbook still references refuses with 409 (spec
 * decision 5), and mapSharedErr composes that sentence server-side, naming
 * the organization and what references it, specifically so an instance
 * admin's own screen can show it verbatim.
 *
 * `remove` is the kind's delete mutation (`mutation.mutateAsync`); the two
 * messages say what this kind calls the thing.
 */
export function useDeleteWithConfirm<T extends { id: string }>({
  remove,
  confirmMessage,
  deletedMessage,
}: {
  remove: (id: string) => Promise<unknown>;
  confirmMessage: (definition: T) => string;
  deletedMessage: (definition: T) => string;
}): (definition: T) => Promise<void> {
  const t = useT();
  const toast = useToast();
  return async (definition) => {
    if (!confirm(confirmMessage(definition))) return;
    try {
      await remove(definition.id);
      toast.show(deletedMessage(definition));
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.instance.shared.deleteFailed, 'destructive');
    }
  };
}
