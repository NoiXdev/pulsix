import type { ReactNode } from 'react';
import { describe, expect, it } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import type { Host } from '../types.ts';
import { useSetHostTags } from './useTags.ts';

const HOST = { id: 'h1', name: 'web-01', zone_id: 'z', address: '', managed: true, monitored: false, facts: {}, revision: 3, created_at: '' } as Host;

describe('useSetHostTags', () => {
  it('puts the new revision into the cached host, so the next write sends it', async () => {
    const sent: Array<string | null> = [];
    let revision = 3;
    mockApi({
      'PUT /api/v1/orgs/acme/hosts/h1/tags': (req: MockRequest) => {
        sent.push(req.headers.get('If-Match'));
        revision += 1;
        return { tags: [], host_revision: revision };
      },
    });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    qc.setQueryData(['host', 'acme', 'h1'], { host: HOST });
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
    const { result } = renderHook(() => useSetHostTags('acme', 'h1'), { wrapper });
    const cachedRevision = () => qc.getQueryData<{ host: Host }>(['host', 'acme', 'h1'])?.host.revision;

    await act(async () => {
      await result.current.mutateAsync({ tags: [{ key: 'env', value: 'prod' }], revision: cachedRevision()! });
    });
    expect(cachedRevision()).toBe(4);

    await act(async () => {
      await result.current.mutateAsync({ tags: [], revision: cachedRevision()! });
    });
    expect(sent).toEqual(['"3"', '"4"']);
    expect(cachedRevision()).toBe(5);
  });
});
