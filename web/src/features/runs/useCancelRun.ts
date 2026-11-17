import { useState } from 'react';
import { useToast } from '../../ui/toastContext.ts';
import { useCancelJob } from '../../api/hooks/useJobs.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * useCancelRuns is the cancel action of the runs list's row menu: it asks
 * the API to cancel a run and says in a toast whether the run is cancelled
 * already or only asked to stop (a claimed or running run stops once its
 * worker sees the request), or why the request failed. `isPending(id)` is
 * true while that run's request is in flight, so its menu item can be
 * disabled while other rows stay usable.
 */
export function useCancelRuns(org: string): { cancel: (id: string) => Promise<void>; isPending: (id: string) => boolean } {
  const t = useT();
  const toast = useToast();
  const mutation = useCancelJob(org);
  const [pending, setPending] = useState<ReadonlySet<string>>(new Set());

  async function cancel(id: string) {
    if (pending.has(id)) return;
    setPending((p) => new Set(p).add(id));
    try {
      const { job } = await mutation.mutateAsync(id);
      toast.show(job.status === 'cancelled' ? t.runs.detail.cancelled : t.runs.detail.cancelRequested);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.runs.detail.cancelFailed, 'destructive');
    } finally {
      setPending((p) => {
        const next = new Set(p);
        next.delete(id);
        return next;
      });
    }
  }

  return { cancel, isPending: (id) => pending.has(id) };
}

/** useCancelRun is useCancelRuns for the run page, which knows its run. */
export function useCancelRun(org: string, id: string): { cancel: () => Promise<void>; isPending: boolean } {
  const runs = useCancelRuns(org);
  return { cancel: () => runs.cancel(id), isPending: runs.isPending(id) };
}
