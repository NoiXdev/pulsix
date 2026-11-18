import { describe, expect, it } from 'vitest';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { HostsListPage } from './HostsListPage.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { acceptConfirm, declineConfirm } from '../../test/confirmDialog.ts';
import { settled } from '../../test/settled.ts';
import { validateHostsSearch } from './hostsSearch.ts';

const ZONES = { zones: [{ id: 'z1', slug: 'fra', name: 'Frankfurt' }] };
const PREFS = { density: 'standard', page_size: 25, lists: {} };
const WEB_04 = {
  id: 'h1',
  name: 'web-04.fra',
  zone_id: 'z1',
  address: '203.0.113.4',
  managed: true,
  monitored: false,
  last_seen_at: '2026-03-01T00:00:00Z',
  facts: {},
  active_runs: 0,
};
const HOSTS = { hosts: [WEB_04], next_cursor: '', total: 1 };
const DB_01 = {
  id: 'h2',
  name: 'db-01.fra',
  zone_id: 'z1',
  address: '203.0.113.9',
  managed: false,
  monitored: true,
  last_seen_at: '2026-03-01T00:00:00Z',
  facts: {},
  active_runs: 0,
};

/** n hosts named host-01 … host-nn, ids h1 … hn. */
function manyHosts(n: number) {
  return Array.from({ length: n }, (_, i) => ({
    ...WEB_04,
    id: `h${i + 1}`,
    name: `host-${String(i + 1).padStart(2, '0')}`,
  }));
}

/** The answer for one page of `all`, as the server pages it. */
function pageOf(all: ReturnType<typeof manyHosts>, req: MockRequest) {
  const page = Number(req.search.get('page') ?? '1');
  const perPage = Number(req.search.get('per_page') ?? '25');
  return { hosts: all.slice((page - 1) * perPage, page * perPage), next_cursor: '', total: all.length };
}

function renderPage(qc: QueryClient, initialPath = '/o/acme/hosts') {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const hostsRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/hosts',
    validateSearch: validateHostsSearch,
    component: HostsListPage,
  });
  const hostRoute = createRoute({ getParentRoute: () => orgRoute, path: '/hosts/$id', component: () => <div>HOST PAGE</div> });
  const runsRoute = createRoute({ getParentRoute: () => orgRoute, path: '/runs', component: () => <div>RUNS PAGE</div> });
  const routeTree = rootRoute.addChildren([orgRoute.addChildren([hostsRoute, hostRoute, runsRoute])]);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [initialPath] }) });

  const result = render(
    <QueryClientProvider client={qc}>
      <ConfirmProvider>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </ConfirmProvider>
    </QueryClientProvider>,
  );
  return { ...result, router };
}

const newClient = () => new QueryClient({ defaultOptions: { queries: { retry: false } } });

/** The table row that holds a host's link. */
function rowOf(name: string): HTMLElement {
  return screen.getByRole('link', { name }).closest('[role="row"]') as HTMLElement;
}

