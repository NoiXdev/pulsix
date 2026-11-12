import type { ReactNode } from 'react';
import { describe, expect, it } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi } from '../../test/mockApi.ts';
import fixture from '../../test/checkPlugins.json';
import { useCheckPlugins } from './useCheckPlugins.ts';

function wrapper(qc: QueryClient) {
  return ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

describe('useCheckPlugins', () => {
  it('reads the descriptors once and keeps them: they never change while the manager runs', async () => {
    let served = 0;
    mockApi({
      'GET /api/v1/check-plugins': () => {
        served += 1;
        return fixture;
      },
    });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const first = renderHook(() => useCheckPlugins(), { wrapper: wrapper(qc) });
    await waitFor(() => expect(first.result.current.data).toBeDefined());
    expect(first.result.current.data?.map((p) => p.name)).toEqual(fixture.plugins.map((p) => p.name));
    first.unmount();

    // A second form opened later reads the cache, not the network.
    const second = renderHook(() => useCheckPlugins(), { wrapper: wrapper(qc) });
    expect(second.result.current.data).toBeDefined();
    await qc.refetchQueries({ stale: true });
    expect(served).toBe(1);
  });

  it('asks for nothing while disabled', async () => {
    let served = 0;
    mockApi({
      'GET /api/v1/check-plugins': () => {
        served += 1;
        return fixture;
      },
    });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { result } = renderHook(() => useCheckPlugins({ enabled: false }), { wrapper: wrapper(qc) });
    expect(result.current.data).toBeUndefined();
    expect(served).toBe(0);
  });

  it('reports a failed read', async () => {
    mockApi({
      'GET /api/v1/check-plugins': () => new Response(JSON.stringify({ detail: 'boom' }), { status: 500 }),
    });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { result } = renderHook(() => useCheckPlugins(), { wrapper: wrapper(qc) });
    await waitFor(() => expect(result.current.isError).toBe(true));
  });
});
