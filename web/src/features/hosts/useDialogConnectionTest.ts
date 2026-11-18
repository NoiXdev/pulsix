import { useEffect, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useStartConnectionTest } from '../../api/hooks/useHostOps.ts';
import { isTerminal, useJob } from '../../api/hooks/useJobs.ts';
import { useT } from '../../i18n/useT.ts';

const POLL_MS = 1000;

/** A started test: the run, the time of the result the host had before it
 * (a result with that time is not this test's), and whether the result has
 * been read since the run ended. */
interface Started {
  jobId: string;
  before?: string;
  read: boolean;
}

/**
 * useDialogConnectionTest starts a connection test from the SSH access
 * dialog and follows it there, rather than leaving for the run page as the
 * card's button does: it polls the run until it ends, then reads the
 * host's latest test result once more.
 */
export function useDialogConnectionTest(org: string, hostId: string) {
  const t = useT();
  const qc = useQueryClient();
  const start = useStartConnectionTest(org, hostId);
  const [started, setStarted] = useState<Started | null>(null);
  const [error, setError] = useState<string | null>(null);
  const job = useJob(org, started?.jobId ?? '', {
    refetchInterval: (q) => {
      const data = q.state.data as { job?: { status?: string } } | undefined;
      return isTerminal(data?.job?.status) ? false : POLL_MS;
    },
  });
  const status = job.data?.job.status;
  const finished = !!started && isTerminal(status);
  const jobId = started?.jobId;

  useEffect(() => {
    if (!finished || !jobId) return;
    let live = true;
    void qc.refetchQueries({ queryKey: ['hostops', org, 'test', hostId] }).then(() => {
      if (live) setStarted((s) => (s && s.jobId === jobId ? { ...s, read: true } : s));
    });
    return () => {
      live = false;
    };
  }, [finished, jobId, qc, org, hostId]);

  return {
    started,
    status,
    error,
    pending: start.isPending || (!!started && !started.read),
    run: async () => {
      setError(null);
      try {
        const before = qc.getQueryData<{ test?: { at: string } }>(['hostops', org, 'test', hostId])?.test?.at;
        const { job_id } = await start.mutateAsync();
        setStarted({ jobId: job_id, before, read: false });
      } catch (err) {
        setError(err instanceof Error ? err.message : t.hosts.connection.startFailed);
      }
    },
  };
}

export type DialogConnectionTest = ReturnType<typeof useDialogConnectionTest>;
