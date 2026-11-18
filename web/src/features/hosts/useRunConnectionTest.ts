import { useNavigate } from '@tanstack/react-router';
import { useStartConnectionTest } from '../../api/hooks/useHostOps.ts';
import { useT } from '../../i18n/useT.ts';
import { useToast } from '../../ui/toastContext.ts';

/** useRunConnectionTest starts a connection test and opens its run, or
 * says in a toast why it could not start. The card's button and the host
 * header's menu both use it. */
export function useRunConnectionTest(org: string, hostId: string): { start: () => Promise<void>; pending: boolean } {
  const t = useT();
  const toast = useToast();
  const start = useStartConnectionTest(org, hostId);
  const navigate = useNavigate();
  return {
    pending: start.isPending,
    start: async () => {
      try {
        const { job_id } = await start.mutateAsync();
        toast.show(t.hosts.connection.started);
        await navigate({ to: '/o/$org/runs/$id', params: { org, id: job_id } });
      } catch (err) {
        toast.show(err instanceof Error ? err.message : t.hosts.connection.startFailed, 'destructive');
      }
    },
  };
}
