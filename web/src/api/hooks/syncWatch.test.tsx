import { describe, expect, it } from 'vitest';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { SyncWatchProvider } from './syncWatch.tsx';
import { useSyncRepo } from './useRepos.ts';

/* A repository sync started anywhere is noticed finishing anywhere in the
 * tab: the watcher polls the job the start returned. */

const detail = (status: string) => ({ job: { id: 'j1', kind: 'sync_repo', status } });

function Starter() {
  const sync = useSyncRepo('acme');
  return <button onClick={() => sync.mutate('r1')}>Sync</button>;
}

function setup(jobRoute: (req: MockRequest) => unknown) {
  const calls = { job: 0 };
  mockApi({
    'POST /api/v1/orgs/acme/repos/r1/sync': { job: { id: 'j1', status: 'queued' } },
    'GET /api/v1/orgs/acme/jobs/j1': (req: MockRequest) => (calls.job++, jobRoute(req)),
  });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const invalidated: string[] = [];
  const original = qc.invalidateQueries.bind(qc);
  qc.invalidateQueries = ((f: { queryKey?: readonly unknown[] }) => {
    if (f?.queryKey?.[0] !== 'sync-watch') invalidated.push(String(f?.queryKey?.[0]));
    return original(f as never);
  }) as typeof qc.invalidateQueries;
  render(
    <QueryClientProvider client={qc}>
      <SyncWatchProvider pollMs={10}>
        <Starter />
      </SyncWatchProvider>
    </QueryClientProvider>,
  );
  return { calls, invalidated };
}

const start = async () => {
  await userEvent.setup().click(screen.getByRole('button', { name: 'Sync' }));
};
const settle = (ms: number) => act(() => new Promise<void>((r) => setTimeout(r, ms)));

const boom = () => new Response(JSON.stringify({ status: 500, detail: 'boom' }), { status: 500, headers: { 'Content-Type': 'application/json' } });

describe('SyncWatchProvider', () => {
  it('invalidates repos, playbooks and jobs once when the started job turns terminal, then stops polling', async () => {
    let n = 0;
    const { calls, invalidated } = setup(() => detail(++n < 3 ? 'running' : 'succeeded'));
    await start();
    // The start itself invalidates the three once.
    await waitFor(() => expect(invalidated).toHaveLength(3));
    await waitFor(() => expect(invalidated).toHaveLength(6));
    expect([...invalidated.slice(3)].sort()).toEqual(['jobs', 'playbooks', 'repos']);
    const seen = calls.job;
    await settle(80);
    expect(calls.job).toBe(seen);
    expect(invalidated).toHaveLength(6);
  });

  it('drops a job that errors three times and stops polling', async () => {
    const { calls, invalidated } = setup(() => new Response(JSON.stringify({ status: 500, detail: 'boom' }), { status: 500, headers: { 'Content-Type': 'application/json' } }));
    await start();
    await waitFor(() => expect(calls.job).toBe(3));
    await settle(80);
    expect(calls.job).toBe(3);
    expect(invalidated).toHaveLength(6);
  });

  it('keeps watching a job whose errors are scattered between good reads', async () => {
    let n = 0;
    const { calls, invalidated } = setup(() => (++n % 2 === 1 ? boom() : detail('running')));
    await start();
    await waitFor(() => expect(calls.job).toBeGreaterThanOrEqual(7));
    expect(invalidated).toHaveLength(3);
  });

  it('refreshes once anyway when it drops a job after three errors in a row', async () => {
    const { calls, invalidated } = setup(boom);
    await start();
    await waitFor(() => expect(invalidated).toHaveLength(6));
    expect([...invalidated.slice(3)].sort()).toEqual(['jobs', 'playbooks', 'repos']);
    await settle(80);
    expect(calls.job).toBe(3);
    expect(invalidated).toHaveLength(6);
  });

  it('refreshes once anyway when the job is gone', async () => {
    const { invalidated } = setup(() => new Response(JSON.stringify({ status: 404, detail: 'gone' }), { status: 404, headers: { 'Content-Type': 'application/json' } }));
    await start();
    await waitFor(() => expect(invalidated).toHaveLength(6));
    await settle(80);
    expect(invalidated).toHaveLength(6);
  });
});
