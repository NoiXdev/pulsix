import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { GroupsPage } from './GroupsPage.tsx';

const summary = (id: string, name: string, extra = {}) => ({
  id, name, description: '', parent_id: null, members: 0, group_fields: 1, host_fields: 0, complete: true, revision: 1, ...extra,
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
      <I18nProvider locale="de">
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe('GroupsPage in German', () => {
  it('writes the page, its count and the state pills in German', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [summary('w', 'webserver', { complete: false }), summary('p', 'production')] },
      'GET /api/v1/orgs/acme/bastions': { bastions: [] },
    });
    renderPage();

    expect(await screen.findByText('2 Inventargruppen · jede Gruppe besitzt ein Variablenschema')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Gruppe hinzufügen' })).toBeInTheDocument();
    expect(screen.getByText('unvollständig')).toBeInTheDocument();
    expect(screen.getByText('Sprunghost')).toBeInTheDocument();
  });

  it('uses the German singular for one group', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [summary('p', 'production')] },
      'GET /api/v1/orgs/acme/bastions': { bastions: [] },
    });
    renderPage();

    expect(await screen.findByText('1 Inventargruppe · jede Gruppe besitzt ein Variablenschema')).toBeInTheDocument();
  });
});
