import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { HostsListPage } from './HostsListPage.tsx';

const ZONES = { zones: [{ id: 'z1', slug: 'fra', name: 'Frankfurt' }] };
const HOSTS = {
  hosts: [
    { id: 'h1', name: 'web-04.fra', zone_id: 'z1', address: '203.0.113.4', managed: true, monitored: false, last_seen_at: '2026-03-01T00:00:00Z', facts: {} },
  ],
};
const SECOND_PAGE_HOST = {
  id: 'h2',
  name: 'db-01.fra',
  zone_id: 'z1',
  address: '203.0.113.9',
  managed: false,
  monitored: true,
  last_seen_at: '2026-03-01T00:00:00Z',
  facts: {},
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
  const hostRoute = createRoute({ getParentRoute: () => orgRoute, path: '/hosts/$id', component: () => <div>HOST PAGE</div> });
  const routeTree = rootRoute.addChildren([orgRoute.addChildren([hostsRoute, hostRoute])]);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [initialPath] }) });

  const result = render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
  return { ...result, router };
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
    await pickOption(screen, 'Zone', { value: 'z1' });
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

  it('loads the next page through "Load more", sending the cursor, and hides the control on the last page (I1)', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const requests: Array<{ cursor: string | null; limit: string | null }> = [];
    mockApi({
      'GET /api/v1/orgs/acme/hosts': (req: MockRequest) => {
        const cursor = req.search.get('cursor');
        requests.push({ cursor, limit: req.search.get('limit') });
        if (cursor === 'page-2') return { hosts: [SECOND_PAGE_HOST], next_cursor: '' };
        return { hosts: HOSTS.hosts, next_cursor: 'page-2' };
      },
      'GET /api/v1/orgs/acme/zones': ZONES,
    });

    renderPage(qc);

    await screen.findByText('web-04.fra');
    // A partial list is not presented as the total.
    expect(screen.getByText('1 hosts loaded, more available')).toBeInTheDocument();
    expect(requests).toEqual([{ cursor: null, limit: '50' }]);

    await user.click(screen.getByRole('button', { name: 'Load more' }));

    expect(await screen.findByText('db-01.fra')).toBeInTheDocument();
    expect(screen.getByText('web-04.fra')).toBeInTheDocument();
    expect(requests[1]).toEqual({ cursor: 'page-2', limit: '50' });
    expect(screen.getByText('2 hosts')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument();
  });

  it('starts over from the first page when a filter changes', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const requests: Array<{ cursor: string | null; managed: string | null }> = [];
    mockApi({
      'GET /api/v1/orgs/acme/hosts': (req: MockRequest) => {
        const cursor = req.search.get('cursor');
        const managed = req.search.get('managed');
        requests.push({ cursor, managed });
        if (managed === 'true') return { hosts: HOSTS.hosts, next_cursor: '' };
        if (cursor === 'page-2') return { hosts: [SECOND_PAGE_HOST], next_cursor: '' };
        return { hosts: HOSTS.hosts, next_cursor: 'page-2' };
      },
      'GET /api/v1/orgs/acme/zones': ZONES,
    });

    renderPage(qc);

    await user.click(await screen.findByRole('button', { name: 'Load more' }));
    await screen.findByText('db-01.fra');

    await user.click(screen.getByRole('button', { name: 'Managed' }));

    await waitFor(() => expect(screen.queryByText('db-01.fra')).not.toBeInTheDocument());
    expect(screen.getByText('web-04.fra')).toBeInTheDocument();
    expect(requests.at(-1)).toEqual({ cursor: null, managed: 'true' });
    expect(screen.getByText('1 host')).toBeInTheDocument();
  });

  it('hides "Load more" while the tag selector drives the list', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mockApi({
      'GET /api/v1/orgs/acme/hosts': { hosts: HOSTS.hosts, next_cursor: 'page-2' },
      'GET /api/v1/orgs/acme/zones': ZONES,
      'POST /api/v1/orgs/acme/hosts/query': { hosts: [SECOND_PAGE_HOST] },
    });

    renderPage(qc);

    await screen.findByRole('button', { name: 'Load more' });
    await user.type(screen.getByPlaceholderText('selector, e.g. role = db'), 'role = db');
    await user.click(screen.getByRole('button', { name: 'Run' }));

    expect(await screen.findByText('db-01.fra')).toBeInTheDocument();
    expect(screen.queryByText('web-04.fra')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument();
  });
});

