import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { MonitoringGroupsPage } from './MonitoringGroupsPage.tsx';
import { clippedElements } from '../../test/clipped.ts';

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
    expect(screen.queryByRole('button', { name: /^Actions for / })).not.toBeInTheDocument();
  });

  it('offers a viewer no row menu', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('viewer'),
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [group(), group({ id: 'g2', name: 'Databases' })] },
    });
    renderPage();

    expect(await screen.findByText('Databases')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Actions for / })).not.toBeInTheDocument();
  });

  it('rows are not links', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [group()] },
    });
    const { container } = renderPage();
    await screen.findByRole('button', { name: 'Actions for Web tier' });
    expect(container.querySelectorAll('[data-list-row]')).toHaveLength(1);
    expect(container.querySelector('[data-list-row] > a')).toBeNull();
  });

  it('no clipped cards', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [group()] },
    });
    const { container } = renderPage();
    await screen.findByRole('button', { name: 'Actions for Web tier' });
    expect(clippedElements(container)).toEqual([]);
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

    // And clearing the field again asks nothing either — the guard has to
    // hold on the debounced setter, not only on the first render's
    // initialiser (review finding 4).
    await user.clear(screen.getByLabelText('Selector'));
    expect(await screen.findByText('Type a selector to see which hosts it matches.')).toBeInTheDocument();
    expect(previews).toBe(1);
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

  it('edits and deletes from the row menu', async () => {
    const user = userEvent.setup();
    const confirmed = vi.spyOn(window, 'confirm').mockReturnValue(true);
    let deleted = 0;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [group()] },
      'POST /api/v1/orgs/acme/monitoring-groups/preview': { count: 1, hosts: [{ id: 'h1', name: 'web-1', monitored: true }] },
      'DELETE /api/v1/orgs/acme/monitoring-groups/g1': () => {
        deleted += 1;
        return {};
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Actions for Web tier' }));
    expect(screen.getAllByRole('menuitem').map((el) => el.textContent)).toEqual(['Edit', 'Delete…']);
    await user.click(screen.getByRole('menuitem', { name: 'Edit' }));
    expect(await screen.findByRole('dialog', { name: 'Edit Web tier' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Cancel' }));

    await user.click(screen.getByRole('button', { name: 'Actions for Web tier' }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete…' }));
    await waitFor(() => expect(deleted).toBe(1));
    confirmed.mockRestore();
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

    await user.click(await screen.findByRole('button', { name: 'Actions for Web tier' }));
    await user.click(screen.getByRole('menuitem', { name: 'Edit' }));
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

    await user.click(await screen.findByRole('button', { name: 'Actions for Web tier' }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete…' }));
    await waitFor(() => expect(deleted).toBe(1));
    // The operator is told what else goes with it.
    expect(confirmed.mock.calls[0][0]).toContain('check definitions bound to it are deleted');
    confirmed.mockRestore();
  });

  // Review finding 5: the guard used to replace the whole page, taking
  // the only link back to the definitions with it.
  it('keeps the section tabs when the groups list fails', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': () =>
        new Response('{"detail":"boom"}', { status: 500, headers: { 'Content-Type': 'application/json' } }),
    });
    renderPage();

    expect(await screen.findByText('Something went wrong')).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Checks' })).toBeInTheDocument();
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
