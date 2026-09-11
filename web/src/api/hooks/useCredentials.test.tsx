import type { ReactNode } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi } from '../../test/mockApi.ts';
import { useCreateCredential, useDeleteCredential, useUpdateCredential } from './useCredentials.ts';

const CRED = { credential: { id: 'c1', name: 'deploy', kind: 'ssh_key', username: 'ansible' }, snippets: {} };

function setup() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const invalidated = vi.spyOn(qc, 'invalidateQueries');
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  return { invalidated, wrapper };
}

// A credential's username is the preview's ansible_user wherever a host
// names none, so every credential write must refresh the inventory too.
describe('credential mutations', () => {
  const cases: [string, () => () => Promise<unknown>][] = [
    ['create', () => {
      const m = useCreateCredential('acme');
      return () => m.mutateAsync({ name: 'deploy', kind: 'ssh_key', generate: true });
    }],
    ['update', () => {
      const m = useUpdateCredential('acme', 'c1');
      return () => m.mutateAsync({ name: 'deploy', username: 'root' });
    }],
    ['delete', () => {
      const m = useDeleteCredential('acme');
      return () => m.mutateAsync('c1');
    }],
  ];
  it.each(cases)('%s invalidates the inventory', async (_name, hook) => {
    mockApi({
      'POST /api/v1/orgs/acme/credentials': CRED,
      'PATCH /api/v1/orgs/acme/credentials/c1': CRED,
      'DELETE /api/v1/orgs/acme/credentials/c1': {},
    });
    const { invalidated, wrapper } = setup();
    const { result } = renderHook(hook, { wrapper });
    await act(async () => {
      await result.current();
    });
    expect(invalidated).toHaveBeenCalledWith({ queryKey: ['inventory', 'acme'] });
    expect(invalidated).toHaveBeenCalledWith({ queryKey: ['credentials', 'acme'] });
  });
});
