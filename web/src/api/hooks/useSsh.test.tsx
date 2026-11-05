import type { ReactNode } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import type { Host } from '../types.ts';
import { useSetHostSSH } from './useSsh.ts';

const HOST = { id: 'h1', name: 'web-01', zone_id: 'z', address: '', managed: true, monitored: false, facts: {}, revision: 3, created_at: '' } as Host;

describe('useSetHostSSH', () => {
  it('puts the new revision into the cached host, so the next write sends it', async () => {
    const sent: Array<string | null> = [];
    let revision = 3;
    mockApi({
      'PUT /api/v1/orgs/acme/hosts/h1/ssh': (req: MockRequest) => {
        sent.push(req.headers.get('If-Match'));
        revision += 1;
        return { ssh: { host_id: 'h1', port: 22, username: 'ops', bastion_mode: 'inherit' }, host_revision: revision };
      },
    });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    qc.setQueryData(['host', 'acme', 'h1'], { host: HOST });
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
    const { result } = renderHook(() => useSetHostSSH('acme', 'h1'), { wrapper });
    const cachedRevision = () => qc.getQueryData<{ host: Host }>(['host', 'acme', 'h1'])?.host.revision;

    await act(async () => {
      await result.current.mutateAsync({ body: { username: 'ops' }, revision: cachedRevision()! });
    });
    expect(cachedRevision()).toBe(4);

    await act(async () => {
      await result.current.mutateAsync({ body: { username: 'deploy' }, revision: cachedRevision()! });
    });
    expect(sent).toEqual(['"3"', '"4"']);
    expect(cachedRevision()).toBe(5);
  });

  it('pins the PUT response ssh into the host ssh cache and refreshes the host lists', async () => {
    const ssh = { host_id: 'h1', port: 2222, username: 'ops', bastion_mode: 'none' };
    mockApi({ 'PUT /api/v1/orgs/acme/hosts/h1/ssh': { ssh, host_revision: 4 } });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    qc.setQueryData(['host', 'acme', 'h1'], { host: HOST });
    qc.setQueryData(['ssh', 'acme', 'host', 'h1'], { ssh: { host_id: 'h1', port: 22, username: 'old', bastion_mode: 'inherit' } });
    const invalidated: unknown[] = [];
    const spy = vi.spyOn(qc, 'invalidateQueries').mockImplementation(async (f) => {
      invalidated.push(f?.queryKey);
    });
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
    const { result } = renderHook(() => useSetHostSSH('acme', 'h1'), { wrapper });

    await act(async () => {
      await result.current.mutateAsync({ body: { port: 2222, username: 'ops', bastion_mode: 'none' }, revision: 3 });
    });

    expect(qc.getQueryData<{ ssh: unknown }>(['ssh', 'acme', 'host', 'h1'])?.ssh).toEqual(ssh);
    expect(qc.getQueryData<{ host: Host }>(['host', 'acme', 'h1'])?.host.revision).toBe(4);
    expect(invalidated).toContainEqual(['hosts', 'acme']);
    spy.mockRestore();
  });
});
