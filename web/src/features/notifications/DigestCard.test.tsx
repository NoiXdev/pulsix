import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { DigestCard } from './DigestCard.tsx';

const digest = { digest: { enabled: false, weekday: 'mon', time: '08:00', timezone: 'UTC', channels: [] } };

/** QueryError — the guard element a failed query renders — calls
 * useNavigate (for the 401 redirect), so the card renders inside a
 * minimal router like the ScheduleCalendar tests do. */
function renderIt(qc: QueryClient) {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const page = createRoute({
    getParentRoute: () => orgRoute, path: '/settings/notifications', component: () => <DigestCard org="acme" channels={[]} canWrite={false} />,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([page])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/settings/notifications'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

/**
 * Backlog 2.85: the card used to `return null` while the digest query
 * failed, so the section silently vanished instead of saying why — the
 * same silent-failure class the calendar's empty grid was, fixed the
 * same way (useQueryGuard renders the error state).
 */
describe('DigestCard', () => {
  it('shows the form when the digest query succeeds', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/notification-digest': digest });
    renderIt(new QueryClient({ defaultOptions: { queries: { retry: false } } }));
    expect(await screen.findByRole('region', { name: 'Weekly digest' })).toBeInTheDocument();
  });

  it('reports a failed query instead of vanishing', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/notification-digest': () =>
        new Response(JSON.stringify({ detail: 'internal error' }), { status: 500, headers: { 'Content-Type': 'application/json' } }),
    });
    renderIt(new QueryClient({ defaultOptions: { queries: { retry: false } } }));
    expect(await screen.findByText('Something went wrong')).toBeInTheDocument();
    expect(screen.getByText('internal error')).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Weekly digest' })).toBeNull();
  });

  it('renders the form again once a refetch succeeds', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let failing = true;
    mockApi({
      'GET /api/v1/orgs/acme/notification-digest': () =>
        failing
          ? new Response(JSON.stringify({ detail: 'internal error' }), { status: 500, headers: { 'Content-Type': 'application/json' } })
          : digest,
    });
    renderIt(qc);
    expect(await screen.findByText('Something went wrong')).toBeInTheDocument();
    failing = false;
    await qc.refetchQueries();
    expect(await screen.findByRole('region', { name: 'Weekly digest' })).toBeInTheDocument();
    expect(screen.queryByText('Something went wrong')).not.toBeInTheDocument();
  });
});
