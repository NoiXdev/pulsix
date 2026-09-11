import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { HostDetailPage } from './HostDetailPage.tsx';

const HOST = {
  host: {
    id: 'h1',
    name: 'web-04.fra',
    zone_id: 'z1',
    address: '203.0.113.4',
    managed: false,
    monitored: false,
    last_seen_at: '2026-03-01T00:00:00Z',
    facts: { os: 'debian-13' },
  },
};

function renderPage(qc: QueryClient) {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const hostsRoute = createRoute({ getParentRoute: () => orgRoute, path: '/hosts', component: () => <div>HOSTS PAGE</div> });
  const detailRoute = createRoute({ getParentRoute: () => orgRoute, path: '/hosts/$id', component: HostDetailPage });
  const routeTree = rootRoute.addChildren([orgRoute.addChildren([hostsRoute, detailRoute])]);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ['/o/acme/hosts/h1'] }) });

  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('HostDetailPage', () => {
  it('renders facts and toggles the Managed capability', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let patched: unknown;
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1': HOST,
      'GET /api/v1/orgs/acme/hosts/h1/tags': { tags: [] },
      'PATCH /api/v1/orgs/acme/hosts/h1/capabilities': (req: MockRequest) => {
        patched = req.body;
        return { host: { ...HOST.host, managed: true } };
      },
    });

    renderPage(qc);

    expect(await screen.findByText('web-04.fra')).toBeInTheDocument();
    expect(screen.getByText('debian-13')).toBeInTheDocument();

    await user.click(screen.getByRole('switch', { name: /Managed/ }));
    expect(patched).toEqual({ managed: true });
  });

  it('adds and removes a manual tag', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let putTags: unknown;
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1': HOST,
      'GET /api/v1/orgs/acme/hosts/h1/tags': { tags: [] },
      'PUT /api/v1/orgs/acme/hosts/h1/tags': (req: MockRequest) => {
        putTags = req.body;
        return { tags: [{ key: 'role', value: 'db', source: 'manual' }] };
      },
    });

    renderPage(qc);

    await user.type(await screen.findByPlaceholderText('role=db'), 'role=db');
    await user.click(screen.getByRole('button', { name: 'Add tag' }));

    expect(putTags).toEqual([{ key: 'role', value: 'db' }]);
  });

  it('rejects a malformed tag chip inline without a round trip', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1': HOST,
      'GET /api/v1/orgs/acme/hosts/h1/tags': { tags: [] },
    });

    renderPage(qc);

    await user.type(await screen.findByPlaceholderText('role=db'), 'Role=db');
    await user.click(screen.getByRole('button', { name: 'Add tag' }));

    expect(await screen.findByText(/not a valid tag key/)).toBeInTheDocument();
  });

  it('deletes the host after confirmation and returns to the list', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const originalConfirm = window.confirm;
    window.confirm = () => true;
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1': HOST,
      'GET /api/v1/orgs/acme/hosts/h1/tags': { tags: [] },
      'DELETE /api/v1/orgs/acme/hosts/h1': {},
    });

    renderPage(qc);

    await user.click(await screen.findByRole('button', { name: /Delete host/ }));

    expect(await screen.findByText('HOSTS PAGE')).toBeInTheDocument();
    window.confirm = originalConfirm;
  });

  it('surfaces a toast instead of an unhandled rejection when delete fails', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const originalConfirm = window.confirm;
    window.confirm = () => true;
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1': HOST,
      'GET /api/v1/orgs/acme/hosts/h1/tags': { tags: [] },
      'DELETE /api/v1/orgs/acme/hosts/h1': () => new Response(JSON.stringify({ detail: 'host still managed' }), { status: 409 }),
    });

    renderPage(qc);

    await user.click(await screen.findByRole('button', { name: /Delete host/ }));

    const toast = await screen.findByRole('status');
    expect(toast).toHaveTextContent('host still managed');
    window.confirm = originalConfirm;
  });

  it('shows the not-found page for a 404 instead of hanging on "Loading…" (Important 7)', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1': () => new Response(JSON.stringify({ detail: 'not found' }), { status: 404 }),
    });

    renderPage(qc);

    expect(await screen.findByText('Not found')).toBeInTheDocument();
  });
});
