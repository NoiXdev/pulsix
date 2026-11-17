import { useDeleteRepo } from '../../api/hooks/useRepos.ts';
import { useT } from '../../i18n/useT.ts';
import { useConfirm } from '../../ui/confirmContext.ts';
import { useToast } from '../../ui/toastContext.ts';

/**
 * useDeleteRepoCommand is deleting a repository as the list's row menu and
 * the dialog both do it: confirm, delete, toast. `run` resolves true once
 * the repository is gone.
 */
export function useDeleteRepoCommand(org: string): { run: (repo: { id: string; name: string }) => Promise<boolean>; isPending: boolean } {
  const t = useT();
  const toast = useToast();
  const ask = useConfirm();
  const remove = useDeleteRepo(org);
  async function run(repo: { id: string; name: string }): Promise<boolean> {
    if (!(await ask({ message: t.repos.dialog.confirmDelete(repo.name), confirmLabel: t.common.delete, destructive: true }))) return false;
    try {
      await remove.mutateAsync(repo.id);
      toast.show(t.repos.dialog.deleted(repo.name));
      return true;
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.repos.dialog.deleteFailed, 'destructive');
      return false;
    }
  }
  return { run, isPending: remove.isPending };
}
