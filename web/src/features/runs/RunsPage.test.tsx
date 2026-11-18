import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { RunsPage } from './RunsPage.tsx';
import { validateRunsSearch } from './runsSearch.ts';

const job = (over: Record<string, unknown> = {}) => ({
  id: '0197c0a2-0000-7000-8000-000000000001', zone_id: '0197c0b1-0000-7000-8000-000000000002',
  kind: 'run', status: 'succeeded', run_group_id: 'g1', trigger: 'manual', created_at: '2026-09-15T08:00:00Z', ...over,
});

function renderPage(initial = '/o/acme/runs') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const listRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/runs',
    validateSearch: validateRunsSearch,
    component: RunsPage,
  });
  const detailRoute = createRoute({ getParentRoute: () => orgRoute, path: '/runs/$id', component: () => <div>RUN DETAIL</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([listRoute, detailRoute])]),
    history: createMemoryHistory({ initialEntries: [initial] }),
  });
  render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
  return router;
}

const PREFS = { density: 'standard', page_size: 25, lists: {} };

/** n finished runs, newest first, with distinct ids. */
function manyJobs(n: number) {
  return Array.from({ length: n }, (_, i) => job({ id: `0197c0a2-0000-7000-8000-${String(i + 1).padStart(12, '0')}` }));
}

/** The answer for one page of `all`, as the server pages it. */
function pageOf(all: ReturnType<typeof manyJobs>, req: MockRequest) {
  const page = Number(req.search.get('page') ?? '1');
  const perPage = Number(req.search.get('per_page') ?? '25');
  return { jobs: all.slice((page - 1) * perPage, page * perPage), next_cursor: '', total: all.length };
}

const ORG_OPERATOR = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
const LIVE_ID = '0190abcd-0000-7000-8000-00000000000e';
const ORG_VIEWER = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'viewer', created_at: '' } };

