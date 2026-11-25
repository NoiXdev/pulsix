import { afterEach, describe, expect, it, vi } from 'vitest';
import { QueryClient } from '@tanstack/react-query';
import { screen, within } from '@testing-library/react';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import { firstCells } from '../../test/wholeList.ts';
import { RunRequestDetailPage } from './RunRequestDetailPage.tsx';
import { RunRequestsPage } from './RunRequestsPage.tsx';
import { MEMBERS, RQ, detail, orgAs, request } from './runRequestTestKit.ts';
import { validateRunRequestsSearch } from './runRequestsSearch.ts';

const SYNC = '0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f9999';

function renderDetail(lang?: 'de') {
  return renderRoutes(
    [
      { path: '/runs/$id', component: () => <div>RUN PAGE</div> },
      { path: '/runs/requests/$id', component: RunRequestDetailPage },
    ],
    `/o/acme/runs/requests/${RQ}`,
    lang ? new QueryClient({ defaultOptions: { queries: { retry: false } } }) : undefined,
    lang,
  );
}

function mockDetail(req: Record<string, unknown>, role = 'operator') {
  mockApi({
    'GET /api/v1/orgs/acme': orgAs(role),
    'GET /api/v1/orgs/acme/members': MEMBERS,
    [`GET /api/v1/orgs/acme/run-requests/${RQ}`]: detail({ request: req }),
  });
}

