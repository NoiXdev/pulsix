import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { GroupsPage } from './GroupsPage.tsx';

const summary = (id: string, name: string, parent_id: string | null, extra = {}) => ({
  id, name, description: '', parent_id, members: 0, group_fields: 1, host_fields: 0, complete: true, revision: 1, ...extra,
});

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const listRoute = createRoute({ getParentRoute: () => orgRoute, path: '/groups', component: GroupsPage });
  const detailRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/groups/$id',
    validateSearch: (s: Record<string, unknown>) => ({ tab: typeof s.tab === 'string' ? s.tab : undefined }),
    component: () => <div>GROUP DETAIL</div>,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([listRoute, detailRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/groups'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('GroupsPage', () => {
  it('renders the tree and creates a group below a parent', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'GET /api/v1/orgs/acme/inventory/groups': {
        groups: [
          summary('w', 'webserver', 'p', { members: 14, host_fields: 1, complete: false, bastion_id: 'b1' }),
          summary('p', 'production', null),
        ],
      },
      'GET /api/v1/orgs/acme/bastions': { bastions: [{ id: 'b1', name: 'bastion-fra', address: 'b.example', port: 22, username: 'ops' }] },
      'POST /api/v1/orgs/acme/inventory/groups': (req: MockRequest) => {
        posted = req.body;
        return { group: { ...summary('n', 'dbserver', 'p'), schema: { fields: [] }, vars: {}, problems: [], orphaned: [], created_at: '', updated_at: '' } };
      },
    });
    renderPage();

    const web = await screen.findByRole('link', { name: 'webserver' });
    expect(web).toHaveStyle({ paddingLeft: '16px' });
    expect(screen.getByRole('link', { name: 'production' })).toHaveStyle({ paddingLeft: '0px' });
    expect(screen.getByText('incomplete')).toBeInTheDocument();
    expect(screen.getByText('bastion-fra')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Add group' }));
    await user.type(screen.getByLabelText('Name'), 'dbserver');
    await pickOption(screen, 'Parent', { value: 'p' });
    await user.click(screen.getByRole('button', { name: 'Create group' }));
    expect(await screen.findByText('GROUP DETAIL')).toBeInTheDocument();
    expect(posted).toEqual({ name: 'dbserver', parent_id: 'p' });
  });
});
