import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { HostsListPage } from './HostsListPage.tsx';

const ZONES = { zones: [{ id: 'z1', slug: 'fra', name: 'Frankfurt' }] };
const HOSTS = {
  hosts: [
    { id: 'h1', name: 'web-04.fra', zone_id: 'z1', address: '203.0.113.4', managed: true, monitored: false, last_seen_at: '2026-03-01T00:00:00Z', facts: {} },
  ],
};

function renderPage(qc: QueryClient, initialPath = '/o/acme/hosts') {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const hostsRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/hosts',
    validateSearch: (s: Record<string, unknown>) => ({ q: typeof s.q === 'string' ? s.q : undefined }),
    component: HostsListPage,
  });
  const routeTree = rootRoute.addChildren([orgRoute.addChildren([hostsRoute])]);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [initialPath] }) });

  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('HostsListPage', () => {
  it('shows the zone slug, not a uuid prefix (cheap fix)', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mockApi({ 'GET /api/v1/orgs/acme/hosts': HOSTS, 'GET /api/v1/orgs/acme/zones': ZONES });

    renderPage(qc);

    expect(await screen.findByText('fra')).toBeInTheDocument();
    expect(screen.queryByText('z1')).not.toBeInTheDocument();
  });

  it('reads the initial ?q= from the URL (the shell\'s ⌘K search, Important 6) and applies it as a filter', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let lastQ: string | null = null;
    mockApi({
      'GET /api/v1/orgs/acme/hosts': (req: MockRequest) => {
        lastQ = req.search.get('q');
        return HOSTS;
      },
      'GET /api/v1/orgs/acme/zones': ZONES,
    });

    renderPage(qc, '/o/acme/hosts?q=web-04');

    await screen.findByText('web-04.fra');
    expect(lastQ).toBe('web-04');
    expect(screen.getByPlaceholderText('Filter hosts…')).toHaveValue('web-04');
  });

  it('creates a host through the Add host dialog', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let created: unknown;
    mockApi({
      'GET /api/v1/orgs/acme/hosts': { hosts: [] },
      'GET /api/v1/orgs/acme/zones': ZONES,
      'POST /api/v1/orgs/acme/hosts': (req: MockRequest) => {
        created = req.body;
        return { host: { ...HOSTS.hosts[0], id: 'h2', name: 'web-09.fra' } };
      },
    });

    renderPage(qc);

    await user.click(await screen.findByRole('button', { name: /Add host/ }));
    await user.type(screen.getByPlaceholderText('web-04.fra'), 'web-09.fra');
    await user.selectOptions(screen.getByLabelText('Zone'), 'z1');
    await user.click(screen.getByRole('button', { name: 'Create host' }));

    expect(await screen.findByText('Host web-09.fra created')).toBeInTheDocument();
    expect(created).toMatchObject({ name: 'web-09.fra', zone_id: 'z1' });
  });

  it('shows the friendly error page instead of "No hosts yet" when the list request fails (Important 7)', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mockApi({
      'GET /api/v1/orgs/acme/hosts': () => new Response(JSON.stringify({ detail: 'internal error' }), { status: 500 }),
      'GET /api/v1/orgs/acme/zones': ZONES,
    });

    renderPage(qc);

    expect(await screen.findByText('Something went wrong')).toBeInTheDocument();
    expect(screen.queryByText('No hosts yet. Add one to get started.')).not.toBeInTheDocument();
  });

  it('runs the tag selector query and shows the matching hosts', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mockApi({
      'GET /api/v1/orgs/acme/hosts': { hosts: [] },
      'GET /api/v1/orgs/acme/zones': ZONES,
      'POST /api/v1/orgs/acme/hosts/query': { hosts: HOSTS.hosts },
    });

    renderPage(qc);

    await screen.findByText('No hosts yet. Add one to get started.');
    await user.type(screen.getByPlaceholderText('selector, e.g. role = db'), 'role = db');
    await user.click(screen.getByRole('button', { name: 'Run' }));

    expect(await screen.findByText('web-04.fra')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'clear' })).toBeInTheDocument();
  });
});
