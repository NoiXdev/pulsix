import { useToast } from '../../ui/toastContext.ts';
import { useCancelJob } from '../../api/hooks/useJobs.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * useCancelRun is the cancel action of the run page and the runs list's row
 * menu: it asks the API to cancel the run and says in a toast whether the
 * run is cancelled already or only asked to stop (a claimed or running run
 * stops once its worker sees the request), or why the request failed.
 */
export function useCancelRun(org: string, id: string): { cancel: () => Promise<void>; isPending: boolean } {
  const t = useT();
  const toast = useToast();
  const mutation = useCancelJob(org, id);

  async function cancel() {
    try {
      const { job } = await mutation.mutateAsync();
      toast.show(job.status === 'cancelled' ? t.runs.detail.cancelled : t.runs.detail.cancelRequested);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.runs.detail.cancelFailed, 'destructive');
    }
  }

  return { cancel, isPending: mutation.isPending };
}