describe('RunsPage', () => {
  it('links to the run requests, with the number waiting', async () => {
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_VIEWER,
      'GET /api/v1/orgs/acme/jobs': { jobs: [], next_cursor: '', total: 0 },
      'GET /api/v1/orgs/acme/run-requests': { requests: [{ id: 'a' }] },
    });
    renderPage();
    const link = await screen.findByRole('link', { name: 'Run requests (1 pending)' });
    expect(link).toHaveAttribute('href', '/o/acme/runs/requests');
  });

  it('lists runs with kind, status colours, template, zone and trigger', async () => {
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': {
        jobs: [
          job({ template_id: '0197c0c1-0000-7000-8000-000000000003' }),
          job({ id: '0197c0a2-0000-7000-8000-00000000000a', kind: 'connection_test', status: 'failed', template_id: undefined }),
        ],
        next_cursor: '',
        total: 2,
      },
    });
    renderPage();
    expect(await screen.findByText('0197c0c1')).toBeInTheDocument();
    const kindCell = screen.getAllByText('connection test').find((el) => el.tagName !== 'OPTION');
    expect(kindCell).toBeDefined();
    const failed = screen.getAllByText('failed').find((el) => el.tagName !== 'OPTION');
    expect(failed?.style.color).toBe('var(--px-crit)');
    expect(screen.getByText('0197c0c1')).toBeInTheDocument();
    const runCell = screen.getAllByText('run').find((el) => el.tagName !== 'OPTION');
    expect(runCell).toBeDefined();
  });

  it('filters by status through the URL', async () => {
    const seen: string[] = [];
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': (req: { search: URLSearchParams }) => {
        seen.push(req.search.toString());
        return { jobs: [], next_cursor: '', total: 0 };
      },
    });
    renderPage();
    await screen.findByLabelText('Filter by status');
    await pickOption(screen, 'Filter by status', { value: 'failed' });
    expect(await screen.findByText('No runs match.')).toBeInTheDocument();
    expect(seen.some((q) => q.includes('status=failed'))).toBe(true);
  });

  it('asks for page 1 at the saved size and shows the total in the footer', async () => {
    const seen: URLSearchParams[] = [];
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
        seen.push(req.search);
        return pageOf(manyJobs(30), req);
      },
    });
    renderPage();
    expect(await screen.findByText('1–25 of 30 runs')).toBeInTheDocument();
    expect(seen).toHaveLength(1);
    expect(seen[0].get('page')).toBe('1');
    expect(seen[0].get('per_page')).toBe('25');
    expect(seen[0].has('sort')).toBe(false);
    expect(seen[0].has('cursor')).toBe(false);
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument();
  });

  it('labels the created time "Created", sorts by it, and leaves Template unsortable', async () => {
    const user = userEvent.setup();
    const seen: URLSearchParams[] = [];
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
        seen.push(req.search);
        return { jobs: [job()], next_cursor: '', total: 1 };
      },
    });
    const router = renderPage();
    await screen.findByRole('link', { name: '0197c0a2' });
    const headers = screen.getAllByRole('columnheader').map((h) => h.textContent);
    expect(headers).toContain('Created');
    expect(headers).not.toContain('Started');
    expect(screen.queryByRole('button', { name: 'Template' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Run' })).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Created' }));
    await waitFor(() => expect(seen.at(-1)?.get('sort')).toBe('created'));
    expect(seen.at(-1)?.get('order')).toBe('asc');
    expect(router.state.location.search).toMatchObject({ sort: 'created', order: 'asc' });
  });

  it('shows Created as a relative time with the full date as its title', async () => {
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': { jobs: [job()], next_cursor: '', total: 1 },
    });
    renderPage();
    const link = await screen.findByRole('link', { name: '0197c0a2' });
    const created = (link.closest('[role="row"]') as HTMLElement).querySelector('[title]') as HTMLElement;
    expect(created.getAttribute('title')).toMatch(/2026/);
    expect(created.textContent).toMatch(/ago|year|month/);
    expect(created.textContent).not.toMatch(/2026-09-15/);
  });

  it.each([
    ['Status', 'status'],
    ['Kind', 'kind'],
    ['Trigger', 'trigger'],
  ])('sorts by %s on the server', async (header, key) => {
    const user = userEvent.setup();
    const seen: URLSearchParams[] = [];
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
        seen.push(req.search);
        return { jobs: [job()], next_cursor: '', total: 1 };
      },
    });
    renderPage();
    await screen.findByRole('link', { name: '0197c0a2' });
    await user.click(screen.getByRole('button', { name: header }));
    await waitFor(() => expect(seen.at(-1)?.get('sort')).toBe(key));
  });

  it('offers the live runs as Active in the status filter and asks for status=active', async () => {
    const seen: URLSearchParams[] = [];
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
        seen.push(req.search);
        return { jobs: [], next_cursor: '', total: 0 };
      },
    });
    const router = renderPage();
    await screen.findByLabelText('Filter by status');
    await pickOption(screen, 'Filter by status', 'Active');
    await waitFor(() => expect(seen.at(-1)?.get('status')).toBe('active'));
    expect(router.state.location.search).toMatchObject({ status: 'active' });
  });

  it('reads a host\'s live runs from the URL, as the hosts list links them', async () => {
    const seen: URLSearchParams[] = [];
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
        seen.push(req.search);
        return { jobs: [job({ status: 'running' })], next_cursor: '', total: 1 };
      },
      'GET /api/v1/orgs/acme/hosts/h1': { host: { id: 'h1', name: 'web-04.fra', zone_id: 'z1', address: '10.0.0.4' } },
    });
    renderPage('/o/acme/runs?host=h1&status=active');
    expect(await screen.findByText('Host: web-04.fra')).toBeInTheDocument();
    expect(seen[0].get('status')).toBe('active');
    expect(seen[0].get('host_id')).toBe('h1');
  });

  it('keeps the sort and starts again at page 1 when a filter changes', async () => {
    const seen: URLSearchParams[] = [];
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
        seen.push(req.search);
        return pageOf(manyJobs(30), req);
      },
    });
    const router = renderPage('/o/acme/runs?sort=kind&order=desc&page=2');
    expect(await screen.findByText('26–30 of 30 runs')).toBeInTheDocument();
    await pickOption(screen, 'Filter by status', { value: 'failed' });
    await waitFor(() => expect(seen.at(-1)?.get('status')).toBe('failed'));
    expect(seen.at(-1)?.get('page')).toBe('1');
    expect(seen.at(-1)?.get('sort')).toBe('kind');
    expect(seen.at(-1)?.get('order')).toBe('desc');
    expect(router.state.location.search).toEqual({ sort: 'kind', order: 'desc', status: 'failed' });
  });

  it('offers Run command to an operator and opens the ad-hoc dialog', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': { jobs: [], next_cursor: '', total: 0 },
      'GET /api/v1/orgs/acme/hosts': { hosts: [], next_cursor: '' },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Run command' }));
    expect(await screen.findByRole('dialog', { name: 'Run command' })).toBeInTheDocument();
  });

  it('hides Run command from a viewer', async () => {
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_VIEWER,
      'GET /api/v1/orgs/acme/jobs': { jobs: [], next_cursor: '', total: 0 },
    });
    renderPage();
    await screen.findByText('No runs match.');
    expect(screen.queryByRole('button', { name: 'Run command' })).not.toBeInTheDocument();
  });

  it('filters by trigger and, for scheduled runs, by schedule', async () => {
    const seen: string[] = [];
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
        seen.push(req.search.toString());
        return { jobs: [job({ trigger: 'schedule' })], next_cursor: '', total: 1 };
      },
      'GET /api/v1/orgs/acme/schedules': { schedules: [{ id: 's1', name: 'Nightly' }] },
    });
    renderPage();
    expect(await screen.findByText('Schedule', { selector: 'span' })).toBeInTheDocument();
    await pickOption(screen, 'Filter by trigger', { value: 'schedule' });
    await screen.findByLabelText('Filter by schedule');
    await pickOption(screen, 'Filter by schedule', { value: 's1' });
    expect(seen.some((q) => q.includes('trigger=schedule') && q.includes('trigger_ref=s1'))).toBe(true);
    await pickOption(screen, 'Filter by trigger', { value: 'webhook' });
    expect(screen.queryByLabelText('Filter by schedule')).not.toBeInTheDocument();
  });

  it('filters by host from the URL, names it in a chip and drops it when the chip is removed', async () => {
    const user = userEvent.setup();
    const seen: string[] = [];
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
        seen.push(req.search.toString());
        return { jobs: [job()], next_cursor: '', total: 1 };
      },
      'GET /api/v1/orgs/acme/hosts/h1': { host: { id: 'h1', name: 'web-04.fra', zone_id: 'z1', address: '10.0.0.4' } },
    });
    renderPage('/o/acme/runs?host=h1');
    expect(await screen.findByText('Host: web-04.fra')).toBeInTheDocument();
    expect(seen.some((q) => q.includes('host_id=h1'))).toBe(true);

    await user.click(screen.getByRole('button', { name: 'Remove host filter' }));
    await waitFor(() => expect(screen.queryByText('Host: web-04.fra')).not.toBeInTheDocument());
    expect(seen[seen.length - 1]).not.toContain('host_id=h1');
  });

  it('keeps the host filter when another filter changes', async () => {
    const seen: string[] = [];
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
        seen.push(req.search.toString());
        return { jobs: [], next_cursor: '', total: 0 };
      },
      'GET /api/v1/orgs/acme/hosts/h1': { host: { id: 'h1', name: 'web-04.fra', zone_id: 'z1', address: '10.0.0.4' } },
    });
    renderPage('/o/acme/runs?host=h1');
    await screen.findByLabelText('Filter by status');
    await pickOption(screen, 'Filter by status', { value: 'failed' });
    await waitFor(() => expect(seen.some((q) => q.includes('status=failed') && q.includes('host_id=h1'))).toBe(true));
  });

  it('drops the schedule from the URL when the trigger changes away from schedule', async () => {
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': { jobs: [], next_cursor: '', total: 0 },
      'GET /api/v1/orgs/acme/schedules': { schedules: [{ id: 's1', name: 'Nightly' }] },
    });
    const router = renderPage('/o/acme/runs?trigger=schedule&schedule=s1&page=2');
    await screen.findByLabelText('Filter by schedule');
    await pickOption(screen, 'Filter by trigger', { value: 'webhook' });
    await waitFor(() => expect(router.state.location.search).toEqual({ trigger: 'webhook' }));
  });

  it('ignores a status the list does not offer', async () => {
    const seen: URLSearchParams[] = [];
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
        seen.push(req.search);
        return { jobs: [], next_cursor: '', total: 0 };
      },
    });
    renderPage('/o/acme/runs?status=exploded');
    await screen.findByText('No runs match.');
    expect(seen[0].has('status')).toBe(false);
    expect(screen.queryByRole('button', { name: 'Reset every filter' })).not.toBeInTheDocument();
  });

  it('brings the previous filter back on Back', async () => {
    const seen: URLSearchParams[] = [];
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
        seen.push(req.search);
        return { jobs: [], next_cursor: '', total: 0 };
      },
    });
    const router = renderPage('/o/acme/runs?kind=bootstrap');
    await screen.findByLabelText('Filter by status');
    await pickOption(screen, 'Filter by status', { value: 'failed' });
    await waitFor(() => expect(router.state.location.search).toEqual({ kind: 'bootstrap', status: 'failed' }));

    router.history.back();

    await waitFor(() => expect(router.state.location.search).toEqual({ kind: 'bootstrap' }));
    await waitFor(() => expect(seen.at(-1)?.has('status')).toBe(false));
    expect(seen.at(-1)?.get('kind')).toBe('bootstrap');
  });

  it('resets every filter, the host included, and keeps the sort', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': { jobs: [], next_cursor: '', total: 0 },
      'GET /api/v1/orgs/acme/hosts/h1': { host: { id: 'h1', name: 'web-04.fra', zone_id: 'z1', address: '10.0.0.4' } },
    });
    const router = renderPage('/o/acme/runs?sort=kind&host=h1&status=failed&kind=run');
    await screen.findByText('Host: web-04.fra');

    await user.click(screen.getByRole('button', { name: 'Reset every filter' }));

    await waitFor(() => expect(router.state.location.search).toEqual({ sort: 'kind' }));
    expect(screen.queryByText('Host: web-04.fra')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Reset every filter' })).not.toBeInTheDocument();
  });

  it('each run row is a link to its run', async () => {
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': { jobs: [job()], next_cursor: '', total: 1 },
    });
    renderPage();
    const link = await screen.findByRole('link', { name: '0197c0a2' });
    expect(link).toHaveAttribute('href', '/o/acme/runs/0197c0a2-0000-7000-8000-000000000001');
    expect(link.closest('[role="row"]')).toHaveTextContent('succeeded');
    expect(within(link).queryAllByRole('link')).toEqual([]);
    expect(within(link).queryAllByRole('button')).toEqual([]);
  });

  it('cancels a live run from its row menu without navigating', async () => {
    const user = userEvent.setup();
    const cancelled: string[] = [];
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': { jobs: [job({ id: LIVE_ID, status: 'running' })], next_cursor: '', total: 1 },
      [`POST /api/v1/orgs/acme/jobs/${LIVE_ID}/cancel`]: (req: MockRequest) => {
        cancelled.push(req.path);
        return { job: { id: LIVE_ID, status: 'cancelling' } };
      },
    });
    const router = renderPage();
    await user.click(await screen.findByRole('button', { name: 'Actions for 0190abcd' }));
    await user.click(screen.getByRole('menuitem', { name: 'Cancel run' }));
    expect(await screen.findByText('Cancellation requested')).toBeInTheDocument();
    expect(cancelled).toEqual([`/api/v1/orgs/acme/jobs/${LIVE_ID}/cancel`]);
    expect(router.state.location.pathname).toBe('/o/acme/runs');
    expect(screen.queryByText('RUN DETAIL')).not.toBeInTheDocument();
  });

  it('disables Cancel run while its cancellation is pending', async () => {
    const user = userEvent.setup();
    let posts = 0;
    let release: () => void = () => undefined;
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': { jobs: [job({ id: LIVE_ID, status: 'running' })], next_cursor: '', total: 1 },
      [`POST /api/v1/orgs/acme/jobs/${LIVE_ID}/cancel`]: async () => {
        posts++;
        await new Promise<void>((resolve) => (release = resolve));
        return { job: { id: LIVE_ID, status: 'cancelling' } };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Actions for 0190abcd' }));
    await user.click(screen.getByRole('menuitem', { name: 'Cancel run' }));
    await waitFor(() => expect(posts).toBe(1));

    // Reopened while the request is in flight: the item is disabled, a repeated
    // pick sends nothing.
    await user.click(screen.getByRole('button', { name: 'Actions for 0190abcd' }));
    const item = await screen.findByRole('menuitem', { name: 'Cancel run' });
    expect(item).toHaveAttribute('aria-disabled', 'true');
    await user.click(item);
    expect(posts).toBe(1);

    release();
    expect(await screen.findByText('Cancellation requested')).toBeInTheDocument();
  });

  it('opens the run from its row menu', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': { jobs: [job({ id: LIVE_ID, status: 'running' })], next_cursor: '', total: 1 },
    });
    const router = renderPage();
    await user.click(await screen.findByRole('button', { name: 'Actions for 0190abcd' }));
    await user.click(screen.getByRole('menuitem', { name: 'Open' }));
    expect(await screen.findByText('RUN DETAIL')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe(`/o/acme/runs/${LIVE_ID}`);
  });

  it.each([
    ['a finished run', ORG_OPERATOR, 'succeeded'],
    ['a viewer', ORG_VIEWER, 'running'],
  ])('offers no Cancel for %s', async (_label, orgBody, status) => {
    const user = userEvent.setup();
    let orgRead = false;
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme': () => {
        orgRead = true;
        return orgBody;
      },
      'GET /api/v1/orgs/acme/jobs': { jobs: [job({ id: LIVE_ID, status })], next_cursor: '', total: 1 },
    });
    renderPage();
    const trigger = await screen.findByRole('button', { name: 'Actions for 0190abcd' });
    await waitFor(() => expect(orgRead).toBe(true));
    await user.click(trigger);
    const items = within(screen.getByRole('menu')).getAllByRole('menuitem');
    expect(items.map((i) => i.textContent)).toEqual(['Open']);
  });
});
