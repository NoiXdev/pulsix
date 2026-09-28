import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi } from '../../test/mockApi.ts';
import { errorStatus } from '../../lib/apiError.ts';
import {
  useChecks,
  useDeleteCheck,
  useHostChecks,
  useHostMetric,
  useMonitoringGroupPreview,
  useMonitoringGroups,
  type MetricSelection,
} from './useChecks.ts';

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

/**
 * The host card's own cadence. Like the series above it, this is invisible
 * to any rendered test: a card test finishes in a second and would pass
 * against a hook that never refetches at all — which is exactly what a
 * monitoring card must not be, since its whole subject is the current
 * reading.
 */
describe('useHostChecks polling', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('re-reads the effective set on the agent’s own 30 s heartbeat cadence', async () => {
    let served = 0;
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': () => {
        served += 1;
        return { host_id: 'h1', monitored: true, checks: [] };
      },
    });
    const { result } = renderHook(() => useHostChecks('acme', 'h1'), { wrapper: wrapper() });

    await settle();
    expect(result.current.data).toBeDefined();
    expect(served).toBe(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(31_000);
    });
    expect(served).toBe(2);

    // And it keeps going: a value that moves once and then stops is a
    // screenshot with extra steps.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(31_000);
    });
    expect(served).toBe(3);
  });

  it('asks for nothing at all when the caller may not read checks', async () => {
    let served = 0;
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': () => {
        served += 1;
        return { host_id: 'h1', monitored: true, checks: [] };
      },
    });
    renderHook(() => useHostChecks('acme', 'h1', false), { wrapper: wrapper() });

    await settle();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(61_000);
    });
    expect(served).toBe(0);
  });
});

/**
 * Definitions and monitoring groups are one subject with two lists, and a
 * write to either moves both: deleting a group cascades away the
 * definitions bound to it (migration 00043's ON DELETE CASCADE), and
 * editing a group's selector changes which hosts a definition bound to it
 * reaches. Refreshing only the list that was written leaves the other page
 * showing rows the server no longer has — and nothing else in the suite
 * notices, because each page renders one list.
 */
describe('check writes refresh both lists', () => {
  it('invalidates the monitoring groups after a definition is deleted', async () => {
    let checksServed = 0;
    let groupsServed = 0;
    mockApi({
      'GET /api/v1/orgs/acme/checks': () => {
        checksServed += 1;
        return { checks: [] };
      },
      'GET /api/v1/orgs/acme/monitoring-groups': () => {
        groupsServed += 1;
        return { groups: [] };
      },
      'DELETE /api/v1/orgs/acme/checks/c1': {},
    });
    const { result } = renderHook(
      () => ({ checks: useChecks('acme'), groups: useMonitoringGroups('acme'), remove: useDeleteCheck('acme') }),
      { wrapper: wrapper() },
    );

    await waitFor(() => expect(result.current.checks.data).toBeDefined());
    await waitFor(() => expect(result.current.groups.data).toBeDefined());
    expect(checksServed).toBe(1);
    expect(groupsServed).toBe(1);

    await act(async () => {
      await result.current.remove.mutateAsync('c1');
    });

    await waitFor(() => expect(checksServed).toBe(2));
    await waitFor(() => expect(groupsServed).toBe(2));
  });
});

/**
 * The selector preview's failure is the parser's own message about where
 * the expression went wrong, and it is final: asking again three times
 * only delays showing the operator what is wrong. The page test cannot see
 * this — its QueryClient already disables retry, so the hook's own
 * `retry: false` decides nothing there.
 */
describe('useMonitoringGroupPreview', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('shows the parser’s refusal once, against a client that would retry', async () => {
    let asked = 0;
    mockApi({
      'POST /api/v1/orgs/acme/monitoring-groups/preview': () => {
        asked += 1;
        return new Response(JSON.stringify({ detail: 'selector: unexpected token at position 7' }), { status: 400 });
      },
    });
    const { result } = renderHook(() => useMonitoringGroupPreview('acme', 'role=web and'), { wrapper: wrapper() });

    await settle();
    expect(result.current.isError).toBe(true);
    expect(asked).toBe(1);

    // Past every backoff the client's retry: 3 would have used.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(asked).toBe(1);
  });

  it('asks nothing for an empty selector', async () => {
    let asked = 0;
    mockApi({
      'POST /api/v1/orgs/acme/monitoring-groups/preview': () => {
        asked += 1;
        return { hosts: [] };
      },
    });
    renderHook(() => useMonitoringGroupPreview('acme', null), { wrapper: wrapper() });

    await settle();
    expect(asked).toBe(0);
  });
});