describe('HostsListPage', () => {
  it('shows the zone slug, not a uuid prefix (cheap fix)', async () => {
    mockApi({ 'GET /api/v1/auth/me/preferences': PREFS, 'GET /api/v1/orgs/acme/hosts': HOSTS, 'GET /api/v1/orgs/acme/zones': ZONES });

    renderPage(newClient());

    expect(await screen.findByText('fra')).toBeInTheDocument();
    expect(screen.queryByText('z1')).not.toBeInTheDocument();
  });

  it('reads the initial ?q= from the URL (the shell\'s ⌘K search, Important 6) and applies it as a filter', async () => {
    let lastQ: string | null = null;
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme/hosts': (req: MockRequest) => {
        lastQ = req.search.get('q');
        return HOSTS;
      },
      'GET /api/v1/orgs/acme/zones': ZONES,
    });

    renderPage(newClient(), '/o/acme/hosts?q=web-04');

    await screen.findByRole('link', { name: 'web-04.fra' });
    expect(lastQ).toBe('web-04');
    expect(screen.getByPlaceholderText('Filter hosts…')).toHaveValue('web-04');
  });

  it('creates a host through the Add host dialog', async () => {
    const user = userEvent.setup();
    let created: unknown;
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme/hosts': { hosts: [], next_cursor: '', total: 0 },
      'GET /api/v1/orgs/acme/zones': ZONES,
      'POST /api/v1/orgs/acme/hosts': (req: MockRequest) => {
        created = req.body;
        return { host: { ...WEB_04, id: 'h2', name: 'web-09.fra' } };
      },
    });

    renderPage(newClient());

    await user.click(await screen.findByRole('button', { name: /Add host/ }));
    await user.type(screen.getByPlaceholderText('web-04.fra'), 'web-09.fra');
    await pickOption(screen, 'Zone', { value: 'z1' });
    await user.click(screen.getByRole('button', { name: 'Create host' }));

    expect(await screen.findByText('Host web-09.fra created')).toBeInTheDocument();
    expect(created).toMatchObject({ name: 'web-09.fra', zone_id: 'z1' });
  });

  it('shows the friendly error page instead of "No hosts yet" when the list request fails (Important 7)', async () => {
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme/hosts': () => new Response(JSON.stringify({ detail: 'internal error' }), { status: 500 }),
      'GET /api/v1/orgs/acme/zones': ZONES,
    });

    renderPage(newClient());

    expect(await screen.findByText('Something went wrong')).toBeInTheDocument();
    expect(screen.queryByText('No hosts yet. Add one to get started.')).not.toBeInTheDocument();
  });

  it('runs the tag selector query and shows the matching hosts', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme/hosts': { hosts: [], next_cursor: '', total: 0 },
      'GET /api/v1/orgs/acme/zones': ZONES,
      'POST /api/v1/orgs/acme/hosts/query': { hosts: HOSTS.hosts },
    });

    renderPage(newClient());

    await screen.findByText('No hosts yet. Add one to get started.');
    await user.type(screen.getByPlaceholderText('selector, e.g. role = db'), 'role = db');
    await user.click(screen.getByRole('button', { name: 'Run' }));

    expect(await screen.findByRole('link', { name: 'web-04.fra' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'clear' })).toBeInTheDocument();
  });

  it('shows only the selector answer while the tag selector drives the list', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme/hosts': HOSTS,
      'GET /api/v1/orgs/acme/zones': ZONES,
      'POST /api/v1/orgs/acme/hosts/query': { hosts: [DB_01] },
    });

    renderPage(newClient());

    await screen.findByRole('link', { name: 'web-04.fra' });
    await user.type(screen.getByPlaceholderText('selector, e.g. role = db'), 'role = db');
    await user.click(screen.getByRole('button', { name: 'Run' }));

    expect(await screen.findByRole('link', { name: 'db-01.fra' })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'web-04.fra' })).not.toBeInTheDocument();
  });
});

