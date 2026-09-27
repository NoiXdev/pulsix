import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { MonitoringGroupsPage } from './MonitoringGroupsPage.tsx';

const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });

const group = (over: Record<string, unknown> = {}) => ({
  id: 'g1', name: 'Web tier', selector: 'role=web', priority: 10, created_at: '', updated_at: '', ...over,
});

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const checksRoute = createRoute({ getParentRoute: () => orgRoute, path: '/checks' });
  const indexRoute = createRoute({ getParentRoute: () => checksRoute, path: '/', component: () => <div>CHECKS VIEW</div> });
  const groupsRoute = createRoute({ getParentRoute: () => checksRoute, path: '/monitoring-groups', component: MonitoringGroupsPage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([checksRoute.addChildren([indexRoute, groupsRoute])])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/checks/monitoring-groups'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('MonitoringGroupsPage', () => {
  it('lists groups with their selector and priority', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [group(), group({ id: 'g2', name: 'Databases', selector: 'role=db', priority: 20 })] },
    });
    renderPage();

    expect(await screen.findByText('Web tier')).toBeInTheDocument();
    expect(screen.getByText('role=db')).toBeInTheDocument();
    expect(screen.getByText('20')).toBeInTheDocument();
  });

  it('shows a viewer the groups but no write affordances', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('viewer'),
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [group()] },
    });
    renderPage();

    expect(await screen.findByText('Web tier')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'New group' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Edit Web tier' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Delete Web tier' })).not.toBeInTheDocument();
  });

  it('previews the match count once the typing settles, and never for an empty selector', async () => {
    const user = userEvent.setup();
    let previews = 0;
    let lastSelector: string | undefined;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'POST /api/v1/orgs/acme/monitoring-groups/preview': (req: MockRequest) => {
        previews += 1;
        lastSelector = (req.body as { selector: string }).selector;
        return { count: 2, hosts: [{ id: 'h1', name: 'web-1', monitored: true }, { id: 'h2', name: 'web-2', monitored: false }] };
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'New group' }));
    // An empty selector is not a question worth asking the server.
    expect(previews).toBe(0);

    await user.type(screen.getByLabelText('Selector'), 'role=web');
    expect(await screen.findByText('Matches 2 hosts')).toBeInTheDocument();
    expect(screen.getByText('web-1')).toBeInTheDocument();
    // A matched host that is not monitored would never actually run the checks.
    expect(screen.getByText('web-2 (not monitored)')).toBeInTheDocument();
    // One request for eight keystrokes: the route expands the selector
    // across the whole host table.
    expect(previews).toBe(1);
    expect(lastSelector).toBe('role=web');
  });

  it('says so visibly when a selector matches no host', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'POST /api/v1/orgs/acme/monitoring-groups/preview': { count: 0, hosts: [] },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'New group' }));
    await user.type(screen.getByLabelText('Selector'), 'role=nope');

    const empty = await screen.findByText('No host matches this selector');
    expect(empty).toHaveAttribute('data-no-match', 'true');
  });

  it('reports an unparseable selector with the server’s own message', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'POST /api/v1/orgs/acme/monitoring-groups/preview': () =>
        new Response(JSON.stringify({ detail: 'invalid input: selector: term 2: expected key=value, got "=="' }), {
          status: 400,
          headers: { 'Content-Type': 'application/problem+json' },
        }),
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'New group' }));
    await user.type(screen.getByLabelText('Selector'), 'role=web,==');

    expect(await screen.findByRole('alert')).toHaveTextContent('invalid input: selector: term 2: expected key=value, got "=="');
  });

  it('edits a group, previewing the selector it already has', async () => {
    const user = userEvent.setup();
    let put: unknown;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [group()] },
      'POST /api/v1/orgs/acme/monitoring-groups/preview': { count: 1, hosts: [{ id: 'h1', name: 'web-1', monitored: true }] },
      'PUT /api/v1/orgs/acme/monitoring-groups/g1': (req: MockRequest) => {
        put = req.body;
        return { group: group({ priority: 30 }) };
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Edit Web tier' }));
    // A saved selector is worth resolving straight away: the operator is
    // about to change it and needs to see what it matches now.
    expect(await screen.findByText('Matches 1 host')).toBeInTheDocument();
    await user.clear(screen.getByLabelText('Priority'));
    await user.type(screen.getByLabelText('Priority'), '30');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(put).toEqual({ name: 'Web tier', selector: 'role=web', priority: 30 }));
  });

  it('deletes a group once the operator has confirmed it', async () => {
    const user = userEvent.setup();
    const confirmed = vi.spyOn(window, 'confirm').mockReturnValue(true);
    let deleted = 0;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [group()] },
      'DELETE /api/v1/orgs/acme/monitoring-groups/g1': () => {
        deleted += 1;
        return {};
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Delete Web tier' }));
    await waitFor(() => expect(deleted).toBe(1));
    // The operator is told what else goes with it.
    expect(confirmed.mock.calls[0][0]).toContain('check definitions bound to it are deleted');
    confirmed.mockRestore();
  });

  it('creates a group with the name, selector and priority that were typed', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'POST /api/v1/orgs/acme/monitoring-groups/preview': { count: 1, hosts: [{ id: 'h1', name: 'web-1', monitored: true }] },
      'POST /api/v1/orgs/acme/monitoring-groups': (req: MockRequest) => {
        posted = req.body;
        return { group: group() };
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'New group' }));
    await user.type(screen.getByLabelText('Name'), 'Web tier');
    await user.type(screen.getByLabelText('Selector'), 'role=web');
    await user.clear(screen.getByLabelText('Priority'));
    await user.type(screen.getByLabelText('Priority'), '10');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(posted).toEqual({ name: 'Web tier', selector: 'role=web', priority: 10 }));
  });
});
