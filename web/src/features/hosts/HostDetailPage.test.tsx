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

/** The org response carries the role every permission on this page is
 * derived from. Every test mocks it: a page that renders before it lands
 * is the state this file's last two cases are about. */
const ORG = { org: { id: 'o1', name: 'Acme', slug: 'acme', role: 'admin', created_at: '2026-01-01T00:00:00Z' } };

const NO_CHECKS = { host_id: 'h1', monitored: false, checks: [] };

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
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/checks': NO_CHECKS,
      'GET /api/v1/orgs/acme/hosts/h1': HOST,
      'GET /api/v1/orgs/acme/hosts/h1/tags': { tags: [] },
      'GET /api/v1/orgs/acme/hosts/h1/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/ssh': { ssh: { host_id: 'h1', username: '', port: 22, bastion_mode: 'inherit' } },
      'GET /api/v1/orgs/acme/bastions': { bastions: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [] },
      'GET /api/v1/orgs/acme/hosts/h1/inventory/preview': { vars: {}, connection: { host_id: 'h1', bastion_source: 'none' }, warnings: [] },
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
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/checks': NO_CHECKS,
      'GET /api/v1/orgs/acme/hosts/h1': HOST,
      'GET /api/v1/orgs/acme/hosts/h1/tags': { tags: [] },
      'GET /api/v1/orgs/acme/hosts/h1/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/ssh': { ssh: { host_id: 'h1', username: '', port: 22, bastion_mode: 'inherit' } },
      'GET /api/v1/orgs/acme/bastions': { bastions: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [] },
      'GET /api/v1/orgs/acme/hosts/h1/inventory/preview': { vars: {}, connection: { host_id: 'h1', bastion_source: 'none' }, warnings: [] },
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
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/checks': NO_CHECKS,
      'GET /api/v1/orgs/acme/hosts/h1': HOST,
      'GET /api/v1/orgs/acme/hosts/h1/tags': { tags: [] },
      'GET /api/v1/orgs/acme/hosts/h1/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/ssh': { ssh: { host_id: 'h1', username: '', port: 22, bastion_mode: 'inherit' } },
      'GET /api/v1/orgs/acme/bastions': { bastions: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [] },
      'GET /api/v1/orgs/acme/hosts/h1/inventory/preview': { vars: {}, connection: { host_id: 'h1', bastion_source: 'none' }, warnings: [] },
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
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/checks': NO_CHECKS,
      'GET /api/v1/orgs/acme/hosts/h1': HOST,
      'GET /api/v1/orgs/acme/hosts/h1/tags': { tags: [] },
      'GET /api/v1/orgs/acme/hosts/h1/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/ssh': { ssh: { host_id: 'h1', username: '', port: 22, bastion_mode: 'inherit' } },
      'GET /api/v1/orgs/acme/bastions': { bastions: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [] },
      'GET /api/v1/orgs/acme/hosts/h1/inventory/preview': { vars: {}, connection: { host_id: 'h1', bastion_source: 'none' }, warnings: [] },
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
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/checks': NO_CHECKS,
      'GET /api/v1/orgs/acme/hosts/h1': HOST,
      'GET /api/v1/orgs/acme/hosts/h1/tags': { tags: [] },
      'GET /api/v1/orgs/acme/hosts/h1/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/ssh': { ssh: { host_id: 'h1', username: '', port: 22, bastion_mode: 'inherit' } },
      'GET /api/v1/orgs/acme/bastions': { bastions: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [] },
      'GET /api/v1/orgs/acme/hosts/h1/inventory/preview': { vars: {}, connection: { host_id: 'h1', bastion_source: 'none' }, warnings: [] },
      'DELETE /api/v1/orgs/acme/hosts/h1': () => new Response(JSON.stringify({ detail: 'host still managed' }), { status: 409 }),
    });

    renderPage(qc);

    await user.click(await screen.findByRole('button', { name: /Delete host/ }));

    const toast = await screen.findByRole('status');
    expect(toast).toHaveTextContent('host still managed');
    window.confirm = originalConfirm;
  });

  it('mounts the monitoring section for a member who may read checks', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mockApi({
      'GET /api/v1/orgs/acme': { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'viewer', created_at: '' } },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1': HOST,
      'GET /api/v1/orgs/acme/hosts/h1/tags': { tags: [] },
      'GET /api/v1/orgs/acme/hosts/h1/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/ssh': { ssh: { host_id: 'h1', username: '', port: 22, bastion_mode: 'inherit' } },
      'GET /api/v1/orgs/acme/bastions': { bastions: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [] },
      'GET /api/v1/orgs/acme/hosts/h1/inventory/preview': { vars: {}, connection: { host_id: 'h1', bastion_source: 'none' }, warnings: [] },
      'GET /api/v1/orgs/acme/hosts/h1/checks': {
        host_id: 'h1',
        monitored: true,
        checks: [
          {
            definition_id: 'd1', key: 'cpu', plugin: 'cpu', params: {}, interval_seconds: 60,
            enabled: true, origin: 'seed', source: 'org',
            values: [{ item: '', metric: 'usage_percent', value: 7.5, at: '2026-03-01T00:00:00Z', stale: false }],
          },
        ],
      },
    });

    renderPage(qc);

    // The section is wired to the host's own id and to the caller's
    // checks.read — dropping either leaves the page with no monitoring at
    // all, which is what this pins.
    expect(await screen.findByRole('heading', { name: 'Monitoring' })).toBeInTheDocument();
    expect(await screen.findByRole('heading', { name: 'cpu' })).toBeInTheDocument();
    expect(screen.getByText('7.5')).toBeInTheDocument();
  });

  it('never claims a member is not one while the role is still in flight', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    // The host is already cached — an operator who came from the list, or
    // a second visit — so the only answer still outstanding on the first
    // paint is the org's, and the role with it. That is the state the page
    // used to render fully in, telling an admin that "monitoring is
    // visible to members of this organization" because may(undefined, …)
    // is false.
    qc.setQueryData(['host', 'acme', 'h1'], HOST);
    const fetch = mockApi({
      'GET /api/v1/orgs/acme/hosts/h1': HOST,
      'GET /api/v1/orgs/acme/hosts/h1/tags': { tags: [] },
      'GET /api/v1/orgs/acme/hosts/h1/checks': NO_CHECKS,
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/ssh': { ssh: { host_id: 'h1', username: '', port: 22, bastion_mode: 'inherit' } },
      'GET /api/v1/orgs/acme/bastions': { bastions: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [] },
      'GET /api/v1/orgs/acme/hosts/h1/inventory/preview': { vars: {}, connection: { host_id: 'h1', bastion_source: 'none' }, warnings: [] },
    });

    // The org answer is held back until this test releases it; every
    // other route answers normally, so the page has everything it needs
    // EXCEPT the role.
    const base = fetch.getMockImplementation()!;
    let release!: () => void;
    const held = new Promise<void>((resolve) => {
      release = resolve;
    });
    fetch.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : new Request(input, init);
      if (new URL(request.url, 'http://localhost').pathname === '/api/v1/orgs/acme') {
        await held;
        return new Response(JSON.stringify(ORG), { status: 200, headers: { 'Content-Type': 'application/json' } });
      }
      return base(input, init);
    });

    renderPage(qc);

    // The whole page waits for the role rather than deciding without one:
    // exactly one "Loading…" (the page's), no host header rendered from
    // the cached host, and above all no verdict about membership.
    expect(await screen.findByText('Loading…')).toBeInTheDocument();
    expect(screen.queryByText('web-04.fra')).not.toBeInTheDocument();
    expect(screen.queryByText(/Monitoring is visible to members/)).not.toBeInTheDocument();

    // And once the role lands the section is rendered for real.
    release();
    expect(await screen.findByRole('heading', { name: 'Monitoring' })).toBeInTheDocument();
    expect(screen.queryByText(/Monitoring is visible to members/)).not.toBeInTheDocument();
  });

  it('reports a failed org read instead of rendering the page without a role', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mockApi({
      'GET /api/v1/orgs/acme': () => new Response(JSON.stringify({ detail: 'org store unavailable' }), { status: 500 }),
      'GET /api/v1/orgs/acme/hosts/h1': HOST,
      'GET /api/v1/orgs/acme/hosts/h1/tags': { tags: [] },
      'GET /api/v1/orgs/acme/hosts/h1/checks': NO_CHECKS,
      'GET /api/v1/orgs/acme/hosts/h1/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/ssh': { ssh: { host_id: 'h1', username: '', port: 22, bastion_mode: 'inherit' } },
      'GET /api/v1/orgs/acme/bastions': { bastions: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [] },
      'GET /api/v1/orgs/acme/hosts/h1/inventory/preview': { vars: {}, connection: { host_id: 'h1', bastion_source: 'none' }, warnings: [] },
    });

    renderPage(qc);

    // A 500 on the org read has no role in it, so there is nothing to
    // derive a permission from — and no retry, no loading state and no
    // error was the one outcome that must not survive it.
    expect(await screen.findByText('Something went wrong')).toBeInTheDocument();
    expect(screen.queryByText(/Monitoring is visible to members/)).not.toBeInTheDocument();
  });

  it('shows the not-found page for a 404 instead of hanging on "Loading…" (Important 7)', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/checks': NO_CHECKS,
      'GET /api/v1/orgs/acme/hosts/h1': () => new Response(JSON.stringify({ detail: 'not found' }), { status: 404 }),
    });

    renderPage(qc);

    expect(await screen.findByText('Not found')).toBeInTheDocument();
  });
});
