import type { ReactNode } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi } from '../../test/mockApi.ts';
import { useRevokeAgent } from './useAgents.ts';

describe('useRevokeAgent', () => {
  it('refreshes the agent views and the host the revoke changed', async () => {
    mockApi({ 'POST /api/v1/orgs/acme/agents/h1/revoke': {} });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const invalidated = vi.spyOn(qc, 'invalidateQueries');
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
    const { result } = renderHook(() => useRevokeAgent('acme'), { wrapper });

    await act(async () => {
      await result.current.mutateAsync('h1');
    });

    // The revoke clears the host's monitored capability: the host page's
    // own query (['host', org, id]) must not keep serving the old one.
    expect(invalidated).toHaveBeenCalledWith({ queryKey: ['host', 'acme'] });
    expect(invalidated).toHaveBeenCalledWith({ queryKey: ['hosts', 'acme'] });
    expect(invalidated).toHaveBeenCalledWith({ queryKey: ['host-agent', 'acme'] });
  });
});
