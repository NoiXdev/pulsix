import type { ReactNode } from 'react';
import { describe, expect, it } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi } from '../../test/mockApi.ts';
import { useGroup, useGroups, useReloadInventory } from './useInventory.ts';

const failure = () => new Response(JSON.stringify({ status: 500, detail: 'boom' }), { status: 500, headers: { 'Content-Type': 'application/json' } });

const group = (revision: number) => ({
  group: {
    id: 'g1', name: 'web', description: '', parent_id: null, revision, schema: { fields: [] },
    vars: {}, problems: [], orphaned: [], created_at: '', updated_at: '',
  },
});

const GROUPS = { groups: [{ id: 'g1', name: 'web', description: '', parent_id: null, members: 0, group_fields: 0, host_fields: 0, complete: true, revision: 3 }] };

/** renderReload mounts what a group page reads (the group and the
 * organization's group list), so the reload's invalidation has active
 * queries to refetch, and returns the reload function. */
async function renderReload() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  const { result } = renderHook(
    () => ({ group: useGroup('acme', 'g1'), groups: useGroups('acme'), reload: useReloadInventory('acme') }),
    { wrapper },
  );
  await waitFor(() => expect(result.current.group.data && result.current.groups.data).toBeTruthy());
  return result.current.reload;
}

/** useReloadInventory answers a 412: it resolves to the stored group a form
 * starts over from. Only that group's own read decides; the rest of the
 * inventory is refreshed alongside, and its failure is not the form's. */
describe('useReloadInventory', () => {
  it('resolves the fresh group even when the groups list fails to refetch', async () => {
    let current = group(3);
    let groupsFail = false;
    mockApi({
      'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
      'GET /api/v1/orgs/acme/inventory/groups': () => (groupsFail ? failure() : GROUPS),
    });
    const reload = await renderReload();

    current = group(4);
    groupsFail = true;
    await expect(reload('g1')).resolves.toMatchObject({ id: 'g1', revision: 4 });
  });

  it('rejects when the group itself fails to load', async () => {
    let groupFails = false;
    mockApi({
      'GET /api/v1/orgs/acme/inventory/groups/g1': () => (groupFails ? failure() : group(3)),
      'GET /api/v1/orgs/acme/inventory/groups': GROUPS,
    });
    const reload = await renderReload();

    groupFails = true;
    await expect(reload('g1')).rejects.toThrow();
  });
});