// Backlog 1.12. The shell carried a global "All zones" control that opened
// nothing — a promise with no mechanism behind it. Only two endpoints take
// a zone filter at all, and the fleet page already has its own; this is
// the other one, where GET /hosts has supported ?zone= all along and the
// page simply never offered it. A filter that works on the page it belongs
// to beats a global one that would do nothing on most of them.
describe('zone filter', () => {
  it('narrows the host request to the chosen zone', async () => {
    const asked: Array<string | null> = [];
    mockApi({
      'GET /api/v1/orgs/acme/zones': ZONES,
      'GET /api/v1/orgs/acme/hosts': (req: MockRequest) => {
        asked.push(req.search.get('zone'));
        return HOSTS;
      },
    });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    renderPage(qc);

    await waitFor(() => expect(asked.length).toBeGreaterThan(0));
    expect(asked[0]).toBeFalsy();

    await screen.findByLabelText('Filter by zone');
    await pickOption(screen, 'Filter by zone', { value: 'z1' });

    await waitFor(() => {
      expect(asked.at(-1)).toBe('z1');
    });
  });
});

const org = (role: string) => ({ org: { id: 'o1', name: 'Acme', slug: 'acme', role, created_at: '2026-01-01T00:00:00Z' } });
const MONITORED_HOST = { ...HOSTS.hosts[0], monitored: true };

function rowRoutes(role: string, host: Record<string, unknown> = MONITORED_HOST) {
  return {
    'GET /api/v1/orgs/acme': org(role),
    'GET /api/v1/orgs/acme/hosts': { hosts: [host] },
    'GET /api/v1/orgs/acme/zones': ZONES,
  };
}

