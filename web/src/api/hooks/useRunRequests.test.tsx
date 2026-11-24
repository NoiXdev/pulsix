import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { mockApi } from '../../test/mockApi.ts';
import {
  COUNT_ROUTE_RETRY_MS,
  PENDING_POLL_MS,
  usePendingRunRequestCount,
} from './useRunRequests.ts';

const COUNT = 'GET /api/v1/orgs/acme/run-requests/pending-count';
const LIST = 'GET /api/v1/orgs/acme/run-requests';
const notFound = () =>
  new Response(JSON.stringify({ detail: 'not found' }), { status: 404, headers: { 'Content-Type': 'application/json' } });

const badRequest = () =>
  new Response(JSON.stringify({ detail: 'invalid id' }), { status: 400, headers: { 'Content-Type': 'application/json' } });
const serverError = () =>
  new Response(JSON.stringify({ detail: 'boom' }), { status: 500, headers: { 'Content-Type': 'application/json' } });

function wrapper() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

describe('usePendingRunRequestCount', () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('asks only the list for 10 minutes after a 404, then the count route again', async () => {
    let counted = 0;
    let listed = 0;
    mockApi({
      [COUNT]: () => {
        counted += 1;
        return notFound();
      },
      [LIST]: () => {
        listed += 1;
        return { requests: [{ id: 'a' }, { id: 'b' }] };
      },
    });
    const { result } = renderHook(() => usePendingRunRequestCount('acme'), { wrapper: wrapper() });
    await waitFor(() => expect(result.current).toBe(2));
    expect([counted, listed]).toEqual([1, 1]);

    await vi.advanceTimersByTimeAsync(PENDING_POLL_MS);
    await waitFor(() => expect(listed).toBe(2));
    expect(counted).toBe(1);

    await vi.advanceTimersByTimeAsync(COUNT_ROUTE_RETRY_MS);
    await waitFor(() => expect(counted).toBe(2));
  });

  it('treats a 400 from an older manager like a missing route', async () => {
    let counted = 0;
    let listed = 0;
    mockApi({
      [COUNT]: () => {
        counted += 1;
        return badRequest();
      },
      [LIST]: () => {
        listed += 1;
        return { requests: [{ id: 'a' }] };
      },
    });
    const { result } = renderHook(() => usePendingRunRequestCount('acme'), { wrapper: wrapper() });
    await waitFor(() => expect(result.current).toBe(1));
    await vi.advanceTimersByTimeAsync(PENDING_POLL_MS);
    await waitFor(() => expect(listed).toBe(2));
    expect(counted).toBe(1);
  });

  it('does not remember a 500 and asks the count route again', async () => {
    let counted = 0;
    mockApi({
      [COUNT]: () => {
        counted += 1;
        return serverError();
      },
      [LIST]: { requests: [] },
    });
    const { result } = renderHook(() => usePendingRunRequestCount('acme'), { wrapper: wrapper() });
    await waitFor(() => expect(counted).toBe(1));
    expect(result.current).toBeUndefined();
    await vi.advanceTimersByTimeAsync(PENDING_POLL_MS);
    await waitFor(() => expect(counted).toBe(2));
  });

  it('clears the memory once the count route answers again', async () => {
    let counted = 0;
    let listed = 0;
    mockApi({
      [COUNT]: () => {
        counted += 1;
        return counted === 1 ? notFound() : counted === 2 ? { count: 7 } : notFound();
      },
      [LIST]: () => {
        listed += 1;
        return { requests: [] };
      },
    });
    const { result } = renderHook(() => usePendingRunRequestCount('acme'), { wrapper: wrapper() });
    await waitFor(() => expect(listed).toBe(1));
    // After the retry window the count route answers and the memory is cleared.
    await vi.advanceTimersByTimeAsync(COUNT_ROUTE_RETRY_MS);
    await waitFor(() => expect(result.current).toBe(7));
    // Cleared: the next poll asks the count route directly (404 again), not the list first.
    await vi.advanceTimersByTimeAsync(PENDING_POLL_MS);
    await waitFor(() => expect(counted).toBe(3));
  });
});
