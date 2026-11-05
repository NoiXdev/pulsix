import type { ReactNode } from 'react';
import { describe, expect, it } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi } from '../../test/mockApi.ts';
import type { Host } from '../types.ts';
import { useSetHostCapabilities, useUpdateHost } from './useHosts.ts';

const HOST = { id: 'h1', name: 'web-01', zone_id: 'z', address: '', managed: true, monitored: false, facts: {}, revision: 3, created_at: '' } as Host;

function setup() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(['host', 'acme', 'h1'], { host: HOST });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  const cached = () => qc.getQueryData<{ host: Host }>(['host', 'acme', 'h1'])?.host;
  return { qc, wrapper, cached };
}

describe('useUpdateHost', () => {
  it('puts the PATCH response host into the cached host, so a following tag or SSH write sends the new revision', async () => {
    mockApi({ 'PATCH /api/v1/orgs/acme/hosts/h1': { host: { ...HOST, name: 'web-02', revision: 4 } } });
    const { wrapper, cached } = setup();
    const { result } = renderHook(() => useUpdateHost('acme', 'h1'), { wrapper });

    await act(async () => {
      await result.current.mutateAsync({ name: 'web-02', zone_id: 'z', address: '', revision: 3 });
    });

    expect(cached()?.revision).toBe(4);
    expect(cached()?.name).toBe('web-02');
  });
});

describe('useSetHostCapabilities', () => {
  it('puts the PATCH response host into the cached host', async () => {
    mockApi({ 'PATCH /api/v1/orgs/acme/hosts/h1/capabilities': { host: { ...HOST, monitored: true, revision: 4 } } });
    const { wrapper, cached } = setup();
    const { result } = renderHook(() => useSetHostCapabilities('acme', 'h1'), { wrapper });

    await act(async () => {
      await result.current.mutateAsync({ monitored: true });
    });

    expect(cached()?.revision).toBe(4);
    expect(cached()?.monitored).toBe(true);
  });
});