describe('host rows', () => {
  it('whole row links to the host page', async () => {
    mockApi(rowRoutes('operator'));
    renderPage(new QueryClient({ defaultOptions: { queries: { retry: false } } }));

    const link = await screen.findByRole('link', { name: 'web-04.fra' });
    expect(link).toHaveAttribute('href', '/o/acme/hosts/h1');
    expect(link).toHaveTextContent('fra');
    expect(link).toHaveTextContent('Managed');
    expect(link).toHaveTextContent('Monitored');
  });

  it('shows muted "not managed" / "not monitored" for a host without the capabilities', async () => {
    mockApi(rowRoutes('operator', { ...HOSTS.hosts[0], managed: false, monitored: false }));
    renderPage(new QueryClient({ defaultOptions: { queries: { retry: false } } }));

    const link = await screen.findByRole('link', { name: 'web-04.fra' });
    expect(link).toHaveTextContent('not managed');
    expect(link).toHaveTextContent('not monitored');
  });

  it('menu toggle does not navigate', async () => {
    const user = userEvent.setup();
    let patched: unknown;
    mockApi({
      ...rowRoutes('operator'),
      'PATCH /api/v1/orgs/acme/hosts/h1/capabilities': (req: MockRequest) => {
        patched = req.body;
        return { host: { ...MONITORED_HOST, monitored: false } };
      },
    });
    const { router } = renderPage(new QueryClient({ defaultOptions: { queries: { retry: false } } }));

    await user.click(await screen.findByRole('button', { name: 'Actions for web-04.fra' }));
    await user.click(await screen.findByRole('menuitem', { name: 'Switch monitoring off' }));

    await waitFor(() => expect(patched).toEqual({ monitored: false }));
    expect(router.state.location.pathname).toBe('/o/acme/hosts');
    expect(screen.queryByText('HOST PAGE')).not.toBeInTheDocument();
  });

  it('switches management on for an unmanaged host', async () => {
    const user = userEvent.setup();
    let patched: unknown;
    mockApi({
      ...rowRoutes('operator', { ...HOSTS.hosts[0], managed: false }),
      'PATCH /api/v1/orgs/acme/hosts/h1/capabilities': (req: MockRequest) => {
        patched = req.body;
        return { host: HOSTS.hosts[0] };
      },
    });
    renderPage(new QueryClient({ defaultOptions: { queries: { retry: false } } }));

    await user.click(await screen.findByRole('button', { name: 'Actions for web-04.fra' }));
    await user.click(await screen.findByRole('menuitem', { name: 'Switch management on' }));

    await waitFor(() => expect(patched).toEqual({ managed: true }));
  });

  it('capabilities in the list are not interactive', async () => {
    mockApi(rowRoutes('admin'));
    renderPage(new QueryClient({ defaultOptions: { queries: { retry: false } } }));

    await screen.findByRole('link', { name: 'web-04.fra' });
    expect(screen.queryAllByRole('switch')).toHaveLength(0);
    expect(screen.queryAllByRole('checkbox')).toHaveLength(0);
    expect(screen.queryByRole('button', { name: /enable/ })).not.toBeInTheDocument();
  });

  it('viewer sees only Open', async () => {
    const user = userEvent.setup();
    mockApi(rowRoutes('viewer'));
    renderPage(new QueryClient({ defaultOptions: { queries: { retry: false } } }));

    await user.click(await screen.findByRole('button', { name: 'Actions for web-04.fra' }));
    const items = await screen.findAllByRole('menuitem');
    expect(items.map((i) => i.textContent)).toEqual(['Open']);
  });

  it('operator sees every item, in order', async () => {
    const user = userEvent.setup();
    mockApi(rowRoutes('operator'));
    renderPage(new QueryClient({ defaultOptions: { queries: { retry: false } } }));

    await user.click(await screen.findByRole('button', { name: 'Actions for web-04.fra' }));
    const items = await screen.findAllByRole('menuitem');
    expect(items.map((i) => i.textContent)).toEqual([
      'Open',
      'Edit',
      'Run command',
      'Switch monitoring off',
      'Switch management off',
      'Delete…',
    ]);
  });

  it('offers no Run command for a host that is not managed', async () => {
    const user = userEvent.setup();
    mockApi(rowRoutes('operator', { ...HOSTS.hosts[0], managed: false }));
    renderPage(new QueryClient({ defaultOptions: { queries: { retry: false } } }));

    await user.click(await screen.findByRole('button', { name: 'Actions for web-04.fra' }));
    await screen.findAllByRole('menuitem');
    expect(screen.queryByRole('menuitem', { name: 'Run command' })).not.toBeInTheDocument();
  });

  it('Open navigates to the host page', async () => {
    const user = userEvent.setup();
    mockApi(rowRoutes('viewer'));
    const { router } = renderPage(new QueryClient({ defaultOptions: { queries: { retry: false } } }));

    await user.click(await screen.findByRole('button', { name: 'Actions for web-04.fra' }));
    await user.click(await screen.findByRole('menuitem', { name: 'Open' }));

    expect(await screen.findByText('HOST PAGE')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/o/acme/hosts/h1');
  });

  it('Edit opens the host dialog without leaving the list', async () => {
    const user = userEvent.setup();
    mockApi(rowRoutes('operator'));
    const { router } = renderPage(new QueryClient({ defaultOptions: { queries: { retry: false } } }));

    await user.click(await screen.findByRole('button', { name: 'Actions for web-04.fra' }));
    await user.click(await screen.findByRole('menuitem', { name: 'Edit' }));

    expect(await screen.findByRole('dialog', { name: 'Edit host web-04.fra' })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/o/acme/hosts');
  });

  it('Run command opens the ad-hoc dialog with this host selected', async () => {
    const user = userEvent.setup();
    mockApi(rowRoutes('operator'));
    renderPage(new QueryClient({ defaultOptions: { queries: { retry: false } } }));

    await user.click(await screen.findByRole('button', { name: 'Actions for web-04.fra' }));
    await user.click(await screen.findByRole('menuitem', { name: 'Run command' }));

    const dialog = await screen.findByRole('dialog', { name: 'Run command' });
    expect(await within(dialog).findByRole('checkbox', { name: 'web-04.fra' })).toBeChecked();
    expect(within(dialog).getByText('1 host(s) selected')).toBeInTheDocument();
  });

  it('Delete asks first, then deletes and stays on the list', async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
    let deleted = false;
    mockApi({
      ...rowRoutes('operator'),
      'DELETE /api/v1/orgs/acme/hosts/h1': () => {
        deleted = true;
        return {};
      },
    });
    const { router } = renderPage(new QueryClient({ defaultOptions: { queries: { retry: false } } }));

    await user.click(await screen.findByRole('button', { name: 'Actions for web-04.fra' }));
    await user.click(await screen.findByRole('menuitem', { name: 'Delete…' }));

    await waitFor(() => expect(deleted).toBe(true));
    expect(confirm).toHaveBeenCalledWith('Delete host web-04.fra? This cannot be undone.');
    expect(await screen.findByText('Host web-04.fra deleted')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/o/acme/hosts');
    confirm.mockRestore();
  });

  it('Delete does nothing when the confirmation is declined', async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    let deleted = false;
    mockApi({
      ...rowRoutes('operator'),
      'DELETE /api/v1/orgs/acme/hosts/h1': () => {
        deleted = true;
        return {};
      },
    });
    renderPage(new QueryClient({ defaultOptions: { queries: { retry: false } } }));

    await user.click(await screen.findByRole('button', { name: 'Actions for web-04.fra' }));
    await user.click(await screen.findByRole('menuitem', { name: 'Delete…' }));

    expect(confirm).toHaveBeenCalled();
    expect(deleted).toBe(false);
    confirm.mockRestore();
  });
});