describe('hosts paging and sorting (server)', () => {
  function setup(opts: { total?: number; initial?: string; prefs?: object } = {}) {
    const all = manyHosts(opts.total ?? 30);
    const requests: URLSearchParams[] = [];
    mockApi({
      'GET /api/v1/auth/me/preferences': { ...PREFS, ...opts.prefs },
      'GET /api/v1/orgs/acme/hosts': (req: MockRequest) => {
        requests.push(req.search);
        return pageOf(all, req);
      },
      'GET /api/v1/orgs/acme/zones': ZONES,
    });
    const { router } = renderPage(newClient(), opts.initial);
    return { router, requests };
  }

  it('asks for page 1 at the saved size, with no sort until one is chosen', async () => {
    const { requests } = setup();

    await screen.findByRole('link', { name: 'host-01' });
    expect(requests).toHaveLength(1);
    expect(requests[0].get('page')).toBe('1');
    expect(requests[0].get('per_page')).toBe('25');
    expect(requests[0].has('sort')).toBe(false);
    expect(requests[0].has('order')).toBe(false);
    expect(requests[0].has('cursor')).toBe(false);
  });

  it('shows the total in the footer, not in the header', async () => {
    setup();

    expect(await screen.findByText('1–25 of 30 hosts')).toBeInTheDocument();
    expect(screen.queryByText('30 hosts')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument();
  });

  it('sorts on the server when the Zone header is pressed', async () => {
    const user = userEvent.setup();
    const { router, requests } = setup();

    await screen.findByRole('link', { name: 'host-01' });
    await user.click(screen.getByRole('button', { name: 'Zone' }));

    await waitFor(() => expect(requests.at(-1)?.get('sort')).toBe('zone'));
    expect(requests.at(-1)?.get('order')).toBe('asc');
    expect(router.state.location.search).toMatchObject({ sort: 'zone', order: 'asc' });
  });

  it('reads sort and page from the URL', async () => {
    const { requests } = setup({ initial: '/o/acme/hosts?sort=last_seen&order=desc&page=2' });

    expect(await screen.findByRole('link', { name: 'host-26' })).toBeInTheDocument();
    expect(requests[0].get('page')).toBe('2');
    expect(requests[0].get('sort')).toBe('last_seen');
    expect(requests[0].get('order')).toBe('desc');
  });

  it('moves a page past the end to the last page', async () => {
    const { router } = setup({ initial: '/o/acme/hosts?page=9' });

    expect(await screen.findByRole('link', { name: 'host-26' })).toBeInTheDocument();
    expect(router.state.location.search).toMatchObject({ page: 2 });
    expect(screen.queryByText('No hosts yet. Add one to get started.')).not.toBeInTheDocument();
  });

  it('sends active_runs=true for the "With active runs" chip and starts again at page 1', async () => {
    const user = userEvent.setup();
    const { router, requests } = setup({ initial: '/o/acme/hosts?page=2' });

    await screen.findByRole('link', { name: 'host-26' });
    await user.click(screen.getByRole('button', { name: 'With active runs' }));

    await waitFor(() => expect(requests.at(-1)?.get('active_runs')).toBe('true'));
    expect(requests.at(-1)?.get('page')).toBe('1');
    expect(requests.some((r) => r.get('active_runs') === 'true' && r.get('page') !== '1')).toBe(false);
    await waitFor(() => expect(router.state.location.search).not.toHaveProperty('page'));
  });

  it('starts again at page 1 for a new ⌘K search', async () => {
    const { router, requests } = setup({ initial: '/o/acme/hosts?page=2' });

    await screen.findByRole('link', { name: 'host-26' });
    // Only the search changes; the URL's page stays as it was.
    await router.navigate({ to: '.', search: (prev: Record<string, unknown>) => ({ ...prev, q: 'web' }) } as never);

    await waitFor(() => expect(requests.at(-1)?.get('q')).toBe('web'));
    expect(requests.at(-1)?.get('page')).toBe('1');
    expect(requests.some((r) => r.get('q') === 'web' && r.get('page') !== '1')).toBe(false);
    await waitFor(() => expect(router.state.location.search).not.toHaveProperty('page'));
  });

  it('starts again at page 1 when the Managed chip changes', async () => {
    const user = userEvent.setup();
    const { requests } = setup({ initial: '/o/acme/hosts?page=2' });

    await screen.findByRole('link', { name: 'host-26' });
    await user.click(screen.getByRole('button', { name: 'Managed' }));

    await waitFor(() => expect(requests.at(-1)?.get('managed')).toBe('true'));
    expect(requests.at(-1)?.get('page')).toBe('1');
    expect(requests.some((r) => r.get('managed') === 'true' && r.get('page') !== '1')).toBe(false);
  });

  it('writes a chip into the URL and drops the page', async () => {
    const user = userEvent.setup();
    const { router } = setup({ initial: '/o/acme/hosts?page=2' });

    await screen.findByRole('link', { name: 'host-26' });
    await user.click(screen.getByRole('button', { name: 'Managed' }));

    await waitFor(() => expect(router.state.location.search).toMatchObject({ managed: true }));
    expect(router.state.location.search).not.toHaveProperty('page');
  });

  it('applies the filters of a loaded URL', async () => {
    const { requests } = setup({ initial: '/o/acme/hosts?zone=z1&managed=true&monitored=true&activeRuns=true&q=web' });

    await screen.findByRole('link', { name: 'host-01' });
    const last = requests.at(-1);
    expect(last?.get('zone')).toBe('z1');
    expect(last?.get('managed')).toBe('true');
    expect(last?.get('monitored')).toBe('true');
    expect(last?.get('active_runs')).toBe('true');
    expect(last?.get('q')).toBe('web');
    expect(screen.getByRole('button', { name: 'Managed' })).toHaveAttribute('aria-pressed', 'true');
    expect(screen.getByPlaceholderText('Filter hosts…')).toHaveValue('web');
  });

  it('writes the zone into the URL', async () => {
    const { router } = setup();

    await screen.findByRole('link', { name: 'host-01' });
    await pickOption(screen, 'Filter by zone', { value: 'z1' });

    await waitFor(() => expect(router.state.location.search).toMatchObject({ zone: 'z1' }));
  });

  it('brings the previous filter and page back on Back', async () => {
    const user = userEvent.setup();
    const { router } = setup({ initial: '/o/acme/hosts?page=2' });

    await screen.findByRole('link', { name: 'host-26' });
    await user.click(screen.getByRole('button', { name: 'Managed' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Managed' })).toHaveAttribute('aria-pressed', 'true'));

    router.history.back();

    await waitFor(() => expect(screen.getByRole('button', { name: 'Managed' })).toHaveAttribute('aria-pressed', 'false'));
    await waitFor(() => expect(router.state.location.search).toMatchObject({ page: 2 }));
    expect(router.state.location.search).not.toHaveProperty('managed');
    expect(await screen.findByRole('link', { name: 'host-26' })).toBeInTheDocument();
  });

  it('starts again at page 1 when a search is typed', async () => {
    const user = userEvent.setup();
    const { router, requests } = setup({ initial: '/o/acme/hosts?page=2' });

    await screen.findByRole('link', { name: 'host-26' });
    await user.type(screen.getByPlaceholderText('Filter hosts…'), 'web');

    await waitFor(() => expect(requests.at(-1)?.get('q')).toBe('web'));
    expect(requests.at(-1)?.get('page')).toBe('1');
    expect(requests.some((r) => r.has('q') && r.get('page') !== '1')).toBe(false);
    await waitFor(() => expect(router.state.location.search).not.toHaveProperty('page'));
  });
});

describe('hosts in tag-selector mode (paged in the browser)', () => {
  it('pages and sorts the selector answer without asking the server again', async () => {
    const user = userEvent.setup();
    const pageRequests: URLSearchParams[] = [];
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme/hosts': (req: MockRequest) => {
        pageRequests.push(req.search);
        return HOSTS;
      },
      'GET /api/v1/orgs/acme/zones': ZONES,
      'POST /api/v1/orgs/acme/hosts/query': { hosts: manyHosts(30) },
    });
    renderPage(newClient());

    await screen.findByRole('link', { name: 'web-04.fra' });
    await user.type(screen.getByPlaceholderText('selector, e.g. role = db'), 'role = db');
    await user.click(screen.getByRole('button', { name: 'Run' }));

    expect(await screen.findByText('1–25 of 30 hosts')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'host-25' })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'host-26' })).not.toBeInTheDocument();
    const before = pageRequests.length;

    await user.click(screen.getByRole('button', { name: 'Page 2' }));
    expect(await screen.findByRole('link', { name: 'host-30' })).toBeInTheDocument();
    expect(screen.getByText('26–30 of 30 hosts')).toBeInTheDocument();

    // Host: ascending, then descending — page 1 of the descending order.
    await user.click(screen.getByRole('button', { name: 'Host' }));
    await user.click(screen.getByRole('button', { name: 'Host' }));
    await waitFor(() =>
      expect(screen.getAllByRole('link', { name: /^host-/ }).slice(0, 3).map((a) => a.getAttribute('aria-label'))).toEqual([
        'host-30',
        'host-29',
        'host-28',
      ]),
    );
    expect(pageRequests.length).toBe(before);
  });

  it('shows the active runs the selector answer carries', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme/hosts': HOSTS,
      'GET /api/v1/orgs/acme/zones': ZONES,
      'POST /api/v1/orgs/acme/hosts/query': { hosts: [{ ...DB_01, active_runs: 3 }] },
    });
    renderPage(newClient());

    await screen.findByRole('link', { name: 'web-04.fra' });
    await user.type(screen.getByPlaceholderText('selector, e.g. role = db'), 'role = db');
    await user.click(screen.getByRole('button', { name: 'Run' }));

    expect(await screen.findByRole('link', { name: '3 running' })).toHaveAttribute('href', '/o/acme/runs?host=h2&status=active');
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
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme/zones': ZONES,
      'GET /api/v1/orgs/acme/hosts': (req: MockRequest) => {
        asked.push(req.search.get('zone'));
        return HOSTS;
      },
    });
    renderPage(newClient());

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
const MONITORED_HOST = { ...WEB_04, monitored: true };

