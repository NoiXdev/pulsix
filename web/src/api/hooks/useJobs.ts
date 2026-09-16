import { useEffect, useRef, useState } from 'react';
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { JobEvent } from '../types.ts';

export const JOBS_PAGE_SIZE = 50;

export interface JobListFilter {
  status?: string;
  kind?: string;
}

/** useJobs is the keyset-paginated runs list; the filters are part of the
 * query key (changing one starts over from the first page) and validated
 * server-side — an invalid value is a 400, so the select options here must
 * stay the server's own vocabulary. */
export function useJobs(org: string, filter: { status?: string; kind?: string } = {}) {
  return useInfiniteQuery({
    queryKey: ['jobs', org, filter],
    initialPageParam: '',
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET('/api/v1/orgs/{org}/jobs', {
          params: {
            path: { org },
            query: {
              status: filter.status ?? '',
              kind: filter.kind ?? '',
              cursor: pageParam || undefined,
              limit: JOBS_PAGE_SIZE,
            },
          },
        }),
      ),
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    enabled: !!org,
  });
}

export function useJob(
  org: string,
  id: string,
  opts: { refetchInterval?: number | false | ((query: { state: { data?: unknown } }) => number | false | undefined) } = {},
) {
  return useQuery({
    queryKey: ['job', org, id],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/jobs/{id}', { params: { path: { org, id } } })),
    enabled: !!org && !!id,
    // TanStack's own refetchInterval callback type; re-declared loosely so
    // the run detail page can stop polling once the job is terminal.
    refetchInterval: opts.refetchInterval as number | false | ((query: unknown) => number | false | undefined) | undefined,
  });
}

function useInvalidateJobs(org: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ['jobs', org] });
    void qc.invalidateQueries({ queryKey: ['job', org] });
  };
}

