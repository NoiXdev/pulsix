import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { AdhocDialog } from './AdhocDialog.tsx';

const host = (id: string, name: string) => ({ id, name, zone_id: 'z', address: '', managed: true, monitored: false, facts: {}, created_at: '' });

function renderDialog(onClose = () => undefined, initialHostIds?: string[]) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/o/$org',
    component: () => (
      <>
        <AdhocDialog org="acme" onClose={onClose} initialHostIds={initialHostIds} />
        <Outlet />
      </>
    ),
  });
  const runRoute = createRoute({ getParentRoute: () => orgRoute, path: '/runs/$id', component: () => <div>RUN VIEW</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([runRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('AdhocDialog', () => {
  it('starts with the given hosts selected', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'GET /api/v1/orgs/acme/hosts': { hosts: [host('h1', 'web-01'), host('h2', 'web-02')], next_cursor: '' },
      'POST /api/v1/orgs/acme/hosts/command': (req: MockRequest) => {
        posted = req.body;
        return { jobs: [{ id: 'job-1', zone_id: 'z', kind: 'adhoc', status: 'queued', trigger: 'manual', run_group_id: 'g1', created_at: '2026-09-16T00:00:00Z' }] };
      },
    });
    renderDialog(undefined, ['h2']);
    const dialog = await screen.findByRole('dialog', { name: 'Run command' });
    expect(await within(dialog).findByRole('checkbox', { name: 'web-02' })).toBeChecked();
    expect(within(dialog).getByRole('checkbox', { name: 'web-01' })).not.toBeChecked();
    await user.type(within(dialog).getByLabelText('Command'), 'uptime');
    await user.click(within(dialog).getByRole('button', { name: 'Run command' }));
    expect(posted).toEqual({ host_ids: ['h2'], module: 'shell', argv: 'uptime' });
  });

  it('shows a preselected host that is not on the loaded page as a pinned, checked row', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      // h9 sorts past the first page, so the list never carries it.
      'GET /api/v1/orgs/acme/hosts': { hosts: [host('h1', 'web-01'), host('h2', 'web-02')], next_cursor: 'c1' },
      'GET /api/v1/orgs/acme/hosts/h9': { host: host('h9', 'zz-db-09') },
      'POST /api/v1/orgs/acme/hosts/command': (req: MockRequest) => {
        posted = req.body;
        return { jobs: [{ id: 'job-1', zone_id: 'z', kind: 'adhoc', status: 'queued', trigger: 'manual', run_group_id: 'g1', created_at: '2026-09-16T00:00:00Z' }] };
      },
    });
    renderDialog(undefined, ['h9']);
    const dialog = await screen.findByRole('dialog', { name: 'Run command' });
    const pinned = await within(dialog).findByRole('checkbox', { name: 'zz-db-09' });
    expect(pinned).toBeChecked();
    // Pinned above the page, not appended after it.
    const boxes = within(dialog).getAllByRole('checkbox');
    expect(boxes.map((b) => b.getAttribute('aria-label'))).toEqual(['zz-db-09', 'web-01', 'web-02']);
    expect(within(dialog).getByText('1 host(s) selected')).toBeInTheDocument();

    await user.type(within(dialog).getByLabelText('Command'), 'uptime');
    await user.click(within(dialog).getByRole('button', { name: 'Run command' }));
    expect(posted).toEqual({ host_ids: ['h9'], module: 'shell', argv: 'uptime' });
  });

  it('runs a shell command on the selected hosts and lands on the run view', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'GET /api/v1/orgs/acme/hosts': { hosts: [host('h1', 'web-01'), host('h2', 'web-02')], next_cursor: '' },
      'POST /api/v1/orgs/acme/hosts/command': (req: MockRequest) => {
        posted = req.body;
        return { jobs: [{ id: 'job-1', zone_id: 'z', kind: 'adhoc', status: 'queued', trigger: 'manual', run_group_id: 'g1', created_at: '2026-09-16T00:00:00Z' }] };
      },
    });
    renderDialog();
    const dialog = await screen.findByRole('dialog', { name: 'Run command' });
    await user.click(await within(dialog).findByRole('checkbox', { name: 'web-01' }));
    await user.type(within(dialog).getByLabelText('Command'), 'uptime');
    await user.click(within(dialog).getByRole('button', { name: 'Run command' }));
    expect(posted).toEqual({ host_ids: ['h1'], module: 'shell', argv: 'uptime' });
    expect(await screen.findByText('RUN VIEW')).toBeInTheDocument();
  });

  it('selects several hosts and sends every one of them', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'GET /api/v1/orgs/acme/hosts': { hosts: [host('h1', 'web-01'), host('h2', 'web-02')], next_cursor: '' },
      'POST /api/v1/orgs/acme/hosts/command': (req: MockRequest) => {
        posted = req.body;
        return { jobs: [{ id: 'job-1', zone_id: 'z', kind: 'adhoc', status: 'queued', trigger: 'manual', run_group_id: 'g1', created_at: '2026-09-16T00:00:00Z' }] };
      },
    });
    renderDialog();
    const dialog = await screen.findByRole('dialog', { name: 'Run command' });
    await user.click(await within(dialog).findByRole('checkbox', { name: 'web-01' }));
    await user.click(within(dialog).getByRole('checkbox', { name: 'web-02' }));
    expect(within(dialog).getByText('2 host(s) selected')).toBeInTheDocument();
    await user.type(within(dialog).getByLabelText('Command'), 'echo hi');
    await user.click(within(dialog).getByRole('button', { name: 'Run command' }));
    expect(posted).toEqual({ host_ids: ['h1', 'h2'], module: 'shell', argv: 'echo hi' });
  });

  it('refuses to submit without a host or a command', async () => {
    const user = userEvent.setup();
    const posted: unknown[] = [];
    mockApi({
      'GET /api/v1/orgs/acme/hosts': { hosts: [host('h1', 'web-01')], next_cursor: '' },
      'POST /api/v1/orgs/acme/hosts/command': (req: MockRequest) => {
        posted.push(req.body);
        return { jobs: [] };
      },
    });
    renderDialog();
    const dialog = await screen.findByRole('dialog', { name: 'Run command' });
    await user.click(within(dialog).getByRole('button', { name: 'Run command' }));
    expect(await within(dialog).findByText(/choose at least one host/i)).toBeInTheDocument();
    expect(posted).toEqual([]);

    await user.click(await within(dialog).findByRole('checkbox', { name: 'web-01' }));
    await user.click(within(dialog).getByRole('button', { name: 'Run command' }));
    expect(await within(dialog).findByText(/a command is required/i)).toBeInTheDocument();
    expect(posted).toEqual([]);
  });

  it('every managed host stays reachable through pagination', async () => {
    const user = userEvent.setup();
    const pages: Record<string, { hosts: ReturnType<typeof host>[]; next_cursor: string }> = {
      '': { hosts: [host('h1', 'web-01')], next_cursor: 'page-2' },
      'page-2': { hosts: [host('h2', 'web-02')], next_cursor: '' },
    };
    let posted: unknown;
    mockApi({
      'GET /api/v1/orgs/acme/hosts': (req: MockRequest) => pages[req.search.get('cursor') ?? ''],
      'POST /api/v1/orgs/acme/hosts/command': (req: MockRequest) => {
        posted = req.body;
        return { jobs: [{ id: 'job-1', zone_id: 'z', kind: 'adhoc', status: 'queued', trigger: 'manual', run_group_id: 'g1', created_at: '2026-09-16T00:00:00Z' }] };
      },
    });
    renderDialog();
    const dialog = await screen.findByRole('dialog', { name: 'Run command' });
    await within(dialog).findByRole('checkbox', { name: 'web-01' });
    expect(within(dialog).queryByRole('checkbox', { name: 'web-02' })).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Load more' }));
    await user.click(await within(dialog).findByRole('checkbox', { name: 'web-02' }));
    await user.type(within(dialog).getByLabelText('Command'), 'uptime');
    await user.click(within(dialog).getByRole('button', { name: 'Run command' }));
    expect(posted).toEqual({ host_ids: ['h2'], module: 'shell', argv: 'uptime' });
  });

  it('shows the server error and keeps the dialog open when the request fails', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme/hosts': { hosts: [host('h1', 'web-01')], next_cursor: '' },
      'POST /api/v1/orgs/acme/hosts/command': () => new Response(JSON.stringify({ detail: 'module "raw" is not supported' }), { status: 422, headers: { 'Content-Type': 'application/json' } }),
    });
    renderDialog();
    const dialog = await screen.findByRole('dialog', { name: 'Run command' });
    await user.click(await within(dialog).findByRole('checkbox', { name: 'web-01' }));
    await user.type(within(dialog).getByLabelText('Command'), 'uptime');
    await user.click(within(dialog).getByRole('button', { name: 'Run command' }));
    expect(await within(dialog).findByText(/not supported/i)).toBeInTheDocument();
  });

  it('keeps the previous host rows on screen while a new search is pending', async () => {
    const user = userEvent.setup();
    let release!: () => void;
    const held = new Promise<void>((resolve) => {
      release = resolve;
    });
    mockApi({
      'GET /api/v1/orgs/acme/hosts': async (req: MockRequest) => {
        if (req.search.get('q') === 'db') {
          await held;
          return { hosts: [host('h3', 'db-01')], next_cursor: '' };
        }
        return { hosts: [host('h1', 'web-01'), host('h2', 'web-02')], next_cursor: '' };
      },
    });
    renderDialog();
    const dialog = await screen.findByRole('dialog', { name: 'Run command' });
    expect(await within(dialog).findByRole('checkbox', { name: 'web-01' })).toBeInTheDocument();

    await user.type(within(dialog).getByRole('searchbox'), 'db');

    // The answer is still outstanding: the list neither empties nor says
    // "no hosts" in the meantime.
    expect(within(dialog).getByRole('checkbox', { name: 'web-01' })).toBeInTheDocument();
    expect(within(dialog).getByRole('checkbox', { name: 'web-02' })).toBeInTheDocument();
    expect(within(dialog).queryByText(/no managed host/i)).not.toBeInTheDocument();

    release();
    expect(await within(dialog).findByRole('checkbox', { name: 'db-01' })).toBeInTheDocument();
    expect(within(dialog).queryByRole('checkbox', { name: 'web-01' })).not.toBeInTheDocument();
  });
});
