import type { ReactNode } from 'react';
import { describe, expect, it } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi } from '../../test/mockApi.ts';
import { useHostNames } from './useHosts.ts';

function wrapper() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

const idsOf = (n: number) => Array.from({ length: n }, (_, i) => `h${i}`);

function listRoute(calls: URLSearchParams[]) {
  return ({ search }: { search: URLSearchParams }) => {
    calls.push(search);
    // h3 does not exist: it is simply absent from the answer.
    return { hosts: search.getAll('id').filter((id) => id !== 'h3').map((id) => ({ id, name: `name-${id}` })), next_cursor: '' };
  };
}

describe('useHostNames', () => {
  it('names up to 100 hosts with one list request, an absent host keeping its id', async () => {
    const calls: URLSearchParams[] = [];
    mockApi({ 'GET /api/v1/orgs/acme/hosts': listRoute(calls) });
    const ids = idsOf(100);
    const { result } = renderHook(() => useHostNames('acme', ids), { wrapper: wrapper() });

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(calls).toHaveLength(1);
    expect(calls[0].getAll('id')).toEqual(ids);
    expect(result.current.hosts[0]).toEqual({ id: 'h0', name: 'name-h0' });
    expect(result.current.hosts[3]).toEqual({ id: 'h3', name: 'h3' });
  });

  it('splits 101 hosts into two requests', async () => {
    const calls: URLSearchParams[] = [];
    mockApi({ 'GET /api/v1/orgs/acme/hosts': listRoute(calls) });
    const ids = idsOf(101);
    const { result } = renderHook(() => useHostNames('acme', ids), { wrapper: wrapper() });

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(calls.map((c) => c.getAll('id').length)).toEqual([100, 1]);
    expect(result.current.hosts[100].name).toBe('name-h100');
  });

  it('reads 500 hosts in five requests', async () => {
    const calls: URLSearchParams[] = [];
    mockApi({ 'GET /api/v1/orgs/acme/hosts': listRoute(calls) });
    const ids = idsOf(500);
    const { result } = renderHook(() => useHostNames('acme', ids), { wrapper: wrapper() });

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(calls).toHaveLength(5);
    expect(result.current.hosts[499].name).toBe('name-h499');
  });

  it('keeps the ids of a chunk that failed and the names of the others', async () => {
    const ids = idsOf(150);
    mockApi({
      'GET /api/v1/orgs/acme/hosts': ({ search }: { search: URLSearchParams }) =>
        search.getAll('id').includes('h100')
          ? new Response(JSON.stringify({ detail: 'boom' }), { status: 500, headers: { 'Content-Type': 'application/json' } })
          : { hosts: search.getAll('id').map((id) => ({ id, name: `name-${id}` })), next_cursor: '' },
    });
    const { result } = renderHook(() => useHostNames('acme', ids), { wrapper: wrapper() });

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.hosts[0].name).toBe('name-h0');
    expect(result.current.hosts[100].name).toBe('h100');
    expect(result.current.hosts[149].name).toBe('h149');
  });
});
