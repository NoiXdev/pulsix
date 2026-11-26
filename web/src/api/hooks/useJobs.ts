import { useEffect, useRef, useState } from 'react';
import { keepPreviousData, useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { AdhocInput, JobEvent } from '../types.ts';
import { invalidateRepoDerived } from './invalidateRepoDerived.ts';
import type { ServerPageQuery } from './paging.ts';

export const JOBS_PAGE_SIZE = 50;

/** How often a live run's detail is read again. */
export const JOB_POLL_MS = 2000;

export interface JobListFilter {
  status?: string;
  kind?: string;
  trigger?: string;
  /** Only the runs of this job template (a template id). */
  template?: string;
  /** Only the runs of the templates that use this playbook (a playbook id). */
  playbook?: string;
  /** Only the runs this schedule (or webhook trigger) started. */
  triggerRef?: string;
  /** Only the runs that targeted this host (a host id). */
  host?: string;
  /** Only the dry runs (true) or only the real runs (false); both when omitted. */
  checkMode?: boolean;
  /** Page size; JOBS_PAGE_SIZE when omitted. */
  limit?: number;
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
              trigger: filter.trigger ?? '',
              template_id: filter.template ?? '',
              playbook_id: filter.playbook || undefined,
              trigger_ref: filter.triggerRef ?? '',
              host_id: filter.host ?? '',
              check_mode: checkModeParam(filter.checkMode),
              cursor: pageParam || undefined,
              limit: filter.limit ?? JOBS_PAGE_SIZE,
            },
          },
        }),
      ),
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    enabled: !!org,
  });
}

type JobSort = 'created' | 'status' | 'kind' | 'trigger';

const checkModeParam = (v: boolean | undefined): 'true' | 'false' | undefined => (v === undefined ? undefined : v ? 'true' : 'false');

/** useNewestJob is the newest run matching the filter (null when none), one
 * row of the list; `refetchInterval` keeps it current while a caller
 * follows a live run. */
export function useNewestJob(org: string, filter: Omit<JobListFilter, 'limit'>, opts: { enabled?: boolean; refetchInterval?: number | false } = {}) {
  return useQuery({
    queryKey: ['jobs', org, 'newest', filter],
    queryFn: async () => {
      const body = await unwrap(
        api.GET('/api/v1/orgs/{org}/jobs', {
          params: {
            path: { org },
            query: {
              status: filter.status || undefined,
              kind: filter.kind || undefined,
              trigger: filter.trigger || undefined,
              template_id: filter.template || undefined,
              playbook_id: filter.playbook || undefined,
              trigger_ref: filter.triggerRef || undefined,
              host_id: filter.host || undefined,
              check_mode: checkModeParam(filter.checkMode),
              limit: 1,
            },
          },
        }),
      );
      return body.jobs?.[0] ?? null;
    },
    enabled: !!org && (opts.enabled ?? true),
    refetchInterval: opts.refetchInterval,
  });
}

/**
 * useJobsPage is the runs list one numbered page at a time (the API's page
 * mode, which also answers the total). The previous page stays on screen
 * while the next one loads. The key keeps the ['jobs', org] prefix the run
 * mutations invalidate. `status: 'active'` asks for the live runs (queued,
 * claimed, running, cancelling). `enabled` waits for the list's
 * preferences (useListView's `ready`).
 */
export function useJobsPage(org: string, filter: Omit<JobListFilter, 'limit'>, page: ServerPageQuery, opts: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: ['jobs', org, 'page', filter, page],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/orgs/{org}/jobs', {
          params: {
            path: { org },
            query: {
              status: filter.status || undefined,
              kind: filter.kind || undefined,
              trigger: filter.trigger || undefined,
              template_id: filter.template || undefined,
              playbook_id: filter.playbook || undefined,
              trigger_ref: filter.triggerRef || undefined,
              host_id: filter.host || undefined,
              check_mode: checkModeParam(filter.checkMode),
              page: page.page,
              per_page: page.per_page,
              sort: page.sort as JobSort | undefined,
              order: page.order,
            },
          },
        }),
      ),
    enabled: !!org && (opts.enabled ?? true),
    placeholderData: keepPreviousData,
  });
}

export function useJob(
  org: string,
  id: string,
  opts: { refetchInterval?: number | false | ((query: { state: { data?: unknown } }) => number | false | undefined) } = {},
) {
  const qc = useQueryClient();
  const query = useQuery({
    queryKey: ['job', org, id],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/jobs/{id}', { params: { path: { org, id } } })),
    enabled: !!org && !!id,
    // TanStack's own refetchInterval callback type; re-declared loosely so
    // the run detail page can stop polling once the job is terminal.
    refetchInterval: opts.refetchInterval as number | false | ((query: unknown) => number | false | undefined) | undefined,
  });
  // A repository sync that completes while it is watched changes what the
  // repository-derived queries show (the playbooks it found, the text it
  // stored): they refetch now, as they do when a sync is started.
  const status = query.data?.job.status;
  const kind = query.data?.job.kind;
  const seen = useRef<{ id: string; status: string | undefined }>({ id, status: undefined });
  useEffect(() => {
    const before = seen.current.id === id ? seen.current.status : undefined;
    seen.current = { id, status };
    if (kind === 'sync_repo' && before !== undefined && !isTerminal(before) && isTerminal(status)) {
      invalidateRepoDerived(qc, org);
    }
  }, [qc, org, id, kind, status]);
  return query;
}