function rowRoutes(role: string, host: Record<string, unknown> = MONITORED_HOST, prefs: object = PREFS) {
  return {
    'GET /api/v1/auth/me/preferences': prefs,
    'GET /api/v1/orgs/acme': org(role),
    'GET /api/v1/orgs/acme/hosts': { hosts: [host], next_cursor: '', total: 1 },
    'GET /api/v1/orgs/acme/zones': ZONES,
  };
}

const AGENT = {
  host_id: 'h1', host_name: 'web-04.fra', agent_name: 'web-04', status: 'online', last_seen: '2026-03-01T00:00:00Z',
  created_at: '2026-01-01T00:00:00Z', secret_names: null, secrets_state: null, local_check_problems: null,
  version: '0.1.0-rc.4', version_status: 'current',
};

describe('host rows', () => {
  it('whole row links to the host page', async () => {
    mockApi(rowRoutes('operator'));
    renderPage(newClient());

    const link = await screen.findByRole('link', { name: 'web-04.fra' });
    expect(link).toHaveAttribute('href', '/o/acme/hosts/h1');
    const row = rowOf('web-04.fra');
    expect(row).toHaveTextContent('fra');
    expect(row).toHaveTextContent('Managed');
    expect(row).toHaveTextContent('Monitored');
  });

  it('puts the name first, with the agent state dot in its cell', async () => {
    mockApi(rowRoutes('operator'));
    renderPage(newClient());

    const link = await screen.findByRole('link', { name: 'web-04.fra' });
    expect(screen.getAllByRole('columnheader')[0]).toHaveTextContent('Host');
    expect(within(link).getByTitle('unknown')).toBeInTheDocument();
  });

  it('shows the address under the name in the comfortable density', async () => {
    mockApi(rowRoutes('operator', MONITORED_HOST, { ...PREFS, density: 'comfortable' }));
    renderPage(newClient());

    const link = await screen.findByRole('link', { name: 'web-04.fra' });
    expect(within(link).getByText('203.0.113.4')).toBeInTheDocument();
  });

  it('leaves the address out in the standard density', async () => {
    mockApi(rowRoutes('operator'));
    renderPage(newClient());

    await screen.findByRole('link', { name: 'web-04.fra' });
    expect(screen.queryByText('203.0.113.4')).not.toBeInTheDocument();
  });

  it('links a host with active runs to its live runs, and shows a dash for none', async () => {
    mockApi({
      ...rowRoutes('operator'),
      'GET /api/v1/orgs/acme/hosts': { hosts: [{ ...MONITORED_HOST, active_runs: 2 }, DB_01], next_cursor: '', total: 2 },
    });
    renderPage(newClient());

    const runs = await screen.findByRole('link', { name: '2 running' });
    expect(runs).toHaveAttribute('href', '/o/acme/runs?host=h1&status=active');
    expect(runs).toHaveClass('relative', 'z-10');
    expect(within(rowOf('db-01.fra')).getByText('—')).toBeInTheDocument();
  });

  it('hides the agent version and the operating system by default', async () => {
    mockApi({
      ...rowRoutes('operator', { ...MONITORED_HOST, facts: { os_version: 'debian 13' } }),
      'GET /api/v1/orgs/acme/agents': { agents: [AGENT] },
    });
    renderPage(newClient());

    await screen.findByRole('link', { name: 'web-04.fra' });
    expect(screen.queryByText('debian 13')).not.toBeInTheDocument();
    expect(screen.queryByRole('columnheader', { name: 'Agent version' })).not.toBeInTheDocument();
  });

  it('shows the agent version and the operating system when they are chosen', async () => {
    mockApi({
      ...rowRoutes('operator', { ...MONITORED_HOST, facts: { os_version: 'debian 13' } }, {
        ...PREFS,
        lists: { hosts: { columns: ['name', 'agent_version', 'os'] } },
      }),
      'GET /api/v1/orgs/acme/agents': { agents: [AGENT] },
    });
    renderPage(newClient());

    expect(await screen.findByText('debian 13')).toBeInTheDocument();
    expect(await screen.findByText('0.1.0-rc.4')).toBeInTheDocument();
    expect(screen.getAllByRole('columnheader').map((h) => h.textContent)).toEqual(['Host', 'Agent version', 'Operating system', 'Actions']);
  });

  it('shows muted "not managed" / "not monitored" for a host without the capabilities', async () => {
    mockApi(rowRoutes('operator', { ...WEB_04, managed: false, monitored: false }));
    renderPage(newClient());

    await screen.findByRole('link', { name: 'web-04.fra' });
    const row = rowOf('web-04.fra');
    expect(row).toHaveTextContent('not managed');
    expect(row).toHaveTextContent('not monitored');
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
    const { router } = renderPage(newClient());

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
      ...rowRoutes('operator', { ...WEB_04, managed: false }),
      'PATCH /api/v1/orgs/acme/hosts/h1/capabilities': (req: MockRequest) => {
        patched = req.body;
        return { host: WEB_04 };
      },
    });
    renderPage(newClient());

    await user.click(await screen.findByRole('button', { name: 'Actions for web-04.fra' }));
    await user.click(await screen.findByRole('menuitem', { name: 'Switch management on' }));

    await waitFor(() => expect(patched).toEqual({ managed: true }));
  });

  it('says so in a toast when a switch fails', async () => {
    const user = userEvent.setup();
    mockApi({
      ...rowRoutes('operator'),
      'PATCH /api/v1/orgs/acme/hosts/h1/capabilities': () => new Response(JSON.stringify({ detail: 'nope' }), { status: 500 }),
    });
    renderPage(newClient());

    await user.click(await screen.findByRole('button', { name: 'Actions for web-04.fra' }));
    await user.click(await screen.findByRole('menuitem', { name: 'Switch monitoring off' }));

    expect(await screen.findByText('nope')).toBeInTheDocument();
  });

  it('capabilities in the list are not interactive', async () => {
    mockApi(rowRoutes('admin'));
    renderPage(newClient());

    await screen.findByRole('link', { name: 'web-04.fra' });
    expect(screen.queryAllByRole('switch')).toHaveLength(0);
    expect(screen.queryAllByRole('checkbox')).toHaveLength(0);
    expect(screen.queryByRole('button', { name: /enable/ })).not.toBeInTheDocument();
  });

  it('viewer sees only Open', async () => {
    const user = userEvent.setup();
    mockApi(rowRoutes('viewer'));
    renderPage(newClient());

    await user.click(await screen.findByRole('button', { name: 'Actions for web-04.fra' }));
    const items = await screen.findAllByRole('menuitem');
    expect(items.map((i) => i.textContent)).toEqual(['Open']);
  });

  it('operator sees every item, in order', async () => {
    const user = userEvent.setup();
    mockApi(rowRoutes('operator'));
    renderPage(newClient());

    await user.click(await screen.findByRole('button', { name: 'Actions for web-04.fra' }));
    const items = await screen.findAllByRole('menuitem');
    expect(items.map((i) => i.textContent)).toEqual([
      'Open',
      'Edit',
      'Run command',
      'Run template…',
      'Switch monitoring off',
      'Switch management off',
      'Delete…',
    ]);
  });

  it('offers the host\'s active runs in its menu when it has some', async () => {
    const user = userEvent.setup();
    mockApi(rowRoutes('viewer', { ...MONITORED_HOST, active_runs: 2 }));
    const { router } = renderPage(newClient());

    await user.click(await screen.findByRole('button', { name: 'Actions for web-04.fra' }));
    const items = await screen.findAllByRole('menuitem');
    expect(items.map((i) => i.textContent)).toEqual(['Open', 'View active runs (2)']);
    await user.click(items[1]);

    expect(await screen.findByText('RUNS PAGE')).toBeInTheDocument();
    expect(router.state.location.search).toEqual({ host: 'h1', status: 'active' });
  });

  it('opens the same entries on a right-click, after "Open in new tab" and "Copy link"', async () => {
    mockApi(rowRoutes('operator'));
    renderPage(newClient());

    await screen.findByRole('link', { name: 'web-04.fra' });
    fireEvent.contextMenu(within(rowOf('web-04.fra')).getByText('fra'), { clientX: 200, clientY: 300 });

    const items = within(await screen.findByRole('menu')).getAllByRole('menuitem');
    expect(items.map((i) => i.textContent)).toEqual([
      'Open in new tab',
      'Copy link',
      'Open',
      'Edit',
      'Run command',
      'Run template…',
      'Switch monitoring off',
      'Switch management off',
      'Delete…',
    ]);
  });

  it('offers no Run command for a host that is not managed', async () => {
    const user = userEvent.setup();
    mockApi(rowRoutes('operator', { ...WEB_04, managed: false }));
    renderPage(newClient());

    await user.click(await screen.findByRole('button', { name: 'Actions for web-04.fra' }));
    await screen.findAllByRole('menuitem');
    expect(screen.queryByRole('menuitem', { name: 'Run command' })).not.toBeInTheDocument();
    expect(screen.queryByRole('menuitem', { name: 'Run template…' })).not.toBeInTheDocument();
  });

  it('Open navigates to the host page', async () => {
    const user = userEvent.setup();
    mockApi(rowRoutes('viewer'));
    const { router } = renderPage(newClient());

    await user.click(await screen.findByRole('button', { name: 'Actions for web-04.fra' }));
    await user.click(await screen.findByRole('menuitem', { name: 'Open' }));

    expect(await screen.findByText('HOST PAGE')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/o/acme/hosts/h1');
  });

  it('Edit opens the host dialog without leaving the list', async () => {
    const user = userEvent.setup();
    mockApi(rowRoutes('operator'));
    const { router } = renderPage(newClient());

    await user.click(await screen.findByRole('button', { name: 'Actions for web-04.fra' }));
    await user.click(await screen.findByRole('menuitem', { name: 'Edit' }));

    expect(await screen.findByRole('dialog', { name: 'Edit host web-04.fra' })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/o/acme/hosts');
  });

  it('Run command opens the ad-hoc dialog with this host selected', async () => {
    const user = userEvent.setup();
    mockApi(rowRoutes('operator'));
    renderPage(newClient());

    await user.click(await screen.findByRole('button', { name: 'Actions for web-04.fra' }));
    await user.click(await screen.findByRole('menuitem', { name: 'Run command' }));

    const dialog = await screen.findByRole('dialog', { name: 'Run command' });
    expect(await within(dialog).findByRole('checkbox', { name: 'web-04.fra' })).toBeChecked();
    expect(within(dialog).getByText('1 host(s) selected')).toBeInTheDocument();
  });

  it('Run template opens the template dialog for this host', async () => {
    const user = userEvent.setup();
    mockApi({ ...rowRoutes('operator'), 'GET /api/v1/orgs/acme/job-templates': { templates: [] } });
    renderPage(newClient());

    await user.click(await screen.findByRole('button', { name: 'Actions for web-04.fra' }));
    await user.click(await screen.findByRole('menuitem', { name: 'Run template…' }));

    expect(await screen.findByRole('dialog', { name: 'Run template on web-04.fra' })).toBeInTheDocument();
  });

  it('Delete asks first, then deletes and stays on the list', async () => {
    const user = userEvent.setup();
    let deleted = false;
    mockApi({
      ...rowRoutes('operator'),
      'DELETE /api/v1/orgs/acme/hosts/h1': () => {
        deleted = true;
        return {};
      },
    });
    const { router } = renderPage(newClient());

    await user.click(await screen.findByRole('button', { name: 'Actions for web-04.fra' }));
    await user.click(await screen.findByRole('menuitem', { name: 'Delete…' }));

    expect(await acceptConfirm(user)).toBe('Delete host web-04.fra? This cannot be undone.');
    await waitFor(() => expect(deleted).toBe(true));
    expect(await screen.findByText('Host web-04.fra deleted')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/o/acme/hosts');
  });

  it('Delete does nothing when the confirmation is declined', async () => {
    const user = userEvent.setup();
    let deleted = false;
    mockApi({
      ...rowRoutes('operator'),
      'DELETE /api/v1/orgs/acme/hosts/h1': () => {
        deleted = true;
        return {};
      },
    });
    renderPage(newClient());

    await user.click(await screen.findByRole('button', { name: 'Actions for web-04.fra' }));
    await user.click(await screen.findByRole('menuitem', { name: 'Delete…' }));

    await declineConfirm(user);
    await settled();
    expect(deleted).toBe(false);
  });
  // Backlog 3.57: a client whose agent is offline is frozen by the manager,
  // so the list says "Off" with when it was last seen; an online client is
  // a server plus a small "Client" tag; a server stays exactly as it was,
  // its offline agent included.
  describe('client hosts', () => {
    const hostOf = (id: string, name: string, kind: 'server' | 'client') => ({ ...DB_01, id, name, kind, monitored: true });
    const agent = (hostId: string, status: 'online' | 'offline', lastSeen: string) => ({
      host_id: hostId, host_name: hostId, agent_name: hostId, status, last_seen: lastSeen, created_at: '2026-01-01T00:00:00Z',
      secret_names: null, secrets_state: null, local_check_problems: null, version_status: 'current',
    });
    const threeHoursAgo = () => new Date(Date.now() - 3 * 3600_000).toISOString();

    function routes() {
      return {
        'GET /api/v1/auth/me/preferences': PREFS,
        'GET /api/v1/orgs/acme/hosts': {
          hosts: [hostOf('h-off', 'laptop-1', 'client'), hostOf('h-on', 'laptop-2', 'client'), hostOf('h-srv', 'web-1', 'server')],
          next_cursor: '',
          total: 3,
        },
        'GET /api/v1/orgs/acme/zones': ZONES,
        'GET /api/v1/orgs/acme/agents': {
          agents: [agent('h-off', 'offline', threeHoursAgo()), agent('h-on', 'online', new Date().toISOString()), agent('h-srv', 'offline', threeHoursAgo())],
        },
      };
    }

    it('shows an offline client as Off with when it was last seen', async () => {
      mockApi(routes());
      renderPage(newClient());
      await screen.findByRole('link', { name: 'laptop-1, Off' });

      expect(within(rowOf('laptop-1, Off')).getByText('Off')).toBeInTheDocument();
      expect(within(rowOf('laptop-1, Off')).getByText('last seen 3 h ago')).toBeInTheDocument();
      expect(within(rowOf('laptop-1, Off')).queryByText('Client')).not.toBeInTheDocument();
    });

    it('tags an online client and leaves a server with an offline agent alone', async () => {
      mockApi(routes());
      renderPage(newClient());
      await screen.findByRole('link', { name: 'laptop-1, Off' });

      expect(within(rowOf('laptop-2, Client')).getByText('Client')).toBeInTheDocument();
      expect(within(rowOf('laptop-2, Client')).queryByText('Off')).not.toBeInTheDocument();
      expect(within(rowOf('web-1')).queryByText('Off')).not.toBeInTheDocument();
      expect(within(rowOf('web-1')).queryByText('Client')).not.toBeInTheDocument();
      expect(within(rowOf('web-1')).queryByText(/last seen/)).not.toBeInTheDocument();
    });

    // The badge sits inside the row's link, whose label is all a screen
    // reader announces for it: the presence has to be in the label.
    it('names a client\'s presence in its row link, and keeps the menu named by the host', async () => {
      mockApi(routes());
      renderPage(newClient());

      expect(await screen.findByRole('link', { name: 'laptop-1, Off' })).toHaveAttribute('href', '/o/acme/hosts/h-off');
      expect(screen.getByRole('link', { name: 'laptop-2, Client' })).toBeInTheDocument();
      expect(screen.getByRole('link', { name: 'web-1' })).toBeInTheDocument();
      expect(screen.getByRole('button', { name: 'Actions for laptop-1' })).toBeInTheDocument();
    });
  });
});
