import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api, SESSION_LOCKED, unwrap } from '../client.ts';
import { errorStatus } from '../../lib/apiError.ts';
import { invalidateRepoDerived } from './invalidateRepoDerived.ts';
import { isTerminal, JOB_POLL_MS } from './useJobs.ts';
import { SYNC_WATCH_KEY, SyncWatchContext, type Watch } from './syncWatchContext.ts';

/** How many failed reads drop a watched job. */
const MAX_ERRORS = 3;

/** How long one job is watched at most; a sync that has not ended by then
 * is dropped (with the refresh a drop always does). */
const MAX_WATCH_MS = 60 * 60 * 1000;
/** A locked session answers every read with 403 session_locked: the job is
 * kept and read this many times more slowly until the unlock. */
const LOCKED_SLOWDOWN = 5;

const isLocked = (err: unknown) => errorStatus(err) === 403 && err instanceof Error && err.message === SESSION_LOCKED;

interface Watched {
  org: string;
  id: string;
}

/**
 * SyncWatchProvider watches the repository syncs started in this tab until
 * their jobs end, whichever page is open: each pending job is read every
 * `pollMs`, and when it turns terminal the repository-derived queries are
 * invalidated (the playbooks the sync found, the text it stored, the
 * variable sources it recorded, the runs list). A job that is gone or fails
 * to read three times is dropped, as is one still running after
 * `maxWatchMs`; a locked session is not a failure (the job stays, polled
 * slowly). Pending syncs are forgotten at a session boundary (the query
 * cache being cleared) and by a reload. Nothing is rendered.
 */
export function SyncWatchProvider({
  children,
  pollMs = JOB_POLL_MS,
  maxWatchMs = MAX_WATCH_MS,
}: {
  children: ReactNode;
  pollMs?: number;
  maxWatchMs?: number;
}) {
  const qc = useQueryClient();
  const [pending, setPending] = useState<Watched[]>([]);
  // resetSessionCache clears the query cache, which removes every watch
  // query: the matching pending entries go with them.
  useEffect(
    () =>
      qc.getQueryCache().subscribe((event) => {
        if (event.type !== 'removed') return;
        const [kind, org, id] = event.query.queryKey;
        if (kind !== SYNC_WATCH_KEY) return;
        setPending((list) => (list.some((w) => w.org === org && w.id === id) ? list.filter((w) => !(w.org === org && w.id === id)) : list));
      }),
    [qc],
  );
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
        <WatchedSync key={`${w.org}/${w.id}`} org={w.org} id={w.id} pollMs={pollMs} maxWatchMs={maxWatchMs} onDone={drop} />
      ))}
    </SyncWatchContext.Provider>
  );
}

function WatchedSync({
  org,
  id,
  pollMs,
  maxWatchMs,
  onDone,
}: {
  org: string;
  id: string;
  pollMs: number;
  maxWatchMs: number;
  onDone: (org: string, id: string) => void;
}) {
  const qc = useQueryClient();
  const done = useRef(false);
  // Failed reads in a row; a good read starts the count over.
  const [failures, setFailures] = useState(0);
  const query = useQuery({
    queryKey: [SYNC_WATCH_KEY, org, id],
    queryFn: async ({ signal }) => {
      try {
        const body = await unwrap(api.GET('/api/v1/orgs/{org}/jobs/{id}', { params: { path: { org, id } }, signal }));
        setFailures(0);
        return body;
      } catch (err) {
        // A locked session is neither a failure nor a good read.
        if (!isLocked(err)) setFailures((n) => n + 1);
        throw err;
      }
    },
    retry: false,
    gcTime: 0,
    refetchInterval: (q) => (isLocked(q.state.error) ? pollMs * LOCKED_SLOWDOWN : pollMs),
  });
  const [expired, setExpired] = useState(false);
  useEffect(() => {
    const t = setTimeout(() => setExpired(true), maxWatchMs);
    return () => clearTimeout(t);
  }, [maxWatchMs]);
  const ended = isTerminal(query.data?.job.status);
  const gone = expired || errorStatus(query.error) === 404 || (query.isError && failures >= MAX_ERRORS);
  useEffect(() => {
    if (done.current || !(ended || gone)) return;
    done.current = true;
    // A dropped job may have finished unseen: refreshing is harmless.
    invalidateRepoDerived(qc, org);
    onDone(org, id);
  }, [ended, gone, qc, org, id, onDone]);
  return null;
}
