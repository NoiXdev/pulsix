import { useEffect, useRef, useState } from 'react';
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { AdhocInput, JobEvent } from '../types.ts';

export const JOBS_PAGE_SIZE = 50;

export interface JobListFilter {
  status?: string;
  kind?: string;
}

/** useJobs is the keyset-paginated runs list; the filters are part of the
 * query key (changing one starts over from the first page) and validated
 * server-side — an invalid value is a 400, so the UI's filter options must
 * stay the server's own vocabulary. */
export function useJobs(org: string, filter: JobListFilter = {}) {
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

/** useRunAdhocCommand starts an ad-hoc shell command on explicit hosts
 * (backlog 2.14, 202, { jobs }) — one job per zone the targets live in,
 * sharing a run group, the same shape a template run takes. */
export function useRunAdhocCommand(org: string) {
  const invalidate = useInvalidateJobs(org);
  return useMutation({
    mutationFn: (body: AdhocInput) => unwrap(api.POST('/api/v1/orgs/{org}/hosts/command', { params: { path: { org } }, body })),
    onSuccess: invalidate,
  });
}

const TERMINAL = new Set(['succeeded', 'failed', 'cancelled', 'error']);

export function isTerminal(status: string | undefined | null): boolean {
  return status !== undefined && status !== null && TERMINAL.has(status);
}

/** fetchJobEvents fetches one page of a run's events after a sequence
 * number — the poll fallback's and the seed's fetch primitive (a plain
 * async function, so an interval can drive it). */
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

interface RunLogState {
  org: string;
  id: string;
  events: JobEvent[];
  status: string | null;
  transport: RunLogTransport | null;
}

/**
 * useRunLog is the run view's live event log: it seeds the event history
 * with one plain fetch, then subscribes to the org-scoped SSE stream
 * (GET /orgs/{org}/jobs/{id}/stream). The SSE contract (Task 12) shapes
 * this hook:
 *
 *  - `event: log` frames carry the event JSON (seq, host, task, state,
 *    payload); the server may silently DROP frames under load
 *    (drop-on-full), so the client detects seq gaps — any event whose seq
 *    is greater than last+1 — closes the stream and falls back to polling
 *    GET /events?after=<seq>, which replays exactly what was lost.
 *  - `event: status` frames carry {id, status}; the terminal status frame
 *    ends the stream, and the hook stops connecting.
 *  - the server ends the stream on transient errors; EventSource
 *    reconnects by itself, and a reconnect's missed frames surface as a
 *    seq gap → the same polling fallback.
 *
 * State is keyed by (org, id): navigating to another run resets it
 * render-time (the "adjust state when a prop changes" pattern), and the
 * seed refetches for the new key.
 */
export function useRunLog(org: string, id: string): RunLog {
  const [state, setState] = useState<RunLogState>({ org, id, events: [], status: null, transport: null });
  if (state.org !== org || state.id !== id) {
    setState({ org, id, events: [], status: null, transport: null });
  }
  // lastSeq lives in a ref so the SSE handlers and the poll timer read the
  // freshest value without re-subscribing on every event.
  const lastSeq = useRef(0);

  // Seed the history once per key: everything already streamed before this
  // page opened (a late subscriber otherwise only sees the live tail).
  useEffect(() => {
    if (!org || !id) return;
    let alive = true;
    void fetchJobEvents(org, id, 0)
      .then((seed) => {
        if (!alive) return;
        const last = seed.length > 0 ? seed[seed.length - 1].seq : 0;
        lastSeq.current = last;
        setState((s) => (s.org === org && s.id === id ? { ...s, events: mergeEvents([], seed), transport: 'sse' } : s));
      })
      .catch(() => {
        if (alive) setState((s) => (s.org === org && s.id === id ? { ...s, transport: 'poll' } : s));
      });
    return () => {
      alive = false;
    };
  }, [org, id]);

  // The live tail. transport sse → EventSource; poll → interval fetch.
  useEffect(() => {
    if (!org || !id) return;
    if (state.transport === 'sse') {
      const es = new EventSource(`/api/v1/orgs/${org}/jobs/${id}/stream`);
      const patch = (next: (s: RunLogState) => RunLogState) => setState((s) => (s.org === org && s.id === id ? next(s) : s));

      es.addEventListener('log', (e) => {
        let ev: JobEvent;
        try {
          ev = JSON.parse((e as MessageEvent<string>).data) as JobEvent;
        } catch {
          return; // a frame we cannot read is not a gap
        }
        if (ev.seq <= lastSeq.current) return; // replay or duplicate
        if (ev.seq > lastSeq.current + 1) {
          // A gap: frames were dropped (drop-on-full) or missed across a
          // reconnect. Polling replays exactly what was lost.
          es.close();
          patch((s) => ({ ...s, transport: 'poll' }));
          return;
        }
        lastSeq.current = ev.seq;
        patch((s) => ({ ...s, events: mergeEvents(s.events, [ev]) }));
      });

      es.addEventListener('status', (e) => {
        try {
          const frame = JSON.parse((e as MessageEvent<string>).data) as { status?: string };
          if (frame.status) {
            if (isTerminal(frame.status)) es.close(); // terminal frame ends the stream
            patch((s) => ({ ...s, status: frame.status ?? null }));
          }
        } catch {
          /* an unreadable status frame is ignored */
        }
      });

      es.addEventListener('error', () => {
        // The server's error frame or a broken connection: polling answers
        // errors properly (404 and such), the stream does not.
        es.close();
        patch((s) => ({ ...s, transport: 'poll' }));
      });

      return () => es.close();
    }
    if (state.transport === 'poll') {
      const poll = async () => {
        try {
          const next = await fetchJobEvents(org, id, lastSeq.current);
          if (next.length > 0) {
            lastSeq.current = next[next.length - 1].seq;
            setState((s) => (s.org === org && s.id === id ? { ...s, events: mergeEvents(s.events, next) } : s));
          }
        } catch {
          // Transient: the next tick tries again.
        }
      };
      const timer = window.setInterval(() => void poll(), 1500);
      void poll();
      return () => window.clearInterval(timer);
    }
  }, [org, id, state.transport]);

  return { events: state.events, status: state.status, transport: state.transport };
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
