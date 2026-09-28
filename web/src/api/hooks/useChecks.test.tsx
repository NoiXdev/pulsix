import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi } from '../../test/mockApi.ts';
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

  it('stops asking once the answer is final', async () => {
    let asked = 0;
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/metrics': () => {
        asked += 1;
        return new Response(JSON.stringify({ detail: 'host has no metric "usage_percent" for check "cpu"' }), { status: 404 });
      },
    });
    const { result } = renderHook(() => useHostMetric('acme', 'h1', SELECTION), { wrapper: wrapper() });

    await settle();
    expect(result.current.isError).toBe(true);
    // retry: false — one attempt, not four.
    expect(asked).toBe(1);

    // The interval timer does not consult the error state on its own: a
    // plain `refetchInterval: 30_000` would re-ask a 404 twice a minute,
    // forever, for an answer that cannot change — each round trip costing
    // a GetHost plus a CurrentForHost on the manager.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5 * 60_000);
    });
    expect(asked).toBe(1);
  });
});
