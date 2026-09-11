import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
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
      <RouterProvider router={router} />
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

    // "Tim R." appears twice: once as the actor-filter <option>, once as
    // the feed row's actor label — the row label is what this finding is
    // actually about.
    expect(await screen.findByText('created host web-04')).toBeInTheDocument();
    const actorLabels = screen.getAllByText('Tim R.');
    expect(actorLabels.length).toBeGreaterThanOrEqual(2);
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

    await screen.findByRole('option', { name: 'Tim R.' });
    await user.selectOptions(screen.getByLabelText('Filter by actor'), 'u1');

    await screen.findByText('No activity recorded yet.');
    expect(lastActorParam).toBe('u1');
  });
});