export function useCancelJob(org: string, id: string) {
  const invalidate = useInvalidateJobs(org);
  return useMutation({
    mutationFn: () => unwrap(api.POST('/api/v1/orgs/{org}/jobs/{id}/cancel', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

/** useRetryFailedJobs re-runs a finished run's failed hosts (202, { jobs })
 * — one job per involved zone, sharing a new run group. */
export function useRetryFailedJobs(org: string, id: string) {
  const invalidate = useInvalidateJobs(org);
  return useMutation({
    mutationFn: () => unwrap(api.POST('/api/v1/orgs/{org}/jobs/{id}/retry-failed', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

const TERMINAL = new Set(['succeeded', 'failed', 'cancelled', 'error']);

export function isTerminal(status: string | undefined | null): boolean {
  return status !== undefined && status !== null && TERMINAL.has(status);
}

/** useJobEventsPage fetches one page of a run's events after a sequence
 * number — the polling fallback's fetch primitive. Exposed as a plain
 * async function (not a hook) so the SSE hook can call it from its poll
 * interval. */
export async function fetchJobEvents(org: string, id: string, after: number): Promise<JobEvent[]> {
  const body = await unwrap(
    api.GET('/api/v1/orgs/{org}/jobs/{id}/events', { params: { path: { org, id }, query: { after } } }),
  );
  return body.events ?? [];
}

export type RunLogTransport = 'sse' | 'poll';

export interface RunLog {
  events: JobEvent[];
  status: string | null;
  transport: RunLogTransport | null;
}

/**
 * useRunLog is the run view's live event log: it seeds the event history
 * with one plain fetch, then subscribes to the org-scoped SSE stream
 * (GET /orgs/{org}/jobs/{id}/stream). The SSE contract (Task 12) is what
 * shapes this hook:
 *
 *  - `event: log` frames carry the event JSON (seq, host, task, state,
 *    payload); a server under load may silently DROP frames (drop-on-full),
 *    so the client detects seq gaps — any event whose seq is greater than
 *    last+1 means frames were lost — closes the stream and falls back to
 *    polling GET /events?after=<seq>, which replays the gap.
 *  - `event: status` frames carry {id, status}; the terminal status frame
 *    ends the stream, and the hook stops connecting.
 *  - the server ends the stream on transient errors; EventSource
 *    reconnects by itself, and a reconnect's missed frames surface as a
 *    seq gap → the same polling fallback.
 *
 * Polling keeps the events fresh (and, with the page's own job-detail
 * polling, the status) until the run reaches a terminal state.
 */
export function useRunLog(org: string, id: string): RunLog {
  const [events, setEvents] = useState<JobEvent[]>([]);
  const [status, setStatus] = useState<string | null>(null);
  const [transport, setTransport] = useState<RunLogTransport | null>(null);
  // lastSeq lives in a ref so the SSE handlers and the poll timer read the
  // freshest value without re-subscribing on every event.
  const lastSeq = useRef(0);

  // Seed the history once: everything already streamed before this page
  // opened. A later SSE reconnect replays from the subscription point, not
  // from lastSeq — the seed plus the dedup below covers both.
  useEffect(() => {
    if (!org || !id) return;
    let alive = true;
    lastSeq.current = 0;
    setEvents([]);
    setStatus(null);
    void fetchJobEvents(org, id, 0)
      .then((seed) => {
        if (!alive) return;
        const last = seed.length > 0 ? seed[seed.length - 1].seq : 0;
        lastSeq.current = last;
        setEvents(mergeEvents([], seed));
        setTransport('sse');
      })
      .catch(() => {
        if (alive) setTransport('poll');
      });
    return () => {
      alive = false;
    };
  }, [org, id]);

  // The live tail. mode sse → EventSource; poll → interval fetch.
  useEffect(() => {
    if (!org || !id) return;
    if (transport === 'sse') {
      const es = new EventSource(`/api/v1/orgs/${org}/jobs/${id}/stream`);
      const close = () => es.close();

      es.addEventListener('log', (e) => {
        let ev: JobEvent;
        try {
          ev = JSON.parse((e as MessageEvent<string>).data) as JobEvent;
        } catch {
          return; // a frame we cannot read is not a gap
        }
        if (ev.seq <= lastSeq.current) return; // replay or duplicate
        if (ev.seq > lastSeq.current + 1) {
          // A gap: the stream dropped frames (drop-on-full) or we
          // disconnected mid-run. Polling replays exactly what is missing.
          close();
          setTransport('poll');
          return;
        }
        lastSeq.current = ev.seq;
        setEvents((prev) => mergeEvents(prev, [ev]));
      });

      es.addEventListener('status', (e) => {
        try {
          const frame = JSON.parse((e as MessageEvent<string>).data) as { status?: string };
          if (frame.status) {
            setStatus(frame.status);
            if (isTerminal(frame.status)) close(); // terminal frame ends the stream
          }
        } catch {
          /* ignore an unreadable status frame */
        }
      });

      es.addEventListener('error', () => {
        // The server's error frame (or a broken connection): fall back to
        // polling, which answers errors properly (404 and such).
        close();
        setTransport('poll');
      });

      return close;
    }
    if (transport === 'poll') {
      const poll = async () => {
        try {
          const next = await fetchJobEvents(org, id, lastSeq.current);
          if (next.length > 0) {
            lastSeq.current = next[next.length - 1].seq;
            setEvents((prev) => mergeEvents(prev, next));
          }
        } catch {
          // Transient: the next tick tries again.
        }
      };
      const timer = window.setInterval(() => void poll(), 1500);
      void poll();
      return () => window.clearInterval(timer);
    }
  }, [org, id, transport]);

  return { events, status, transport };
}

/** mergeEvents folds a fetched batch into the existing list, keeping the
 * list sorted by seq and free of duplicates (the SSE reconnect path can
 * replay overlapping ranges). */
export function mergeEvents(prev: readonly JobEvent[], next: readonly JobEvent[]): JobEvent[] {
  const seen = new Set(prev.map((e) => e.seq));
  const additions = next.filter((e) => !seen.has(e.seq));
  if (additions.length === 0) return [...prev];
  return [...prev, ...additions].sort((a, b) => a.seq - b.seq);
}
