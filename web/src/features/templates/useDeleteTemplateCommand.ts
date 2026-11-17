import { useDeleteTemplate } from '../../api/hooks/useTemplates.ts';
import { useT } from '../../i18n/useT.ts';
import { useConfirm } from '../../ui/confirmContext.ts';
import { useToast } from '../../ui/toastContext.ts';

/**
 * useDeleteTemplateCommand is deleting a template as the list's row menu and
 * the dialog both do it: confirm, delete, toast. `run` resolves true once
 * the template is gone.
 */
export function useDeleteTemplateCommand(org: string): { run: (tpl: { id: string; name: string }) => Promise<boolean>; isPending: boolean } {
  const t = useT();
  const toast = useToast();
  const ask = useConfirm();
  const remove = useDeleteTemplate(org);
  async function run(tpl: { id: string; name: string }): Promise<boolean> {
    if (!(await ask({ message: t.templates.dialog.confirmDelete(tpl.name), confirmLabel: t.common.delete, destructive: true }))) return false;
    try {
      await remove.mutateAsync(tpl.id);
      toast.show(t.templates.dialog.deleted(tpl.name));
      return true;
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.templates.dialog.deleteFailed, 'destructive');
      return false;
    }
  }
  return { run, isPending: remove.isPending };
}
