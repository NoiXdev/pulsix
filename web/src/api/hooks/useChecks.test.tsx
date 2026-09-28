import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi } from '../../test/mockApi.ts';
import { errorStatus } from '../../lib/apiError.ts';
import { useHostMetric, type MetricSelection } from './useChecks.ts';

const NOW = '2026-09-27T12:00:00Z';
const SELECTION: MetricSelection = { check: 'cpu', item: '', metric: 'usage_percent', range: '1h' };

function wrapper() {
  // retry: 3, so the hook's own retry: false is what decides the counts
  // below rather than a permissive client default.
  const qc = new QueryClient({ defaultOptions: { queries: { retry: 3 } } });
  return ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

/**
 * settle flushes the microtask queue the mocked fetch resolves on.
 * Testing Library's own waitFor cannot be used here: it detects *jest*
 * fake timers by looking for a `jest` global, finds none under Vitest, and
 * then polls on timers this test has frozen — so it hangs until the
 * suite's timeout rather than advancing anything.
 */
const settle = () => act(async () => void (await vi.advanceTimersByTimeAsync(10)));

/**
 * The polling contract, which no rendered test can see: every assertion
 * here needs minutes of wall clock to fail, so a card test that finishes
 * in a second would pass against any interval at all.
 */
describe('useHostMetric polling', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('follows the 30 s cadence while the series reads cleanly', async () => {
    let served = 0;
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/metrics': () => {
        served += 1;
        return { check: 'cpu', item: '', metric: 'usage_percent', range: '1h', from: NOW, to: NOW, points: [] };
      },
    });
    const { result } = renderHook(() => useHostMetric('acme', 'h1', SELECTION), { wrapper: wrapper() });

    await settle();
    expect(result.current.data).toBeDefined();
    expect(served).toBe(1);

    // An open graph must not freeze at the instant it was opened while the
    // values above it keep ticking.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(31_000);
    });
    expect(served).toBe(2);
  });

  /**
   * The refetch gate is a window — `status >= 400 && status < 500` — and a
   * window has three edges, every one of which a single character can move:
   *
   *  - `>= 400` → `> 400` lets a 400 (a discrete metric, an unknown range)
   *    poll forever for an answer that cannot change;
   *  - `< 500` → `<= 500` kills the graph on a 500, which is what a slow
   *    hypertable query answers, and is the exact bug this gate exists to
   *    prevent;
   *  - a stop on a null status kills it on a connection refused during a
   *    manager restart.
   *
   * 404 and 502 are interior to the window and pin none of those. Each row
   * below sits on an edge.
   *
   * The last row matters for a second reason: `errorStatus() === null` is
   * indistinguishable from the healthy case in the gate's own expression,
   * since `query.state.error` is null when nothing has failed either. So
   * the row asserts the query really is in an error state carrying no
   * status, not merely that polling continued.
   */
  const FAILURES: [name: string, respond: () => Response, keepsPolling: boolean][] = [
    ['400 — the request itself is wrong', () => new Response(JSON.stringify({ detail: 'range must be one of 1h, 24h, 7d' }), { status: 400 }), false],
    ['404 — this host never reported that tuple', () => new Response(JSON.stringify({ detail: 'host has no metric' }), { status: 404 }), false],
    ['500 — the manager faulted answering', () => new Response(JSON.stringify({ detail: 'query timed out' }), { status: 500 }), true],
    ['502 — a proxy between us during a restart', () => new Response(JSON.stringify({ detail: 'upstream unavailable' }), { status: 502 }), true],
  ];

  it.each(FAILURES)('%s', async (_name, respond, keepsPolling) => {
    let asked = 0;
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/metrics': () => {
        asked += 1;
        return respond();
      },
    });
    const { result } = renderHook(() => useHostMetric('acme', 'h1', SELECTION), { wrapper: wrapper() });

    await settle();
    expect(result.current.isError).toBe(true);
    // retry: false — one attempt, not four, whatever the client default.
    expect(asked).toBe(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(31_000);
    });
    expect(asked).toBe(keepsPolling ? 2 : 1);

    // A final answer must stay final, not merely be slow to repeat.
    if (!keepsPolling) {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5 * 60_000);
      });
      expect(asked).toBe(1);
    }
  });

  it('keeps polling when the request never reached a server at all', async () => {
    let asked = 0;
    // A rejection, not a response: connection refused while the manager
    // restarts. openapi-fetch never sees a status, so this surfaces as a
    // plain TypeError rather than one of our ApiErrors.
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/metrics': () => {
        asked += 1;
        throw new TypeError('Failed to fetch');
      },
    });
    const { result } = renderHook(() => useHostMetric('acme', 'h1', SELECTION), { wrapper: wrapper() });

    await settle();
    // The query is genuinely failed, and the failure genuinely carries no
    // status — which is what separates this row from the healthy case, the
    // two being identical to the gate's `query.state.error` check.
    expect(result.current.isError).toBe(true);
    expect(errorStatus(result.current.error)).toBeNull();
    expect(asked).toBe(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(31_000);
    });
    expect(asked).toBe(2);
  });
});
