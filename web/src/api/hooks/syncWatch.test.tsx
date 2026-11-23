import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { resetSessionCache } from '../../lib/session.ts';
import { SyncWatchProvider } from './syncWatch.tsx';
import { useSyncRepo } from './useRepos.ts';

/* A repository sync started anywhere is noticed finishing anywhere in the
 * tab: the watcher polls the job the start returned. */

const detail = (status: string) => ({ job: { id: 'j1', kind: 'sync_repo', status } });

function Starter() {
  const sync = useSyncRepo('acme');
  return <button onClick={() => sync.mutate('r1')}>Sync</button>;
}

function setup(jobRoute: (req: MockRequest) => unknown, opts: { maxWatchMs?: number; pollMs?: number } = {}) {
  const calls = { job: 0 };
  mockApi({
    'POST /api/v1/orgs/acme/repos/r1/sync': { job: { id: 'j1', status: 'queued' } },
    'GET /api/v1/orgs/acme/jobs/j1': (req: MockRequest) => (calls.job++, jobRoute(req)),
  });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: 1 } } });
  const invalidated: string[] = [];
  const original = qc.invalidateQueries.bind(qc);
  qc.invalidateQueries = ((f: { queryKey?: readonly unknown[] }) => {
    if (f?.queryKey?.[0] !== 'sync-watch') invalidated.push(String(f?.queryKey?.[0]));
    return original(f as never);
  }) as typeof qc.invalidateQueries;
  render(
    <QueryClientProvider client={qc}>
      <SyncWatchProvider pollMs={opts.pollMs ?? 10} maxWatchMs={opts.maxWatchMs}>
        <Starter />
      </SyncWatchProvider>
    </QueryClientProvider>,
  );
  return { calls, invalidated, qc };
}

const start = async () => {
  await userEvent.setup().click(screen.getByRole('button', { name: 'Sync' }));
};
const settle = (ms: number) => act(() => new Promise<void>((r) => setTimeout(r, ms)));

const boom = () => new Response(JSON.stringify({ status: 500, detail: 'boom' }), { status: 500, headers: { 'Content-Type': 'application/json' } });

afterEach(() => vi.useRealTimers());

const locked = () => new Response(JSON.stringify({ status: 403, detail: 'session_locked' }), { status: 403, headers: { 'Content-Type': 'application/json' } });

describe('SyncWatchProvider', () => {
  it('invalidates repos, playbooks, templates and jobs once when the started job turns terminal, then stops polling', async () => {
    let n = 0;
    const { calls, invalidated } = setup(() => detail(++n < 3 ? 'running' : 'succeeded'));
    await start();
    // The start itself invalidates the four once.
    await waitFor(() => expect(invalidated).toHaveLength(4));
    await waitFor(() => expect(invalidated).toHaveLength(8));
    expect([...invalidated.slice(4)].sort()).toEqual(['jobs', 'playbooks', 'repos', 'templates']);
    const seen = calls.job;
    await settle(80);
    expect(calls.job).toBe(seen);
    expect(invalidated).toHaveLength(8);
  });

  it('drops a job that errors three times and stops polling', async () => {
    const { calls, invalidated } = setup(() => new Response(JSON.stringify({ status: 500, detail: 'boom' }), { status: 500, headers: { 'Content-Type': 'application/json' } }));
    await start();
    await waitFor(() => expect(calls.job).toBe(3));
    await settle(80);
    expect(calls.job).toBe(3);
    expect(invalidated).toHaveLength(8);
  });

  it('keeps watching a job whose errors are scattered between good reads', async () => {
    let n = 0;
    const { calls, invalidated } = setup(() => (++n % 2 === 1 ? boom() : detail('running')));
    await start();
    await waitFor(() => expect(calls.job).toBeGreaterThanOrEqual(7));
    expect(invalidated).toHaveLength(4);
  });

  it('refreshes once anyway when it drops a job after three errors in a row', async () => {
    const { calls, invalidated } = setup(boom);
    await start();
    await waitFor(() => expect(invalidated).toHaveLength(8));
    expect([...invalidated.slice(4)].sort()).toEqual(['jobs', 'playbooks', 'repos', 'templates']);
    await settle(80);
    expect(calls.job).toBe(3);
    expect(invalidated).toHaveLength(8);
  });

  it('refreshes once anyway when the job is gone', async () => {
    const { invalidated } = setup(() => new Response(JSON.stringify({ status: 404, detail: 'gone' }), { status: 404, headers: { 'Content-Type': 'application/json' } }));
    await start();
    await waitFor(() => expect(invalidated).toHaveLength(8));
    await settle(80);
    expect(invalidated).toHaveLength(8);
  });
  it('does not count a locked session as a failure: it keeps the job, polls on slowly and finishes after the unlock', async () => {
    let n = 0;
    const { calls, invalidated } = setup(() => (++n <= 5 ? locked() : detail('succeeded')));
    await start();
    // Five locked answers would have dropped the job three times over.
    await waitFor(() => expect(calls.job).toBeGreaterThanOrEqual(6), { timeout: 3000 });
    await waitFor(() => expect(invalidated).toHaveLength(8), { timeout: 3000 });
    expect(calls.job).toBe(6);
  });

  it('polls a locked session more slowly than an unlocked one', async () => {
    const { calls } = setup(locked);
    await start();
    await waitFor(() => expect(calls.job).toBe(1));
    await settle(120);
    expect(calls.job).toBeLessThanOrEqual(4);
  });

  it('forgets pending syncs when the session cache is reset', async () => {
    const { calls, invalidated, qc } = setup(() => detail('running'));
    await start();
    await waitFor(() => expect(calls.job).toBeGreaterThanOrEqual(2));
    act(() => resetSessionCache(qc));
    await settle(30);
    const seen = calls.job;
    await settle(100);
    expect(calls.job).toBe(seen);
    // Dropped by the boundary, not by an end: nothing is refreshed.
    expect(invalidated).toHaveLength(4);
  });

  it('drops a job still running after the watch limit, with one refresh', async () => {
    const { calls, invalidated } = setup(() => detail('running'), { maxWatchMs: 60 });
    await start();
    await waitFor(() => expect(invalidated).toHaveLength(8));
    await settle(80);
    const seen = calls.job;
    await settle(80);
    expect(calls.job).toBe(seen);
    expect(invalidated).toHaveLength(8);
  });

  it('watches for 60 minutes by default (fake timers)', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { invalidated } = setup(() => detail('running'), { pollMs: 60_000 });
    fireEvent.click(screen.getByRole('button', { name: 'Sync' }));
    await vi.advanceTimersByTimeAsync(59 * 60_000);
    expect(invalidated).toHaveLength(4);
    await vi.advanceTimersByTimeAsync(2 * 60_000);
    expect(invalidated).toHaveLength(8);
  });
});
