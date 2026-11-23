import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import { errorStatus } from '../../lib/apiError.ts';
import { isTerminal, JOB_POLL_MS } from './useJobs.ts';
import { SyncWatchContext, type Watch } from './syncWatchContext.ts';

/** How many failed reads drop a watched job. */
const MAX_ERRORS = 3;

interface Watched {
  org: string;
  id: string;
}

/**
 * SyncWatchProvider watches the repository syncs started in this tab until
 * their jobs end, whichever page is open: each pending job is read every
 * `pollMs`, and when it turns terminal the repository-derived queries are
 * invalidated (the playbooks the sync found, the text it stored, the runs
 * list). A job that is gone or fails to read three times is dropped. Nothing
 * is rendered; a reload forgets what was pending.
 */
export function SyncWatchProvider({ children, pollMs = JOB_POLL_MS }: { children: ReactNode; pollMs?: number }) {
  const [pending, setPending] = useState<Watched[]>([]);
  const watch = useCallback<Watch>((org, id) => {
    setPending((list) => (list.some((w) => w.org === org && w.id === id) ? list : [...list, { org, id }]));
  }, []);
  const drop = useCallback((org: string, id: string) => {
    setPending((list) => list.filter((w) => !(w.org === org && w.id === id)));
  }, []);

  return (
    <SyncWatchContext.Provider value={watch}>
      {children}
      {pending.map((w) => (
        <WatchedSync key={`${w.org}/${w.id}`} org={w.org} id={w.id} pollMs={pollMs} onDone={drop} />
      ))}
    </SyncWatchContext.Provider>
  );
}

function WatchedSync({ org, id, pollMs, onDone }: { org: string; id: string; pollMs: number; onDone: (org: string, id: string) => void }) {
  const qc = useQueryClient();
  const done = useRef(false);
  // Failed reads in a row; a good read starts the count over.
  const [failures, setFailures] = useState(0);
  const query = useQuery({
    queryKey: ['sync-watch', org, id],
    queryFn: async () => {
      try {
        const body = await unwrap(api.GET('/api/v1/orgs/{org}/jobs/{id}', { params: { path: { org, id } } }));
        setFailures(0);
        return body;
      } catch (err) {
        setFailures((n) => n + 1);
        throw err;
      }
    },
    retry: false,
    gcTime: 0,
    refetchInterval: pollMs,
  });
  const ended = isTerminal(query.data?.job.status);
  const gone = errorStatus(query.error) === 404 || (query.isError && failures >= MAX_ERRORS);
  useEffect(() => {
    if (done.current || !(ended || gone)) return;
    done.current = true;
    // A dropped job may have finished unseen: refreshing is harmless.
    void qc.invalidateQueries({ queryKey: ['repos', org] });
    void qc.invalidateQueries({ queryKey: ['playbooks', org] });
    void qc.invalidateQueries({ queryKey: ['jobs', org] });
    onDone(org, id);
  }, [ended, gone, qc, org, id, onDone]);
  return null;
}
