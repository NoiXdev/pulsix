import { describe, expect, it } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { ActivityPage } from './ActivityPage.tsx';

const MEMBERS = { members: [{ user_id: 'u1', display_name: 'Tim R.', email: 'tim@acme.io', role: 'owner', created_at: '2026-01-01T00:00:00Z', disabled_at: null }] };

function renderPage(qc: QueryClient) {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const activityRoute = createRoute({ getParentRoute: () => orgRoute, path: '/settings/activity', component: ActivityPage });
  const routeTree = rootRoute.addChildren([orgRoute.addChildren([activityRoute])]);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ['/o/acme/settings/activity'] }) });

  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

/** Important 6 (task-15a-report.md fix round 1): the feed used to show
 * `actor_kind · <uuid prefix>` for every event instead of a name. */
describe('ActivityPage actor names', () => {
  it('resolves a user actor to their name from the members list', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mockApi({
      'GET /api/v1/orgs/acme/members': MEMBERS,
      'GET /api/v1/orgs/acme/activity': {
        items: [{ id: 'e1', at: '2026-03-01T10:00:00Z', actor_kind: 'user', actor_id: 'u1', action: 'created', object_type: 'host', summary: 'created host web-04' }],
        next_cursor: '',
      },
    });

    renderPage(qc);

    // "Tim R." used to also appear as the native actor-filter <option>,
    // always present in the DOM regardless of the picker's open state; a
    // ui/Select (v0.4g) renders its options only while open, so the feed
    // row's own label is what remains — and is what this finding is
    // actually about.
    expect(await screen.findByText('created host web-04')).toBeInTheDocument();
    const actorLabels = screen.getAllByText('Tim R.');
    expect(actorLabels.length).toBeGreaterThanOrEqual(1);
    expect(screen.queryByText(/actor_kind/)).not.toBeInTheDocument();
  });

  it('labels a token actor and a system actor without a raw uuid prefix', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mockApi({
      'GET /api/v1/orgs/acme/members': MEMBERS,
      'GET /api/v1/orgs/acme/activity': {
        items: [
          { id: 'e1', at: '2026-03-01T10:00:00Z', actor_kind: 'token', actor_id: 'tok1', action: 'created', object_type: 'host', summary: 'created host web-05' },
          { id: 'e2', at: '2026-03-01T09:00:00Z', actor_kind: 'system', actor_id: null, action: 'reconciled', object_type: 'zone', summary: 'reconciled default zone' },
        ],
        next_cursor: '',
      },
    });

    renderPage(qc);

    expect(await screen.findByText('API token')).toBeInTheDocument();
    expect(screen.getByText('Pulsix')).toBeInTheDocument();
  });

  it('filters by actor using the member picker, matching the API\'s user-id filter', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let lastActorParam: string | null = null;
    mockApi({
      'GET /api/v1/orgs/acme/members': MEMBERS,
      'GET /api/v1/orgs/acme/activity': (req: MockRequest) => {
        lastActorParam = req.search.get('actor');
        return { items: [], next_cursor: '' };
      },
    });

    renderPage(qc);

    // The page itself is behind useQueryGuard(activity) — wait past that
    // before reaching for the picker, or getByLabelText finds nothing.
    await screen.findByText('No activity recorded yet.');

    // Not pickOption: it reads its options synchronously, which would run
    // ahead of the members fetch (same reasoning as
    // GroupDetailPage.test.tsx's own Host picker). Open first, then wait
    // for the async-loaded row before clicking it.
    await user.click(screen.getByLabelText('Filter by actor'));
    await user.click(await screen.findByRole('option', { name: 'Tim R.' }));

    await waitFor(() => expect(lastActorParam).toBe('u1'));
    expect(lastActorParam).toBe('u1');
  });
});

/** I1: the feed used to issue one cursor-less request, silently ending at
 * the API's default page size. */
describe('ActivityPage pagination', () => {
  const FIRST = { id: 'e1', at: '2026-03-01T10:00:00Z', actor_kind: 'system', actor_id: null, action: 'reconciled', object_type: 'zone', summary: 'first page event' };
  const SECOND = { id: 'e2', at: '2026-02-27T10:00:00Z', actor_kind: 'system', actor_id: null, action: 'reconciled', object_type: 'zone', summary: 'second page event' };

  it('loads the next page through "Load more", sending the cursor, and hides the control on the last page', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const requests: Array<{ cursor: string | null; limit: string | null }> = [];
    mockApi({
      'GET /api/v1/orgs/acme/members': MEMBERS,
      'GET /api/v1/orgs/acme/activity': (req: MockRequest) => {
        const cursor = req.search.get('cursor');
        requests.push({ cursor, limit: req.search.get('limit') });
        if (cursor === 'older') return { items: [SECOND], next_cursor: '' };
        return { items: [FIRST], next_cursor: 'older' };
      },
    });

    renderPage(qc);

    expect(await screen.findByText('first page event')).toBeInTheDocument();
    expect(screen.queryByText('second page event')).not.toBeInTheDocument();
    expect(requests).toEqual([{ cursor: null, limit: '50' }]);

    await user.click(screen.getByRole('button', { name: 'Load more' }));

    expect(await screen.findByText('second page event')).toBeInTheDocument();
    expect(screen.getByText('first page event')).toBeInTheDocument();
    expect(requests[1]).toEqual({ cursor: 'older', limit: '50' });
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument();
  });

  it('starts over from the first page when the object-type filter changes', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const requests: Array<{ cursor: string | null; objectType: string | null }> = [];
    mockApi({
      'GET /api/v1/orgs/acme/members': MEMBERS,
      'GET /api/v1/orgs/acme/activity': (req: MockRequest) => {
        const cursor = req.search.get('cursor');
        const objectType = req.search.get('object_type');
        requests.push({ cursor, objectType });
        if (objectType === 'host') return { items: [], next_cursor: '' };
        if (cursor === 'older') return { items: [SECOND], next_cursor: '' };
        return { items: [FIRST], next_cursor: 'older' };
      },
    });

    renderPage(qc);

    await user.click(await screen.findByRole('button', { name: 'Load more' }));
    await screen.findByText('second page event');

    await user.click(screen.getByRole('button', { name: 'host' }));

    expect(await screen.findByText('No activity recorded yet.')).toBeInTheDocument();
    expect(requests.at(-1)).toEqual({ cursor: null, objectType: 'host' });
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument();
  });
});

describe('ActivityPage automation object types', () => {
  it('filters by the automation object types', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const seen: (string | null)[] = [];
    mockApi({
      'GET /api/v1/orgs/acme/members': MEMBERS,
      'GET /api/v1/orgs/acme/activity': (req: MockRequest) => {
        seen.push(req.search.get('object_type'));
        return {
          items: [{ id: 'e1', at: '2026-03-01T10:00:00Z', actor_kind: 'schedule', actor_id: 's1', action: 'schedule.run', object_type: 'job_template', summary: 'schedule "Nightly" triggered' }],
          next_cursor: '',
        };
      },
    });
    renderPage(qc);
    expect(await screen.findByText('schedule "Nightly" triggered')).toBeInTheDocument();
    for (const t of ['schedule', 'trigger', 'worker', 'channel', 'maintenance', 'job_template']) {
      expect(screen.getByRole('button', { name: t })).toBeInTheDocument();
    }
    await user.click(screen.getByRole('button', { name: 'maintenance' }));
    await waitFor(() => expect(seen).toContain('maintenance'));
  });
});

