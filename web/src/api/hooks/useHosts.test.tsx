import type { ReactNode } from 'react';
import { describe, expect, it } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi } from '../../test/mockApi.ts';
import type { Host } from '../types.ts';
import { useDeleteHost, useSetCapabilitiesOfHost, useUpdateHost } from './useHosts.ts';

const HOST = { id: 'h1', name: 'web-01', zone_id: 'z', address: '', managed: true, monitored: false, facts: {}, revision: 3, created_at: '' } as Host;

function setup() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(['host', 'acme', 'h1'], { host: HOST });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  const cached = () => qc.getQueryData<{ host: Host }>(['host', 'acme', 'h1'])?.host;
  return { qc, wrapper, cached };
}

describe('host writes and the host names a form read', () => {
  it('invalidate the names useHostNames cached', async () => {
    mockApi({ 'PATCH /api/v1/orgs/acme/hosts/h1': { host: { ...HOST, name: 'web-02', revision: 4 } } });
    const { qc, wrapper } = setup();
    qc.setQueryData(['host-names', 'acme', ['h1']], ['web-01']);
    const { result } = renderHook(() => useUpdateHost('acme', 'h1'), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({ name: 'web-02', zone_id: 'z', address: '', revision: 3 });
    });
    expect(qc.getQueryState(['host-names', 'acme', ['h1']])?.isInvalidated).toBe(true);
  });
});

describe('useDeleteHost', () => {
  it('invalidates the hosts and the names useHostNames cached', async () => {
    mockApi({ 'DELETE /api/v1/orgs/acme/hosts/h1': {} });
    const { qc, wrapper } = setup();
    qc.setQueryData(['host-names', 'acme', ['h1']], ['web-01']);
    qc.setQueryData(['hosts', 'acme'], { hosts: [] });
    const { result } = renderHook(() => useDeleteHost('acme'), { wrapper });
    await act(async () => {
      await result.current.mutateAsync('h1');
    });
    expect(qc.getQueryState(['hosts', 'acme'])?.isInvalidated).toBe(true);
    expect(qc.getQueryState(['host-names', 'acme', ['h1']])?.isInvalidated).toBe(true);
  });
});

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

  it.each([
    ['tags', { tags: [{ key: 'env', value: 'prod' }] }],
    ['the jump host', { jump: { mode: 'none' as const } }],
  ])('keeps the old revision cached when the save also wrote %s, so a sidebar write built from not yet refetched data answers 412 instead of undoing it', async (_what, extra) => {
    mockApi({ 'PATCH /api/v1/orgs/acme/hosts/h1': { host: { ...HOST, revision: 4 } } });
    const { wrapper, cached } = setup();
    const { result } = renderHook(() => useUpdateHost('acme', 'h1'), { wrapper });

    await act(async () => {
      await result.current.mutateAsync({ name: 'web-01', zone_id: 'z', address: '', revision: 3, ...extra });
    });

    expect(cached()?.revision).toBe(3);
  });
});

describe('useSetCapabilitiesOfHost', () => {
  it('puts the PATCH response host into the cached host', async () => {
    mockApi({ 'PATCH /api/v1/orgs/acme/hosts/h1/capabilities': { host: { ...HOST, monitored: true, revision: 4 } } });
    const { wrapper, cached } = setup();
    const { result } = renderHook(() => useSetCapabilitiesOfHost('acme'), { wrapper });

    await act(async () => {
      await result.current.mutateAsync({ id: 'h1', patch: { monitored: true } });
    });

    expect(cached()?.revision).toBe(4);
    expect(cached()?.monitored).toBe(true);
  });
});
