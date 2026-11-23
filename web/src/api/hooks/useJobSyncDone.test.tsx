import type { ReactNode } from 'react';
import { describe, expect, it } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi } from '../../test/mockApi.ts';
import { useJob } from './useJobs.ts';
import { usePlaybookContent } from './usePlaybooks.ts';
import { useRepos } from './useRepos.ts';

/* A repository sync that completes changes what the repository-derived
 * queries show (backlog 3.130): they refetch then, as they do when one is
 * started. */

const job = (kind: string, status: string) => ({ job: { id: 'j1', kind, status, zone_id: 'z', run_group_id: 'g', trigger: 'manual', check_mode: false, hosts: [], stats: {}, created_at: '', group: [] } });
const TEXT = { path: 'a.yml', commit: 'c', synced_at: '2026-11-20T10:00:00Z', content: '- hosts: all\n', from_earlier_sync: false };

async function setup(kind: string, statuses: string[]) {
  let polls = 0;
  let content = 0;
  mockApi({
    'GET /api/v1/orgs/acme/jobs/j1': () => job(kind, statuses[Math.min(polls++, statuses.length - 1)]),
    'GET /api/v1/orgs/acme/playbooks/p1/content': () => (content++, TEXT),
    'GET /api/v1/orgs/acme/repos': { repos: [] },
  });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  const { result } = renderHook(() => ({ job: useJob('acme', 'j1'), text: usePlaybookContent('acme', 'p1', true), repos: useRepos('acme') }), { wrapper });
  await waitFor(() => expect(result.current.job.data && result.current.text.data && result.current.repos.data).toBeTruthy());
  return { result, qc, contents: () => content };
}

describe('useJob on a repository sync', () => {
  it('refetches the repository-derived queries when the sync completes', async () => {
    const { result, contents } = await setup('sync_repo', ['running', 'succeeded']);
    expect(contents()).toBe(1);
    await result.current.job.refetch();
    await waitFor(() => expect(contents()).toBe(2));
  });

  it('does so once, not on every poll of a finished sync', async () => {
    const { result, contents } = await setup('sync_repo', ['succeeded']);
    await result.current.job.refetch();
    await result.current.job.refetch();
    expect(contents()).toBe(1);
  });

  it('leaves other kinds of run alone', async () => {
    const { result, contents } = await setup('run', ['running', 'succeeded']);
    await result.current.job.refetch();
    await waitFor(() => expect(result.current.job.data?.job.status).toBe('succeeded'));
    expect(contents()).toBe(1);
  });
});