// Spec 2026-11-25 A, sync_first: a request waits for its repository's sync,
// then is pending, or fails with the sync's error.
describe('RunRequestDetailPage, sync first', () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it('polls while the request waits and stops once it is pending, then approves without a changed notice', async () => {
    let loads = 0;
    mockApi({
      'GET /api/v1/orgs/acme': orgAs('operator'),
      'GET /api/v1/orgs/acme/members': MEMBERS,
      [`GET /api/v1/orgs/acme/run-requests/${RQ}`]: () => {
        loads += 1;
        return loads < 3
          ? detail({ request: { status: 'waiting_for_sync', expires_at: undefined, sync_job_id: SYNC }, fingerprint: 'fp-waiting' })
          : detail({ fingerprint: 'fp-pending' });
      },
    });
    vi.useFakeTimers({ shouldAdvanceTime: true });
    renderDetail();
    expect(await screen.findByRole('note', { name: "Waiting for the repository's sync" })).toBeInTheDocument();
    expect(loads).toBe(1);
    await vi.advanceTimersByTimeAsync(5_000);
    await vi.waitFor(() => expect(loads).toBe(2));
    await vi.advanceTimersByTimeAsync(5_000);
    expect(await screen.findByRole('button', { name: 'Approve' })).toBeEnabled();
    expect(loads).toBe(3);
    expect(screen.queryByText('This request changed since you opened it. Review it again.')).not.toBeInTheDocument();
    await vi.advanceTimersByTimeAsync(20_000);
    expect(loads).toBe(3);
  });

  it('polls while approval waits for a running sync and stops once it may be approved, without a changed notice', async () => {
    let loads = 0;
    const commit = (blocked?: string[]) => ({ sha: '0123456789abcdef0123456789abcdef01234567', ...(blocked ? { blocked } : {}) });
    mockApi({
      'GET /api/v1/orgs/acme': orgAs('operator'),
      'GET /api/v1/orgs/acme/members': MEMBERS,
      [`GET /api/v1/orgs/acme/run-requests/${RQ}`]: () => {
        loads += 1;
        return loads < 3
          ? detail({ picture: { commit: commit(['sync_running']) }, fingerprint: 'fp-blocked' })
          : detail({ picture: { commit: { ...commit(), sha: 'fedcba9876543210fedcba9876543210fedcba98' } }, fingerprint: 'fp-free' });
      },
    });
    vi.useFakeTimers({ shouldAdvanceTime: true });
    renderDetail();
    expect(await screen.findByRole('button', { name: 'Approve' })).toBeDisabled();
    expect(loads).toBe(1);
    await vi.advanceTimersByTimeAsync(5_000);
    await vi.waitFor(() => expect(loads).toBe(2));
    await vi.advanceTimersByTimeAsync(5_000);
    await vi.waitFor(() => expect(screen.getByRole('button', { name: 'Approve' })).toBeEnabled());
    expect(loads).toBe(3);
    // Nothing could be approved while it was blocked, so the free picture
    // is the one reviewed.
    expect(screen.queryByText('This request changed since you opened it. Review it again.')).not.toBeInTheDocument();
    await vi.advanceTimersByTimeAsync(20_000);
    expect(loads).toBe(3);
  });

  it('does not poll a request blocked only until the repository is synced by hand', async () => {
    let loads = 0;
    mockApi({
      'GET /api/v1/orgs/acme': orgAs('operator'),
      'GET /api/v1/orgs/acme/members': MEMBERS,
      [`GET /api/v1/orgs/acme/run-requests/${RQ}`]: () => {
        loads += 1;
        return detail({ picture: { commit: { sha: '', blocked: ['never_synced'] } } });
      },
    });
    vi.useFakeTimers({ shouldAdvanceTime: true });
    renderDetail();
    expect(await screen.findByRole('button', { name: 'Approve' })).toBeDisabled();
    await vi.advanceTimersByTimeAsync(20_000);
    expect(loads).toBe(1);
  });

  it('says the request waits for its sync, links the sync, holds approval and allows a rejection', async () => {
    mockDetail({ status: 'waiting_for_sync', expires_at: undefined, sync_job_id: SYNC });
    renderDetail();

    const note = await screen.findByRole('note', { name: "Waiting for the repository's sync" });
    expect(note).toHaveTextContent('It can be approved once the sync has finished; its expiry starts then.');
    expect(within(note).getByRole('link', { name: 'Open the sync' })).toHaveAttribute('href', `/o/acme/runs/${SYNC}`);
    expect(screen.getAllByText('waiting for sync').length).toBeGreaterThan(0);
    expect(screen.getByText('starts once the sync has finished')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Reject' })).toBeEnabled();
  });

  it("shows a failed request's sync error and that the run was not started", async () => {
    mockDetail({ status: 'failed', expires_at: '2026-11-16T09:05:00Z', sync_job_id: SYNC, sync_error: "the repository's sync failed: git fetch: authentication required" });
    renderDetail();

    const alert = await screen.findByRole('alert', { name: "The repository's sync failed" });
    expect(alert).toHaveTextContent('git fetch: authentication required');
    expect(within(alert).getByRole('link', { name: 'Open the sync' })).toHaveAttribute('href', `/o/acme/runs/${SYNC}`);
    expect(screen.getByText('Failed: the sync it waited for did not succeed. The run was not started.')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Reject' })).not.toBeInTheDocument();
  });

  it('says it in German', async () => {
    mockDetail({ status: 'waiting_for_sync', expires_at: undefined, sync_job_id: SYNC });
    renderDetail('de');
    expect(await screen.findByRole('note', { name: 'Wartet auf den Sync des Repositorys' })).toBeInTheDocument();
    expect(screen.getAllByText('wartet auf Sync').length).toBeGreaterThan(0);
  });
});

describe('RunRequestsPage, sync first', () => {
  it('lists waiting and failed requests with their state, the waiting ones after the pending ones', async () => {
    mockApi({
      'GET /api/v1/auth/me/preferences': { density: 'standard', page_size: 25, lists: {} },
      'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
      'GET /api/v1/orgs/acme/job-templates': { templates: [{ id: 'tpl1', name: 'deploy' }] },
      'GET /api/v1/orgs/acme/run-requests/pending-count': { count: 1 },
      'GET /api/v1/orgs/acme/run-requests': {
        requests: [
          request({ id: 'r-fail', reason: 'it broke', status: 'failed', expires_at: '2026-11-16T09:05:00Z', sync_error: 'boom' }),
          request({ id: 'r-wait', reason: 'wait for it', status: 'waiting_for_sync', expires_at: undefined, sync_job_id: SYNC }),
          request({ id: 'r-pend', reason: 'go ahead' }),
        ],
      },
    });
    renderListPage({
      path: '/runs/requests',
      component: RunRequestsPage,
      initial: '/o/acme/runs/requests',
      extraSearch: (s) => ({ ...validateRunRequestsSearch(s) }),
      others: [{ path: '/runs/requests/$id', component: () => <div>REQUEST DETAIL</div> }],
    });

    expect(await screen.findByText('wait for it')).toBeInTheDocument();
    expect(firstCells()).toEqual(['go ahead', 'wait for it', 'it broke']);
    expect(screen.getByText('waits for the sync')).toBeInTheDocument();
    expect(screen.getAllByText('waiting for sync').length).toBeGreaterThan(0);
    expect(screen.getAllByText('failed').length).toBeGreaterThan(0);
  });
});