function useInvalidateJobs(org: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ['jobs', org] });
    void qc.invalidateQueries({ queryKey: ['job', org] });
  };
}

/** useCancelJob cancels a run; the run's id travels with the call, so one
 * hook serves the run page and every row of the runs list. */
export function useCancelJob(org: string) {
  const invalidate = useInvalidateJobs(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.POST('/api/v1/orgs/{org}/jobs/{id}/cancel', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

/** fetchRetryEnrollment reads what a retry of the run's failed hosts
 * started now enrols the agent on (spec 2026-11-25 B). */
export function fetchRetryEnrollment(org: string, id: string) {
  return unwrap(api.GET('/api/v1/orgs/{org}/jobs/{id}/enrollment-preview', { params: { path: { org, id } } }));
}

/** useRetryFailedJobs re-runs a finished run's failed hosts (202, { jobs })
 * — one job per involved zone, sharing a new run group. `enrollHostIds`
 * are the hosts the person confirmed the retry enrols the agent on, for a
 * template with agent enrollment. */
export function useRetryFailedJobs(org: string, id: string) {
  const invalidate = useInvalidateJobs(org);
  return useMutation({
    mutationFn: (enrollHostIds?: string[]) =>
      unwrap(
        api.POST('/api/v1/orgs/{org}/jobs/{id}/retry-failed', {
          params: { path: { org, id } },
          body: enrollHostIds ? { enroll_host_ids: enrollHostIds } : undefined,
        }),
      ),
    onSuccess: invalidate,
  });
}

/** useDismissFollowUps drops the run's pending follow-ups ("Do not start
 * follow-ups"); the answer carries how many were dismissed. */
export function useDismissFollowUps(org: string, id: string) {
  const invalidate = useInvalidateJobs(org);
  return useMutation({
    mutationFn: () => unwrap(api.POST('/api/v1/orgs/{org}/jobs/{id}/follow-ups/dismiss', { params: { path: { org, id } } })),
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

/** EVENTS_PAGE is the most events the events route answers in one read. */
const EVENTS_PAGE = 1000;

/** fetchJobEvents fetches all of a run's events after a sequence number —
 * the poll fallback's and the seed's fetch primitive (a plain async
 * function, so an interval can drive it). The route answers at most
 * EVENTS_PAGE events a read, so it reads on from each full page's last
 * event until a page comes back short. */
export async function fetchJobEvents(org: string, id: string, after: number): Promise<JobEvent[]> {
  const all: JobEvent[] = [];
  let cursor = after;
  for (;;) {
    const body = await unwrap(
      api.GET('/api/v1/orgs/{org}/jobs/{id}/events', { params: { path: { org, id }, query: { after: cursor, limit: EVENTS_PAGE } } }),
    );
    const page = body.events ?? [];
    all.push(...page);
    const last = page.length > 0 ? page[page.length - 1].seq : cursor;
    if (page.length < EVENTS_PAGE || last <= cursor) return all;
    cursor = last;
  }
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
 *    ends the stream, and the hook stops connecting. Because the server
 *    may write that frame while events it has not yet sent are still
 *    buffered, the hook does ONE final history fetch before it reports
 *    the terminal status — see the status listener below.
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
        let frame: { status?: string };
        try {
          frame = JSON.parse((e as MessageEvent<string>).data) as { status?: string };
        } catch {
          return; // an unreadable status frame is ignored
        }
        const status = frame.status;
        if (!status) return;
        if (!isTerminal(status)) {
          patch((s) => ({ ...s, status }));
          return;
        }
        // The terminal frame ends the stream — and can end it on top of
        // events this client was never sent. writeJobStream's select
        // (internal/api/job_sse.go) does not order the event channel
        // against its status ticker, and the worker stores every event
        // before ReportResult flips the job terminal, so a backlog can
        // still be sitting in the subscriber's buffer when the tick wins
        // the race: the terminal status frame goes out and the server
        // returns without draining it. Those events are at the TAIL, so
        // no later frame ever exposes a seq gap and neither the gap
        // detector above nor the poll fallback ever engages — the log
        // would just silently end early, and the cancelled-run breakdown
        // (features/runs/status.ts) would read the missing closing events
        // as hosts interrupted mid-task. One last history fetch closes
        // it, and the status is applied together with what it brings
        // back, so nothing downstream sees a terminal run with a log that
        // is still short. (The drain belongs here, not in the server's
        // select: a server-side drain races a slow or stalled client's
        // own writes.)
        es.close();
        void fetchJobEvents(org, id, lastSeq.current)
          .then((tail) => {
            if (tail.length > 0) lastSeq.current = tail[tail.length - 1].seq;
            patch((s) => ({ ...s, status, events: tail.length > 0 ? mergeEvents(s.events, tail) : s.events }));
          })
          .catch(() => {
            // That one fetch failed; hand the tail to the poll fallback
            // rather than dropping it, exactly as every other error path
            // in this hook does.
            patch((s) => ({ ...s, status, transport: 'poll' }));
          });
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
