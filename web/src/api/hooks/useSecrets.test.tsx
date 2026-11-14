import type { ReactNode } from 'react';
import { describe, expect, it } from 'vitest';
import { act, renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { usePutSecret, useSecrets } from './useSecrets.ts';
import { useCreateCheck, useDeleteCheck, useUpdateCheck } from './useChecks.ts';
import { useCreateCheckDefinition, useDeleteCheckDefinition, useUpdateCheckDefinition } from './useSharedCheckDefinitions.ts';

function setup() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  return { qc, wrapper };
}

const SECRET = { name: 'sw', field_names: ['community'], created_at: '', updated_at: '', used_by: [] };

describe('usePutSecret', () => {
  it('sends If-None-Match: * on a create and nothing on a replace', async () => {
    const headers: (string | null)[] = [];
    mockApi({
      'PUT /api/v1/orgs/acme/secrets/sw': (req: MockRequest) => {
        headers.push(req.headers.get('If-None-Match'));
        return { secret: SECRET };
      },
    });
    const { wrapper } = setup();
    const { result } = renderHook(() => usePutSecret('acme'), { wrapper });
    await act(() => result.current.mutateAsync({ name: 'sw', fields: { community: 'x' }, createOnly: true }));
    await act(() => result.current.mutateAsync({ name: 'sw', fields: { community: 'x' }, createOnly: false }));
    expect(headers).toEqual(['*', null]);
  });
});

/** "Used by" follows the check definitions: every write that can add or
 * drop a check naming a secret refreshes the list. */
describe('the secrets list after check writes', () => {
  const cases: [string, () => { mutateAsync: (v: never) => Promise<unknown> }, unknown][] = [
    ['creating a check', () => useCreateCheck('acme') as never, { key: 'snmp' }],
    ['changing a check', () => useUpdateCheck('acme', 'c1') as never, { key: 'snmp' }],
    ['deleting a check', () => useDeleteCheck('acme') as never, 'c1'],
    ['creating a shared check definition', () => useCreateCheckDefinition() as never, { key: 'snmp' }],
    ['changing a shared check definition', () => useUpdateCheckDefinition('d1') as never, { key: 'snmp' }],
    ['deleting a shared check definition', () => useDeleteCheckDefinition() as never, 'd1'],
  ];

  it.each(cases)('is refreshed after %s', async (_name, useWrite, variables) => {
    let lists = 0;
    mockApi({
      'GET /api/v1/orgs/acme/secrets': () => {
        lists += 1;
        return { secrets: [] };
      },
      'POST /api/v1/orgs/acme/checks': { check: {} },
      'PUT /api/v1/orgs/acme/checks/c1': { check: {} },
      'DELETE /api/v1/orgs/acme/checks/c1': {},
      'POST /api/v1/admin/checks/definitions': { definition: {} },
      'PUT /api/v1/admin/checks/definitions/d1': { definition: {} },
      'DELETE /api/v1/admin/checks/definitions/d1': {},
    });
    const { wrapper } = setup();
    const list = renderHook(() => useSecrets('acme'), { wrapper });
    await waitFor(() => expect(list.result.current.isSuccess).toBe(true));
    expect(lists).toBe(1);
    const write = renderHook(useWrite, { wrapper });
    await act(() => write.result.current.mutateAsync(variables as never));
    await waitFor(() => expect(lists).toBe(2));
  });
});
